package localmcp

import (
	"context"
	"os"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func TestMissingCheckpointAfterClaimIsUnknown(t *testing.T) {
	for _, kind := range []string{"decision-lock", "decision", "worker"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, 1)
			s := f.service(t)
			id := savePlan(t, s, f.Plan)
			requireOperation(t, s, id, "not_started")
			marker, e := s.store.claim(id, kind)
			if e != nil {
				t.Fatal(e)
			}
			marker.Close()
			op := requireOperation(t, s, id, "unknown")
			if op.Reason != "operation_record_missing_do_not_retry" {
				t.Fatal(op)
			}
		})
	}
}
func TestWorkerDeadlineContextReachesPlanRebuild(t *testing.T) {
	f := newFixture(t, 1)
	s := f.service(t)
	id := savePlan(t, s, f.Plan)
	if e := s.store.write(id, "decision", decision{PlanID: f.Plan.PlanID, Approved: true}, true); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := runWorker(ctx, s.store, id, func(manage.Plan) manage.Engine {
		engine := f.engine(runnerFunc(func(context.Context, manage.Command) ([]byte, error) { t.Error("unexpected mutation"); return nil, nil }))
		engine.Snapshot = func(context.Context) (audit.Report, error) { cancel(); return f.Report, nil }
		return engine
	})
	if e == nil {
		t.Fatal("cancelled rebuild was allowed")
	}
	requireOperation(t, s, id, "failed")
	attempt := f.Home + "/actions/" + f.Plan.PlanID + ".attempted"
	if _, e := os.Stat(attempt); !os.IsNotExist(e) {
		t.Fatal("cancelled rebuild claimed Apply")
	}
}

func TestFinishedOperationNeedsCompleteItemResults(t *testing.T) {
	f := newFixture(t, 1)
	s := f.service(t)
	id := savePlan(t, s, f.Plan)
	op := Operation{ID: id, PlanHandle: id, PlanID: f.Plan.PlanID, State: "finished"}
	if e := s.store.write(id, "operation", op, true); e != nil {
		t.Fatal(e)
	}
	if _, e := s.operation(id); e == nil {
		t.Fatal("empty finished operation accepted")
	}
}
