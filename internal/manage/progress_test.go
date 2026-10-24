package manage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func TestApplyProgressCountsFailedWorkWithoutChangingResult(t *testing.T) {
	f := setup(t)
	i := f.npm("one", "example")
	plan := f.plan(Selection{Action: "update", IDs: []string{i.ID}})
	var events []audit.Progress
	f.o.Progress = func(p audit.Progress) { events = append(events, p) }
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) { return nil, errors.New("controlled failure") }))
	result, err := Apply(context.Background(), plan, plan.PlanID, e)
	if err != nil || len(result.Results) != 1 || result.Results[0].Status != "failed" {
		t.Fatal(result, err)
	}
	last := events[len(events)-1]
	if last.Phase != "apply" || last.State != "finished" || last.Completed != 1 || last.Unresolved != 1 || *last.Total != 1 {
		t.Fatal(last)
	}
	verifications := 0
	for _, event := range events {
		if event.Phase == "verify" && event.State == "finished" {
			verifications++
		}
	}
	if verifications != 3 {
		t.Fatalf("fresh verification phases: %d", verifications)
	}
}

func TestApplyProgressVisibleDuringSlowExecutionAndCancelledCounts(t *testing.T) {
	f := setup(t)
	i := f.npm("one", "example")
	plan := f.plan(Selection{Action: "update", IDs: []string{i.ID}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan audit.Progress, 100)
	f.o.Progress = func(p audit.Progress) { events <- p }
	started := make(chan struct{})
	e := f.engine(runFunc(func(ctx context.Context, _ Command) ([]byte, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	done := make(chan error, 1)
	go func() { _, err := Apply(ctx, plan, plan.PlanID, e); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("controlled manager did not start")
	}
	seen := false
	for len(events) > 0 {
		p := <-events
		seen = seen || p.Phase == "apply" && p.State == "running" && p.Current == i.Name && p.Completed == 0
	}
	if !seen {
		t.Fatal("no progress before slow execution completed")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("execution did not cancel")
	}
	var last audit.Progress
	for len(events) > 0 {
		last = <-events
	}
	if last.Phase != "apply" || last.State != "cancelled" || last.Completed != 0 {
		t.Fatal("cancelled work counted as processed", last)
	}
}
