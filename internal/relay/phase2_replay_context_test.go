package relay

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/router/dsl"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

// phase2ReplayRouter returns a fixed dispatch list independent of input.
// Used to characterize P2-08: a signed relay replay must not grow durable
// event_context lineage when the queue would dedupe the resulting jobs.
type phase2ReplayRouter struct {
	dispatches []router.Dispatch
}

func (r phase2ReplayRouter) Next(context.Context, router.Request) ([]router.Dispatch, error) {
	out := make([]router.Dispatch, len(r.dispatches))
	copy(out, r.dispatches)
	return out, nil
}

func (r phase2ReplayRouter) GetNode(string, string) (dsl.Node, bool) {
	return dsl.Node{}, false
}

func TestPhase2RelayReplayDoesNotCreateContextsForDedupedJobs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	q := queue.New(db, queue.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	contexts := state.NewContextStore(db)
	key := "relay:home-primary:home-primary:origin-event-1"
	r := &Receiver{
		queue:    q,
		contexts: contexts,
		admitter: state.NewAdmitter(q, state.DefaultMaxContextBytes),
		router: phase2ReplayRouter{dispatches: []router.Dispatch{
			{
				Plugin:             "branch_a",
				Command:            "handle",
				Event:              protocol.Event{Type: "backup.ready", DedupeKey: key},
				PipelineName:       "relay-consume-a",
				StepID:             "a",
				PipelineInstanceID: "instance-a-1",
				RouteDepth:         1,
				RouteMaxDepth:      1,
			},
			{
				Plugin:             "branch_b",
				Command:            "handle",
				Event:              protocol.Event{Type: "backup.ready", DedupeKey: key},
				PipelineName:       "relay-consume-b",
				StepID:             "b",
				PipelineInstanceID: "instance-b-1",
				RouteDepth:         1,
				RouteMaxDepth:      1,
			},
		}},
	}

	first := rootAcceptance{
		LocalEvent: protocol.Event{Type: "backup.ready", EventID: "receiver-event-1", DedupeKey: key},
		Peer:       trustedPeer{Name: "home-primary"},
	}
	if _, err := r.enqueueRootDispatches(ctx, first); err != nil {
		t.Fatalf("first enqueueRootDispatches: %v", err)
	}

	for i := 0; i < 2; i++ {
		job, err := q.DequeueEligible(ctx, nil, nil)
		if err != nil {
			t.Fatalf("dequeue first delivery job %d: %v", i, err)
		}
		if job == nil {
			t.Fatalf("missing first delivery job %d", i)
		}
		result := "ok"
		if err := q.Complete(ctx, job.ID, queue.StatusSucceeded, &result, nil); err != nil {
			t.Fatalf("complete first delivery job %s: %v", job.ID, err)
		}
	}

	before := countEventContexts(t, db)
	if before != 4 {
		t.Fatalf("context count after first delivery = %d, want 4", before)
	}

	replay := rootAcceptance{
		LocalEvent: protocol.Event{Type: "backup.ready", EventID: "receiver-event-2", DedupeKey: key},
		Peer:       trustedPeer{Name: "home-primary"},
	}
	if _, err := r.enqueueRootDispatches(ctx, replay); err != nil {
		t.Fatalf("replay enqueueRootDispatches: %v", err)
	}

	after := countEventContexts(t, db)
	if after != before {
		t.Fatalf("replay created %d additional contexts for deduped jobs; before=%d after=%d", after-before, before, after)
	}
}

func TestPhase2RelayReplayContextGrowthIsZeroUnderReplayPressure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	q := queue.New(db, queue.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	contexts := state.NewContextStore(db)
	key := "relay:home-primary:home-primary:pressure-event"
	r := &Receiver{
		queue:    q,
		contexts: contexts,
		admitter: state.NewAdmitter(q, state.DefaultMaxContextBytes),
		router: phase2ReplayRouter{dispatches: []router.Dispatch{
			{
				Plugin:             "branch_a",
				Command:            "handle",
				Event:              protocol.Event{Type: "backup.ready", DedupeKey: key},
				PipelineName:       "relay-consume-a",
				StepID:             "a",
				PipelineInstanceID: "instance-a-pressure",
				RouteDepth:         1,
				RouteMaxDepth:      1,
			},
			{
				Plugin:             "branch_b",
				Command:            "handle",
				Event:              protocol.Event{Type: "backup.ready", DedupeKey: key},
				PipelineName:       "relay-consume-b",
				StepID:             "b",
				PipelineInstanceID: "instance-b-pressure",
				RouteDepth:         1,
				RouteMaxDepth:      1,
			},
		}},
	}

	first := rootAcceptance{
		LocalEvent: protocol.Event{Type: "backup.ready", EventID: "receiver-event-1", DedupeKey: key},
		Peer:       trustedPeer{Name: "home-primary"},
	}
	if _, err := r.enqueueRootDispatches(ctx, first); err != nil {
		t.Fatalf("first enqueueRootDispatches: %v", err)
	}
	for i := 0; i < 2; i++ {
		job, err := q.DequeueEligible(ctx, nil, nil)
		if err != nil {
			t.Fatalf("dequeue first delivery job %d: %v", i, err)
		}
		if job == nil {
			t.Fatalf("missing first delivery job %d", i)
		}
		result := "ok"
		if err := q.Complete(ctx, job.ID, queue.StatusSucceeded, &result, nil); err != nil {
			t.Fatalf("complete first delivery job %s: %v", job.ID, err)
		}
	}

	before := countEventContexts(t, db)
	if before != 4 {
		t.Fatalf("context count after first delivery = %d, want 4", before)
	}

	const replays = 25
	for i := 0; i < replays; i++ {
		replay := rootAcceptance{
			LocalEvent: protocol.Event{Type: "backup.ready", EventID: "receiver-replay", DedupeKey: key},
			Peer:       trustedPeer{Name: "home-primary"},
		}
		if _, err := r.enqueueRootDispatches(ctx, replay); err != nil {
			t.Fatalf("replay %d enqueueRootDispatches: %v", i+1, err)
		}
	}

	after := countEventContexts(t, db)
	if gotGrowth := after - before; gotGrowth != 0 {
		t.Fatalf("replay-pressure context growth = %d, want 0 for %d exact replays; before=%d after=%d", gotGrowth, replays, before, after)
	}
}

func countEventContexts(t *testing.T, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM event_context;`).Scan(&n); err != nil {
		t.Fatalf("count event_context: %v", err)
	}
	return n
}
