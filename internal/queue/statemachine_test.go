package queue

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mattjoyce/ductile/internal/storage"
)

// statemachine_test.go writes down the job-queue transition relation as a
// checked artifact (card #117). Ductile is a state machine over a durable
// queue, but the legal (from, event, to) relation lived only in scattered
// store methods. These tests make the relation explicit and assert the real
// store obeys it — in-process against a temp SQLite, no Docker.
//
// Read-back goes through the public store surface (GetJobByID for status,
// GetJobLineage for the cached attempt) so the suite survives internal
// refactors of the SQL.

func newTestQueue(t *testing.T) *Queue {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

func jobStatus(t *testing.T, q *Queue, id string) Status {
	t.Helper()
	res, err := q.GetJobByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJobByID(%s): %v", id, err)
	}
	return res.Status
}

func jobAttempt(t *testing.T, q *Queue, id string) int {
	t.Helper()
	lin, err := q.GetJobLineage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJobLineage(%s): %v", id, err)
	}
	return lin.CachedAttempt
}

// --- The transition relation (the checked artifact) ----------------------
//
// event names the store operation that drives a transition. `stateStart` is
// the pseudo-state a job occupies before it exists (an Enqueue creates it).
const stateStart Status = ""

type event string

const (
	evEnqueue           event = "enqueue"            // Enqueue
	evDequeue           event = "dequeue"            // Dequeue (claim)
	evCompleteSucceeded event = "complete:succeeded" // Complete
	evCompleteSkipped   event = "complete:skipped"   // Complete
	evCompleteFailed    event = "complete:failed"    // Complete
	evCompleteTimedOut  event = "complete:timed_out" // Complete
	evCompleteDead      event = "complete:dead"      // Complete
	evRecoverRequeue    event = "recover:requeue"    // UpdateJobForRecovery -> queued, attempt+1
	evRecoverDead       event = "recover:dead"       // UpdateJobForRecovery -> dead
)

type transition struct {
	from Status
	on   event
	to   Status
}

// legalTransitions is the canonical, written-down relation. Every edge here is
// asserted to be drivable through the real store (TestQueueLegalTransitions...)
// and the set is asserted to be well-formed (TestQueueTransitionRelation...).
// Adding a store transition without adding it here — or vice versa — is the
// drift this artifact exists to catch.
var legalTransitions = []transition{
	{stateStart, evEnqueue, StatusQueued},
	{StatusQueued, evDequeue, StatusRunning},
	{StatusRunning, evCompleteSucceeded, StatusSucceeded},
	{StatusRunning, evCompleteSkipped, StatusSkipped},
	{StatusRunning, evCompleteFailed, StatusFailed},
	{StatusRunning, evCompleteTimedOut, StatusTimedOut},
	{StatusRunning, evCompleteDead, StatusDead},
	{StatusRunning, evRecoverRequeue, StatusQueued},
	{StatusRunning, evRecoverDead, StatusDead},
}

// allStatuses is every Status the queue can persist. Kept beside the relation
// so a newly-added Status with no transitions fails the exhaustiveness test.
var allStatuses = []Status{
	StatusQueued, StatusRunning, StatusSucceeded, StatusSkipped,
	StatusFailed, StatusTimedOut, StatusDead,
}

// terminalStatuses are absorbing: no legal event leaves them.
var terminalStatuses = []Status{
	StatusSucceeded, StatusSkipped, StatusFailed, StatusTimedOut, StatusDead,
}

// completeTargets maps each "complete" event to the terminal status it lands
// in — the data behind Complete's five terminal edges, so applyEvent doesn't
// repeat one case per status.
var completeTargets = map[event]Status{
	evCompleteSucceeded: StatusSucceeded,
	evCompleteSkipped:   StatusSkipped,
	evCompleteFailed:    StatusFailed,
	evCompleteTimedOut:  StatusTimedOut,
	evCompleteDead:      StatusDead,
}

// TestQueueTransitionRelationIsWellFormed checks the artifact itself: every
// status is reachable, terminal states are absorbing (no outgoing edges), and
// every non-terminal state has somewhere to go (no accidental dead-ends).
func TestQueueTransitionRelationIsWellFormed(t *testing.T) {
	t.Parallel()

	reachable := map[Status]bool{}
	outgoing := map[Status]int{}
	for _, tr := range legalTransitions {
		reachable[tr.to] = true
		outgoing[tr.from]++
	}

	for _, s := range allStatuses {
		if !reachable[s] {
			t.Errorf("status %q is unreachable: no legal transition lands in it", s)
		}
	}

	terminal := map[Status]bool{}
	for _, s := range terminalStatuses {
		terminal[s] = true
		if outgoing[s] != 0 {
			t.Errorf("terminal status %q is not absorbing: has %d outgoing transition(s)", s, outgoing[s])
		}
	}

	// Every non-terminal status must have somewhere to go (derived from the two
	// declared sets — no third hardcoded enumeration to drift).
	for _, s := range allStatuses {
		if !terminal[s] && outgoing[s] == 0 {
			t.Errorf("non-terminal status %q is a dead-end: no outgoing transition", s)
		}
	}
}

// setupInState returns the id of a job parked in the given (non-terminal)
// state, reached only through legal store operations.
func setupInState(t *testing.T, q *Queue, state Status) string {
	t.Helper()
	switch state {
	case stateStart:
		return "" // no job yet; the event under test (Enqueue) creates it
	case StatusQueued:
		id, err := q.Enqueue(context.Background(), EnqueueRequest{Plugin: "echo", Command: "poll", SubmittedBy: "test", MaxAttempts: 3})
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		return id
	case StatusRunning:
		return enqueueRunning(t, q)
	default:
		t.Fatalf("setupInState: %q is terminal/unsupported as a from-state", state)
		return ""
	}
}

// applyEvent drives one store operation and returns the id of the job that
// should now hold the transition's `to` state.
func applyEvent(t *testing.T, q *Queue, id string, on event) string {
	t.Helper()
	ctx := context.Background()
	if status, ok := completeTargets[on]; ok {
		mustComplete(t, q, id, status)
		return id
	}
	switch on {
	case evEnqueue:
		newID, err := q.Enqueue(ctx, EnqueueRequest{Plugin: "echo", Command: "poll", SubmittedBy: "test", MaxAttempts: 3})
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		return newID
	case evDequeue:
		job, err := q.Dequeue(ctx)
		if err != nil {
			t.Fatalf("Dequeue: %v", err)
		}
		if job == nil || job.ID != id {
			t.Fatalf("Dequeue claimed %#v, want id %s", job, id)
		}
		return id
	case evRecoverRequeue:
		if err := q.UpdateJobForRecovery(ctx, id, StatusQueued, jobAttempt(t, q, id)+1, nil, "recovered"); err != nil {
			t.Fatalf("UpdateJobForRecovery(queued): %v", err)
		}
	case evRecoverDead:
		if err := q.UpdateJobForRecovery(ctx, id, StatusDead, jobAttempt(t, q, id), nil, "attempts exhausted"); err != nil {
			t.Fatalf("UpdateJobForRecovery(dead): %v", err)
		}
	default:
		t.Fatalf("applyEvent: unknown event %q", on)
	}
	return id
}

func mustComplete(t *testing.T, q *Queue, id string, status Status) {
	t.Helper()
	if err := q.Complete(context.Background(), id, status, nil, nil); err != nil {
		t.Fatalf("Complete(%s, %s): %v", id, status, err)
	}
}

// TestQueueLegalTransitionsHoldInStore drives every edge of the written-down
// relation through the real store and asserts the job lands in the declared
// `to` state. This is what binds the artifact to reality.
func TestQueueLegalTransitionsHoldInStore(t *testing.T) {
	t.Parallel()
	for _, tr := range legalTransitions {
		name := string(tr.from) + "--" + string(tr.on) + "-->" + string(tr.to)
		if tr.from == stateStart {
			name = "start" + name
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			q := newTestQueue(t)
			id := setupInState(t, q, tr.from)
			checkID := applyEvent(t, q, id, tr.on)
			if got := jobStatus(t, q, checkID); got != tr.to {
				t.Fatalf("after %s from %q: status = %q, want %q", tr.on, tr.from, got, tr.to)
			}
		})
	}
}

// enqueueRunning enqueues a job and claims it, leaving it in `running` — the
// pre-state for the crash-recovery and completion transitions.
func enqueueRunning(t *testing.T, q *Queue) string {
	t.Helper()
	ctx := context.Background()
	id, err := q.Enqueue(ctx, EnqueueRequest{Plugin: "echo", Command: "poll", SubmittedBy: "test", MaxAttempts: 3})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	job, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if job == nil || job.ID != id || job.Status != StatusRunning {
		t.Fatalf("expected running job %s, got %#v", id, job)
	}
	return id
}

// TestQueueCrashRecoveryRequeuesOrphanedRunning is the tracer: the single
// highest-value invariant. A job left `running` by a crash must, on recovery,
// return to `queued` with attempt+1 and be claimable again — never lost,
// never silently re-run under the old attempt number.
func TestQueueCrashRecoveryRequeuesOrphanedRunning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	q := newTestQueue(t)

	id := enqueueRunning(t, q)
	before := jobAttempt(t, q, id)

	// Simulate crash recovery: the orphaned `running` job is re-queued with a
	// bumped attempt (mirrors Dispatcher.failOrRetry's retryable path).
	if err := q.UpdateJobForRecovery(ctx, id, StatusQueued, before+1, nil, "recovered after restart"); err != nil {
		t.Fatalf("UpdateJobForRecovery: %v", err)
	}

	if got := jobStatus(t, q, id); got != StatusQueued {
		t.Fatalf("recovered status = %q, want %q", got, StatusQueued)
	}
	if got := jobAttempt(t, q, id); got != before+1 {
		t.Fatalf("recovered attempt = %d, want %d", got, before+1)
	}

	// The recovered job must be claimable again (recovery is not a dead-end).
	job, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue after recovery: %v", err)
	}
	if job == nil || job.ID != id {
		t.Fatalf("recovered job not re-claimable, got %#v", job)
	}
}

// TestQueueTimedOutIsReachableAndDistinctFromFailed pins that `timed_out` is a
// first-class terminal state, not folded into `failed`. The dispatcher routes
// SIGTERM→SIGKILL kills here; collapsing the two would lose the distinction an
// operator (or agent) needs to tell "the plugin errored" from "it ran too long".
func TestQueueTimedOutIsReachableAndDistinctFromFailed(t *testing.T) {
	t.Parallel()
	q := newTestQueue(t)

	timedOut := enqueueRunning(t, q)
	mustComplete(t, q, timedOut, StatusTimedOut)

	failed := enqueueRunning(t, q)
	mustComplete(t, q, failed, StatusFailed)

	gotTimedOut := jobStatus(t, q, timedOut)
	gotFailed := jobStatus(t, q, failed)
	if gotTimedOut != StatusTimedOut {
		t.Fatalf("timed-out job status = %q, want %q", gotTimedOut, StatusTimedOut)
	}
	if gotFailed != StatusFailed {
		t.Fatalf("failed job status = %q, want %q", gotFailed, StatusFailed)
	}
	if gotTimedOut == gotFailed {
		t.Fatal("timed_out and failed collapsed to the same status")
	}
}

// TestQueueTerminalStatesAreAbsorbing asserts the operational half of the
// absorbing property: once a job reaches a terminal state it is never
// re-claimed by Dequeue (no edge leaves it via the normal progression path).
func TestQueueTerminalStatesAreAbsorbing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, terminal := range terminalStatuses {
		t.Run(string(terminal), func(t *testing.T) {
			t.Parallel()
			q := newTestQueue(t)
			id := enqueueRunning(t, q)
			mustComplete(t, q, id, terminal)

			job, err := q.Dequeue(ctx)
			if err != nil {
				t.Fatalf("Dequeue: %v", err)
			}
			if job != nil {
				t.Fatalf("terminal %q job was re-claimed by Dequeue: %#v", terminal, job)
			}
			if got := jobStatus(t, q, id); got != terminal {
				t.Fatalf("terminal job drifted from %q to %q", terminal, got)
			}
		})
	}
}

// TestQueueCompleteRejectsNonTerminalTarget pins the store guard that keeps the
// machine honest: Complete may only move a job to a terminal state, so it can
// never be used to smuggle a job back to `queued`/`running`.
func TestQueueCompleteRejectsNonTerminalTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, nonTerminal := range []Status{StatusQueued, StatusRunning} {
		t.Run(string(nonTerminal), func(t *testing.T) {
			t.Parallel()
			q := newTestQueue(t)
			id := enqueueRunning(t, q)
			if err := q.Complete(ctx, id, nonTerminal, nil, nil); err == nil {
				t.Fatalf("Complete to non-terminal %q was accepted; want rejection", nonTerminal)
			}
			if got := jobStatus(t, q, id); got != StatusRunning {
				t.Fatalf("job left %q after rejected Complete, want still running", got)
			}
		})
	}
}
