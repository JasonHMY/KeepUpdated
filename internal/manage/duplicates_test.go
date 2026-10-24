package manage

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDuplicatePreservesNonReadyReason(t *testing.T) {
	for _, scenario := range []string{"blocked", "manual"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			item := f.npm("a", "example")
			item.ID = "z"
			f.r.Items = append(f.r.Items, item)
			f.o.ResolvedTargets[item.ID] = f.o.ResolvedTargets["a"]
			if scenario == "blocked" {
				f.r.Items[1].Dependencies = []audit.Dependency{{Name: "broken", Status: "error"}}
			} else {
				target := f.o.ResolvedTargets["z"]
				target.Version = ""
				f.o.ResolvedTargets["z"] = target
			}
			alone := f.plan(Selection{Action: "update", IDs: []string{"z"}}).Items[0]
			if alone.Status != scenario {
				t.Fatalf("fixture: %+v", alone)
			}
			both := f.plan(Selection{Action: "update", All: true})
			if both.Items[1].Status != alone.Status || both.Items[1].Reason != alone.Reason {
				t.Fatalf("lost original %s/%s: %+v", alone.Status, alone.Reason, both.Items[1])
			}
		})
	}
}

func duplicateFixture(t *testing.T) *fixture {
	f := setup(t)
	item := f.npm("a", "example")
	item.ID = "z"
	f.r.Items = append(f.r.Items, item)
	f.o.ResolvedTargets[item.ID] = f.o.ResolvedTargets["a"]
	return f
}

func TestDuplicateCanonicalSelectionAndEvidence(t *testing.T) {
	f := duplicateFixture(t)
	s := Selection{Action: "update", IDs: []string{"z", "a"}}
	p := f.plan(s)
	if p.Items[0].Status != "ready" || p.Items[1].Status != "skipped" || p.Items[1].CanonicalID != "a" {
		t.Fatal(p.Items)
	}
	f.r.Items[0], f.r.Items[1] = f.r.Items[1], f.r.Items[0]
	s.IDs = []string{"a", "z"}
	if next := f.plan(s); !reflect.DeepEqual(p, next) {
		t.Fatal("input order changed canonical")
	}
	if one := f.plan(Selection{Action: "update", IDs: []string{"z"}}); one.Items[0].Status != "ready" || one.Items[0].CanonicalID != "" {
		t.Fatal(one.Items)
	}
	target := f.o.ResolvedTargets["z"]
	target.Source = "different verified source"
	f.o.ResolvedTargets["z"] = target
	conflict := f.plan(s)
	if conflict.Items[1].Status != "blocked" || conflict.Items[1].Reason != "duplicate_installation_conflicting_evidence" || conflict.Items[1].CanonicalID != "" {
		t.Fatal(conflict.Items)
	}
}

func TestDuplicateCannotChooseNonReadyCanonical(t *testing.T) {
	f := duplicateFixture(t)
	f.r.Items[0].Dependencies = []audit.Dependency{{Name: "broken", Status: "error"}}
	p := f.plan(Selection{Action: "update", All: true})
	if p.Items[0].Status != "blocked" || p.Items[1].Status != "ready" || p.Items[1].CanonicalID != "" {
		t.Fatal(p.Items)
	}
}

func TestDuplicateConflictingHashedInventoryEvidence(t *testing.T) {
	f := duplicateFixture(t)
	f.r.Items[1].Architecture = []string{"different-architecture"}
	p := f.plan(Selection{Action: "update", All: true})
	if p.Items[1].Status != "blocked" || p.Items[1].Reason != "duplicate_installation_conflicting_evidence" || p.Items[1].CanonicalID != "" {
		t.Fatal(p.Items)
	}
}

func TestDuplicateDifferentRootsRemainIndependent(t *testing.T) {
	f := duplicateFixture(t)
	item := &f.r.Items[1]
	scope := filepath.Join(f.home, "other")
	item.Path = filepath.Join(scope, "lib/node_modules/example")
	item.RealPath = item.Path
	f.write(filepath.Join(item.Path, "package.json"), `{"name":"example","version":"1.0.0"}`, 0644)
	target := f.o.ResolvedTargets[item.ID]
	target.Scope = scope
	target.Node = f.nodePolicy(*item, scope, target.ManagerPath, "11.19.1")
	f.o.ResolvedTargets[item.ID] = target
	p := f.plan(Selection{Action: "update", All: true})
	for _, pi := range p.Items {
		if pi.Status != "ready" || pi.CanonicalID != "" {
			t.Fatal(p.Items)
		}
	}
}

func TestDuplicateApplyLinksActualOutcomeOnce(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "unknown", "not-executed"} {
		t.Run(outcome, func(t *testing.T) {
			f := duplicateFixture(t)
			p := f.plan(Selection{Action: "update", All: true})
			calls := 0
			e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
				calls++
				if outcome == "failure" {
					return nil, errors.New("controlled failure")
				}
				if outcome == "success" {
					for n := range f.r.Items {
						f.r.Items[n].Version = "1.1.0"
					}
					f.write(filepath.Join(f.r.Items[0].Path, "package.json"), `{"name":"example","version":"1.1.0"}`, 0644)
				}
				return nil, nil
			}))
			snapshots := 0
			original := e.Snapshot
			e.Snapshot = func(ctx context.Context) (audit.Report, error) {
				snapshots++
				if outcome == "not-executed" && snapshots > 1 {
					return audit.Report{}, errors.New("preflight failed")
				}
				return original(ctx)
			}
			out, err := Apply(context.Background(), p, p.PlanID, e)
			wantCalls := 1
			if outcome == "not-executed" {
				wantCalls = 0
			}
			if err != nil || calls != wantCalls || len(out.Results) != 2 {
				t.Fatal(out, err, calls)
			}
			canonical, ref := out.Results[0], out.Results[1]
			wantStatus := map[string]string{"success": "updated", "failure": "failed", "unknown": "verification_unknown", "not-executed": "blocked"}[outcome]
			wantReason := "duplicate_installation_target"
			if outcome == "not-executed" {
				wantReason = "duplicate_canonical_not_executed"
			} else if outcome != "success" {
				wantReason = "duplicate_canonical_not_verified"
			}
			if canonical.Status != wantStatus || ref.CanonicalID != canonical.ID || ref.Reason != wantReason || ref.Status != "skipped" || ref.Executed || ref.Changed || ref.CommandSucceeded || ref.AfterVersion != "" {
				t.Fatal(out)
			}
		})
	}
}

func TestDuplicateTamperAndDriftRejected(t *testing.T) {
	for _, scenario := range []string{"link", "removed-link", "missing-canonical", "not-ready", "source", "target", "drift"} {
		t.Run(scenario, func(t *testing.T) {
			f := duplicateFixture(t)
			p := f.plan(Selection{Action: "update", All: true})
			switch scenario {
			case "link":
				p.Items[1].CanonicalID = "z"
			case "removed-link":
				p.Items[1].CanonicalID = ""
			case "missing-canonical":
				p.Items = p.Items[1:]
			case "not-ready":
				p.Items[0].Status = "blocked"
			case "source":
				p.Items[1].Binding.Source = "other"
			case "target":
				p.Items[1].TargetVersion = "9.0.0"
			case "drift":
				target := f.o.ResolvedTargets["z"]
				target.Source = "changed source"
				f.o.ResolvedTargets["z"] = target
			}
			p.PlanID = planHash(p)
			calls := 0
			e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) { calls++; return nil, nil }))
			if _, err := Apply(context.Background(), p, p.PlanID, e); err == nil || calls != 0 {
				t.Fatal("tampered plan accepted", err, calls)
			}
		})
	}
}

func TestDuplicateOptionalFieldLegacyHash(t *testing.T) {
	f := setup(t)
	f.npm("one", "example")
	p := f.plan(Selection{Action: "update", All: true})
	data, err := json.Marshal(p)
	if err != nil || strings.Contains(string(data), "canonical_id") {
		t.Fatal(string(data), err)
	}
	var decoded Plan
	if err = json.Unmarshal(data, &decoded); err != nil || planHash(decoded) != p.PlanID {
		t.Fatal("legacy hash changed", err)
	}
	legacyResult := `{"schema":"keepupdated/apply/v1","results":[{"id":"one","status":"skipped","reason":"duplicate_installation_target"}]}`
	var result ApplyResult
	if err = json.Unmarshal([]byte(legacyResult), &result); err != nil || result.Results[0].CanonicalID != "" {
		t.Fatal(result, err)
	}
}
