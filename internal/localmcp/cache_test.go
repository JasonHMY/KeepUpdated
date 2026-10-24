package localmcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func cachePlan(p manage.Plan, created time.Time) manage.Plan {
	p.CreatedAt = created.UTC().Format(time.RFC3339Nano)
	p.ExpiresAt = created.Add(30 * time.Minute).UTC().Format(time.RFC3339Nano)
	p.PlanID = ""
	b, _ := json.Marshal(p)
	hash := sha256.Sum256(b)
	p.PlanID = hex.EncodeToString(hash[:])
	return p
}
func countCache(t *testing.T, s store, kind string) int {
	t.Helper()
	entries, e := os.ReadDir(s.dir)
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), "."+kind) {
			count++
		}
	}
	return count
}
func fillPreviews(t *testing.T, s *service, plan manage.Plan, count int) {
	t.Helper()
	start := time.Now().Add(-time.Second)
	for n := 0; n < count; n++ {
		if _, e := s.savePlan(cachePlan(plan, start.Add(time.Duration(n)*time.Nanosecond))); e != nil {
			t.Fatalf("preview %d: %v", n, e)
		}
	}
}
func TestSnapshotRetentionContinuesNormalUse(t *testing.T) {
	s := store{filepath.Join(canonicalTemp(t), "state")}
	first, _ := handle()
	if e := s.saveCached(first, "snapshot", map[string]int{"n": 0}); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for n := 1; n <= 100; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id, e := handle()
			if e == nil {
				e = s.saveCached(id, "snapshot", map[string]int{"n": n})
			}
			errs <- e
		}(n)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if countCache(t, s, "snapshot") != retainedRecords {
		t.Fatal("snapshot cache not bounded")
	}
	var out map[string]int
	if e := s.readCached(first, "snapshot", &out); e == nil || e.Error() != "snapshot_handle_unavailable_or_expired" {
		t.Fatalf("evicted snapshot: %v", e)
	}
}
func TestUnsubmittedPreviewRetentionAndReplayRefusal(t *testing.T) {
	f := newFixture(t, 1)
	s := f.service(t)
	first, e := s.savePlan(f.Plan)
	if e != nil {
		t.Fatal(e)
	}
	fillPreviews(t, s, f.Plan, 100)
	if countCache(t, s.store, "plan") != retainedRecords || countCache(t, s.store, "plan-handle") != retainedRecords {
		t.Fatal("preview or alias cache not bounded")
	}
	if _, e = s.request(context.Background(), first); e == nil || e.Error() != "plan_handle_unavailable_or_expired" {
		t.Fatalf("evicted submission: %v", e)
	}
	if _, e = s.operation(first); e == nil || e.Error() != "operation_unavailable_or_expired" {
		t.Fatalf("evicted operation: %v", e)
	}
	path, _ := s.store.path(first, "decision-lock")
	if _, e = os.Lstat(path); !os.IsNotExist(e) {
		t.Fatal("evicted handle was recreated")
	}
}
func TestRetentionPreservesActiveUnknownAndDamagedOperations(t *testing.T) {
	for _, mode := range []string{"active", "unknown", "missing-checkpoint", "damaged"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, 1)
			s := f.service(t)
			p := cachePlan(f.Plan, time.Now().Add(-time.Hour))
			id := savePlan(t, s, p)
			marker, e := s.store.claim(id, "worker")
			if e != nil {
				t.Fatal(e)
			}
			if mode == "active" {
				defer marker.Close()
			} else {
				marker.Close()
			}
			if mode != "missing-checkpoint" {
				op := Operation{ID: id, PlanHandle: id, PlanID: p.PlanID, State: "running"}
				if mode == "damaged" {
					op.State = "finished"
				}
				if e = s.store.write(id, "operation", op, true); e != nil {
					t.Fatal(e)
				}
			}
			fillPreviews(t, s, f.Plan, 100)
			var retained manage.Plan
			if e = s.store.read(id, "plan", &retained); e != nil {
				t.Fatalf("lost %s evidence: %v", mode, e)
			}
			if countCache(t, s.store, "plan") != retainedRecords {
				t.Fatal("plan cache not bounded")
			}
		})
	}
}
func TestRetentionEvictsExpiredTerminalWithoutRemovingAttemptedProtection(t *testing.T) {
	for _, state := range []string{"finished", "not_started", "failed"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t, 1)
			s := f.service(t)
			p := cachePlan(f.Plan, time.Now().Add(-time.Hour))
			id := savePlan(t, s, p)
			if e := s.store.write(p.PlanID, "plan-handle", id, true); e != nil {
				t.Fatal(e)
			}
			result := &manage.ApplyResult{Schema: manage.ApplySchema, PlanID: p.PlanID}
			if state == "finished" {
				result.State = "finished"
				for _, item := range p.Items {
					result.OriginalIDs = append(result.OriginalIDs, item.ID)
					result.Results = append(result.Results, manage.ItemResult{ID: item.ID, Status: "updated"})
				}
			}
			op := Operation{ID: id, PlanHandle: id, PlanID: p.PlanID, State: state, Result: result}
			if e := s.store.write(id, "operation", op, true); e != nil {
				t.Fatal(e)
			}
			marker, e := s.store.claim(id, "decision-lock")
			if e != nil {
				t.Fatal(e)
			}
			marker.Close()
			attempt := filepath.Join(f.Home, "actions", p.PlanID+".attempted")
			writeTest(t, attempt, []byte("must remain"), 0600)
			fillPreviews(t, s, f.Plan, 64)
			if _, e = s.request(context.Background(), id); e == nil {
				t.Fatal("evicted terminal replay accepted")
			}
			if b, e := os.ReadFile(attempt); e != nil || string(b) != "must remain" {
				t.Fatal("attempt protection removed")
			}
			if _, e = s.savePlan(p); e == nil {
				t.Fatal("expired digest recreated")
			}
			var alias string
			if e = s.store.read(p.PlanID, "plan-handle", &alias); !os.IsNotExist(e) {
				t.Fatal("evicted alias retained")
			}
		})
	}
}
func TestProtectedCapacityIsTemporaryAndDoesNotDiscardEvidence(t *testing.T) {
	f := newFixture(t, 1)
	s := f.service(t)
	now := time.Now().Add(-time.Second)
	for n := 0; n < retainedRecords; n++ {
		p := cachePlan(f.Plan, now.Add(time.Duration(n)*time.Nanosecond))
		id := savePlan(t, s, p)
		op := Operation{ID: id, PlanHandle: id, PlanID: p.PlanID, State: "not_started"}
		if e := s.store.write(id, "operation", op, true); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.savePlan(cachePlan(f.Plan, now.Add(time.Millisecond))); e == nil {
		t.Fatal("discarded unexpired decisions")
	}
	if countCache(t, s.store, "plan") != retainedRecords {
		t.Fatal("protected evidence lost")
	}
}

func TestRetentionPreservesUncertainItemsInFinishedOperation(t *testing.T) {
	for _, status := range []string{"verification_unknown", "failed", "cleanup_failed", "unfamiliar_status"} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t, 1)
			s := f.service(t)
			p := cachePlan(f.Plan, time.Now().Add(-time.Hour))
			id := savePlan(t, s, p)
			result := &manage.ApplyResult{Schema: manage.ApplySchema, PlanID: p.PlanID, State: "finished", OriginalIDs: []string{p.Items[0].ID}, Results: []manage.ItemResult{{ID: p.Items[0].ID, Status: status, RecoveryPath: "retained recovery evidence"}}}
			op := Operation{ID: id, PlanHandle: id, PlanID: p.PlanID, State: "finished", Result: result}
			if e := s.store.write(id, "operation", op, true); e != nil {
				t.Fatal(e)
			}
			fillPreviews(t, s, f.Plan, 64)
			retained, e := s.operation(id)
			if e != nil || retained.Result.Results[0].RecoveryPath != "retained recovery evidence" {
				t.Fatalf("lost uncertain item: %+v %v", retained, e)
			}
		})
	}
}
func TestRetentionPreflightsEveryAssociatedRecord(t *testing.T) {
	for _, fault := range []string{"worker-mode", "worker-hardlink", "decision-json", "decision-binding", "decision-mode"} {
		t.Run(fault, func(t *testing.T) {
			f := newFixture(t, 1)
			s := f.service(t)
			p := cachePlan(f.Plan, time.Now().Add(-time.Hour))
			id := savePlan(t, s, p)
			result := &manage.ApplyResult{Schema: manage.ApplySchema, PlanID: p.PlanID, State: "finished", OriginalIDs: []string{p.Items[0].ID}, Results: []manage.ItemResult{{ID: p.Items[0].ID, Status: "updated"}}}
			op := Operation{ID: id, PlanHandle: id, PlanID: p.PlanID, State: "finished", Result: result}
			if e := s.store.write(id, "operation", op, true); e != nil {
				t.Fatal(e)
			}
			worker, e := s.store.claim(id, "worker")
			if e != nil {
				t.Fatal(e)
			}
			worker.Close()
			if e = s.store.write(id, "decision", decision{PlanID: p.PlanID, Approved: true}, true); e != nil {
				t.Fatal(e)
			}
			path, _ := s.store.path(id, "worker")
			decisionPath, _ := s.store.path(id, "decision")
			switch fault {
			case "worker-mode":
				e = os.Chmod(path, 0644)
			case "worker-hardlink":
				e = os.Link(path, filepath.Join(f.Home, "worker-hardlink"))
			case "decision-json":
				e = os.WriteFile(decisionPath, []byte("broken"), 0600)
			case "decision-binding":
				e = s.store.write(id, "decision", decision{PlanID: strings.Repeat("f", 64), Approved: true}, false)
			case "decision-mode":
				e = os.Chmod(decisionPath, 0644)
			}
			if e != nil {
				t.Fatal(e)
			}
			fillPreviews(t, s, f.Plan, 64)
			var saved manage.Plan
			if e = s.store.read(id, "plan", &saved); e != nil {
				t.Fatal("deleted plan before detecting damaged auxiliary state", e)
			}
			var operation Operation
			if e = s.store.read(id, "operation", &operation); e != nil {
				t.Fatal("deleted result before detecting damaged auxiliary state", e)
			}
		})
	}
}
