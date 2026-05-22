package dispatch

import (
	"strings"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/queue"
)

const (
	retryPolicyOwner = "core"
	pluginRetryField = "v2_boundary_compatibility_signal"

	retryReasonPluginError       = "plugin_error"
	retryReasonPluginRetryFalse  = "plugin_retry_false"
	retryReasonExitCode78        = "exit_code_78"
	retryReasonTimeout           = "timeout"
	retryReasonSpawnError        = "spawn_error"
	retryReasonNilResponse       = "nil_response"
	retryReasonAttemptsExhausted = "attempts_exhausted"
)

type retryDecision struct {
	Retryable         bool
	Reason            string
	AttemptsExhausted bool
}

func decideRetryPolicy(resp *protocol.Response, compat protocol.ResponseCompat, exitCode int, job *queue.Job, pluginCfg config.PluginConf, defaultReason string) retryDecision {
	reason := strings.TrimSpace(defaultReason)
	if reason == "" {
		reason = retryReasonPluginError
	}

	decision := retryDecision{
		Retryable: true,
		Reason:    reason,
	}

	if exitCode == nonRetryableExitCode {
		decision.Retryable = false
		decision.Reason = retryReasonExitCode78
		return decision
	}

	if resp != nil && resp.Status == "error" && compat.Retry != nil && !*compat.Retry {
		decision.Retryable = false
		decision.Reason = retryReasonPluginRetryFalse
		return decision
	}

	maxAttempts := effectiveMaxAttempts(job, pluginCfg)
	if job != nil && maxAttempts > 0 && job.Attempt >= maxAttempts {
		decision.AttemptsExhausted = true
	}

	return decision
}

func effectiveMaxAttempts(job *queue.Job, pluginCfg config.PluginConf) int {
	if job != nil && job.MaxAttempts > 0 {
		return job.MaxAttempts
	}
	return config.MaxAttemptsForPlugin(pluginCfg)
}
