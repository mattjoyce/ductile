package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os/exec"
	"time"

	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/protocol"
)

type subprocessExecutor struct {
	events *events.Hub
	// extraEnv lists operator-granted environment variable names passed through
	// to plugin children on top of the built-in allowlist (see buildPluginEnv).
	extraEnv []string
	// account is the resolved privsep identity this plugin spawns under. The zero
	// value (Confined=false) means unconfined — spawn at the gateway uid, as today.
	account ResolvedAccount
}

func newSubprocessExecutor(events *events.Hub, extraEnv []string, account ResolvedAccount) *subprocessExecutor {
	return &subprocessExecutor{events: events, extraEnv: extraEnv, account: account}
}

// publishDropFailed emits the privsep drop-failure signal — its own event, distinct
// from a generic spawn failure, so an operator (and #90) can tell "the wall could
// not be built" from "the binary is missing."
func (e *subprocessExecutor) publishDropFailed(req *protocol.Request, pluginName string, cause error) {
	if e.events == nil {
		return
	}
	e.events.Publish("plugin.drop_failed", map[string]any{
		"job_id":  req.JobID,
		"plugin":  pluginName,
		"account": e.account.Name,
		"uid":     e.account.UID,
		"gid":     e.account.GID,
		"error":   cause.Error(),
	})
}

// spawnPlugin spawns the plugin subprocess, writes the request to stdin, and reads the response from stdout.
// Returns the parsed response, raw response bytes, raw stdout bytes, stderr output, exit code, and any error.
func (e *subprocessExecutor) execute(
	ctx context.Context,
	pluginName string,
	entrypoint string,
	req *protocol.Request,
	timeout time.Duration,
	logger *slog.Logger,
) (*protocol.Response, protocol.ResponseCompat, json.RawMessage, []byte, string, int, error) {
	// Create timer for timeout enforcement
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	// Prepare command (don't use CommandContext - we'll manage termination ourselves).
	// The entrypoint is invoked with NO arguments: secrets never travel on argv
	// (which is world-readable via /proc), only over the stdin request channel.
	cmd := exec.Command(entrypoint)
	// Replace full-environment inheritance with a minimal allowlist so the child
	// never receives a copy of the gateway's environment (1a spawn hygiene).
	cmd.Env = buildPluginEnv(e.extraEnv)
	// #109 runtime contract: a confined plugin gets a usable runtime rooted at the
	// account's own state_dir — a writable HOME + cache (env) and working directory
	// (cmd.Dir). Privsep gave a dropped account no writable home, and (before the
	// 0711 traversal floor) no way to even reach its state_dir, so any plugin that
	// wrote state, or a runtime needing a cache, failed closed. cmd.Dir alone can't
	// fix it (Go drops the uid before chdir), so HOME/XDG_CACHE_HOME travel as env;
	// with the 0711 floor the dropped uid can now also chdir into the dir it owns.
	// Unconfined plugins are untouched — gateway uid, gateway HOME/cwd, as before.
	if e.account.Confined && e.account.StateDir != "" {
		cmd.Env = withAccountRuntimeEnv(cmd.Env, e.account.StateDir)
		cmd.Dir = e.account.StateDir
	}
	configurePluginProcess(cmd)
	// Compose the privsep uid/gid drop onto the lifecycle-configured command. A
	// confined account on a platform that cannot drop fails the spawn closed here —
	// never a silent run at gateway privilege (PrivSec ADR §3 Layer 1b, §8).
	if err := applyAccountCredential(cmd, e.account); err != nil {
		e.publishDropFailed(req, pluginName, err)
		return nil, protocol.ResponseCompat{}, nil, nil, "", 0, fmt.Errorf("%w: %v", ErrAccountDropFailed, err)
	}

	// Prepare stdin pipe
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, protocol.ResponseCompat{}, nil, nil, "", 0, fmt.Errorf("create stdin pipe: %w", err)
	}

	// Capture stdout and stderr with hard memory caps. Stdout is the protocol
	// channel, so overflow is a protocol failure; stderr is diagnostic evidence
	// and is truncated without failing an otherwise valid response.
	stdout := newBoundedBuffer(maxStdoutBytes)
	stderr := newBoundedBuffer(maxStderrBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	logger.Info("plugin executing", "entrypoint", entrypoint, "timeout", timeout)

	// Start the process. When the plugin is confined, the kernel performs the
	// uid/gid drop in the fork-child before execve — so a Start failure on a
	// confined spawn that is NOT a missing binary is a failed drop, not a generic
	// spawn error. Classify it as such so it fails closed and terminal (no retry).
	if err := cmd.Start(); err != nil {
		if e.account.Confined && !errors.Is(err, fs.ErrNotExist) {
			e.publishDropFailed(req, pluginName, err)
			return nil, protocol.ResponseCompat{}, nil, nil, "", 0, fmt.Errorf("%w (account %q uid %d): %v", ErrAccountDropFailed, e.account.Name, e.account.UID, err)
		}
		return nil, protocol.ResponseCompat{}, nil, nil, "", 0, fmt.Errorf("start process: %w", err)
	}

	if e.events != nil {
		e.events.Publish("plugin.spawned", map[string]any{
			"job_id":  req.JobID,
			"plugin":  pluginName,
			"command": req.Command,
			"pid":     cmd.Process.Pid,
		})
	}

	// Write request to stdin in a goroutine
	writeErr := make(chan error, 1)
	go func() {
		if err := protocol.EncodeRequest(stdin, req); err != nil {
			if closeErr := stdin.Close(); closeErr != nil {
				writeErr <- fmt.Errorf("encode request: %w (close stdin: %v)", err, closeErr)
				return
			}
			writeErr <- fmt.Errorf("encode request: %w", err)
			return
		}
		if err := stdin.Close(); err != nil {
			writeErr <- fmt.Errorf("close stdin: %w", err)
			return
		}
		writeErr <- nil
	}()

	// Wait for process to complete or timeout
	waitErr := make(chan error, 1)
	go func() {
		waitErr <- cmd.Wait()
	}()

	// decodeCompletion handles a process that finished (whether the timer
	// fired or not). A process that produced its output and exited has done
	// its work — the timeout branch must never discard that result. waitErr
	// carries the exec.Cmd.Wait error (nil, *exec.ExitError, or fatal).
	decodeCompletion := func(err error) (*protocol.Response, protocol.ResponseCompat, json.RawMessage, []byte, string, int, error) {
		werr := <-writeErr

		stdoutBytes := stdout.Bytes()
		stderrStr := stderr.String()
		if stderr.Truncated() {
			logger.Warn("plugin stderr truncated", "limit_bytes", maxStderrBytes)
		}
		exitCode := 0

		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
				logger.Warn("plugin exited with non-zero status", "exit_code", exitCode)
			} else {
				return nil, protocol.ResponseCompat{}, nil, stdoutBytes, stderrStr, 0, fmt.Errorf("wait for process: %w", err)
			}
		}

		if stdout.Truncated() {
			logger.Error("plugin stdout exceeded capture limit", "limit_bytes", maxStdoutBytes)
			return nil, protocol.ResponseCompat{}, json.RawMessage(stdoutBytes), stdoutBytes, stderrStr, exitCode, outputLimitError{stream: "stdout", limit: maxStdoutBytes}
		}

		resp, compat, rawBytes, decErr := protocol.DecodeResponseLenient(bytes.NewReader(stdoutBytes))
		if decErr != nil {
			if werr != nil {
				logger.Warn("stdin write failed (process may not read stdin)", "error", werr)
			}
			logger.Error("failed to decode plugin response", "error", decErr, "stdout", string(rawBytes))
			return nil, protocol.ResponseCompat{}, json.RawMessage(rawBytes), stdoutBytes, stderrStr, exitCode, fmt.Errorf("decode response: %w", decErr)
		}

		// Some plugins don't read stdin and may exit before the write
		// completes; not a failure if a valid response was produced.
		if werr != nil {
			logger.Debug("stdin write error (ignored, valid response received)", "error", werr)
		}

		return resp, compat, json.RawMessage(rawBytes), stdoutBytes, stderrStr, exitCode, nil
	}

	// Wait for completion or timeout
	select {
	case <-timeoutTimer.C:
		// Timer fired — but the process may have completed in the same
		// instant the timer expired (the OPS-001 race). Prefer a ready
		// result over declaring a timeout.
		select {
		case err := <-waitErr:
			logger.Debug("plugin completed at timeout edge, preferring result over timeout")
			return decodeCompletion(err)
		default:
		}

		// Genuinely still running - enforce termination.
		logger.Warn("plugin execution timed out, sending SIGTERM")
		if err := terminatePluginProcess(cmd); err != nil {
			logger.Error("failed to send SIGTERM", "error", err)
		}

		// Wait for grace period
		grace := time.NewTimer(terminationGracePeriod)
		defer grace.Stop()

		select {
		case <-waitErr:
			// The process exited in response to SIGTERM. It still exceeded
			// its configured timeout, so it is a timeout regardless of any
			// output produced during the grace period — anything later than
			// the deadline must not be reclassified as success. (C-FRO-5 is
			// strictly the deadline-edge race handled by the non-blocking
			// pre-check above; the grace period is not in scope.)
			logger.Info("plugin exited after SIGTERM (still a timeout)")
		case <-grace.C:
			// Grace period expired, send SIGKILL. Only a process that had
			// to be killed never produced a result — that is the timeout.
			logger.Warn("plugin did not exit after SIGTERM, sending SIGKILL")
			if err := killPluginProcess(cmd); err != nil {
				logger.Error("failed to send SIGKILL", "error", err)
			}
			<-waitErr // Wait for process to die
		}

		if stdout.Truncated() {
			logger.Warn("plugin stdout exceeded capture limit", "limit_bytes", maxStdoutBytes)
		}
		if stderr.Truncated() {
			logger.Warn("plugin stderr truncated", "limit_bytes", maxStderrBytes)
		}
		stderrStr := stderr.String()
		return nil, protocol.ResponseCompat{}, nil, stdout.Bytes(), stderrStr, 0, context.DeadlineExceeded

	case err := <-waitErr:
		return decodeCompletion(err)
	}
}
