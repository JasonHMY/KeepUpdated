package manage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func TestNativeUpdateWithApprovedLifecycle(t *testing.T) {
	for _, scenario := range []string{"success", "refused", "candidate-drift", "restart-during-download", "reopen-failure"} {
		t.Run(scenario, func(t *testing.T) {
			n := nativeSetup(t)
			n.running = true
			base := n.runner
			quits, downloads, opens := 0, 0, 0
			instance := ApplicationInstance{PID: 4321, LaunchedAt: "1000.5", Path: n.i.Path, Executable: filepath.Join(n.i.Path, "Contents/MacOS/Native"), Identity: n.i.Identity}
			n.runner = runFunc(func(ctx context.Context, c Command) ([]byte, error) {
				if c.Path == "/usr/bin/osascript" {
					var input struct {
						Operation, Path string
						Instance        ApplicationInstance
					}
					if json.Unmarshal([]byte(c.Args[len(c.Args)-1]), &input) != nil || input.Path != n.i.Path {
						t.Fatal("wrong lifecycle target")
					}
					if input.Operation == "inspect" {
						instances := []ApplicationInstance{}
						if n.running {
							instances = append(instances, instance)
						}
						return json.Marshal(map[string]any{"instances": instances})
					}
					if input.Operation != "quit" || input.Instance != instance || !n.running {
						t.Fatal("unexpected quit request")
					}
					quits++
					if scenario == "refused" {
						return []byte(`{"status":"refused"}`), nil
					}
					n.running = false
					if scenario == "candidate-drift" {
						n.feed = strings.Replace(n.feed, "Native.zip", "Other.zip", 1)
					}
					return []byte(`{"status":"requested"}`), nil
				}
				if c.Path == "/usr/bin/open" {
					if len(c.Args) != 3 || c.Args[0] != "-n" || c.Args[1] != "-a" || c.Args[2] != n.i.Path {
						t.Fatal("wrong reopen target", c)
					}
					opens++
					if scenario == "reopen-failure" {
						return nil, errors.New("controlled reopen failure")
					}
					n.running = true
					instance.PID++
					instance.LaunchedAt = "2000.5"
					return nil, nil
				}
				return base.Run(ctx, c)
			})
			n.download = func() {
				if n.running || quits != 1 {
					t.Fatal("download began before the approved exit")
				}
				downloads++
				if scenario == "restart-during-download" {
					n.running = true
				}
			}
			snapshot := func(ctx context.Context) (audit.Report, error) {
				r, err := n.report(ctx)
				r.Items[0].Active = n.running
				return r, err
			}
			selection := Selection{Action: "update", IDs: []string{n.i.ID}, QuitApps: []string{n.i.ID}, ReopenApps: []string{n.i.ID}}
			report, err := snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			n.f.o.ResolvedTargets, err = n.resolver(context.Background(), report, selection)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := NewPlan(report, selection, n.f.o)
			if err != nil || len(plan.Items) != 1 || plan.Items[0].Status != "ready" || !plan.Items[0].RequiresQuit || len(plan.Items[0].ApplicationLifecycle) != 1 || plan.Items[0].Binding.ResolvedTarget.ApplicationUpdate == nil {
				t.Fatalf("candidate and lifecycle did not compose: %#v, %v", plan, err)
			}
			originalPlan, _ := json.Marshal(plan)
			result, err := Apply(context.Background(), plan, plan.PlanID, Engine{Snapshot: snapshot, ResolveTargets: n.resolver, Runner: n.runner, Options: n.f.o, StateDir: n.f.state, Now: func() time.Time { return n.f.o.Now }})
			if err != nil || len(result.Results) != 1 {
				t.Fatalf("%#v, %v", result, err)
			}
			afterPlan, _ := json.Marshal(plan)
			if string(originalPlan) != string(afterPlan) {
				t.Fatal("post-quit validation mutated the approved plan")
			}
			got := result.Results[0]
			if quits != 1 || len(got.ApplicationLifecycle) != 1 {
				t.Fatal("exit result missing", got)
			}
			if scenario == "success" || scenario == "reopen-failure" {
				if got.Status != "updated" || !got.Changed || got.AfterVersion != "2.0" || downloads != 1 || opens != 1 {
					t.Fatalf("update/reopen did not compose: %#v, downloads=%d, opens=%d", got, downloads, opens)
				}
				expected := "reopened"
				if scenario == "reopen-failure" {
					expected = "failed"
				}
				if got.ApplicationLifecycle[0].ReopenStatus != expected || got.ApplicationLifecycle[0].QuitStatus != "exited" {
					t.Fatal("independent lifecycle results lost", got)
				}
			} else {
				if got.Status == "updated" || got.Changed || opens != 0 || scenario != "restart-during-download" && downloads != 0 {
					t.Fatal("unsafe update executed", got, downloads, opens)
				}
				info, err := readApplicationInfo(context.Background(), n.runner, n.i.Path)
				if err != nil || info.Version != "1.0" {
					t.Fatal("original application changed", info, err)
				}
			}
		})
	}
}

func TestNativeRunningAppStillRequiresExactExitChoice(t *testing.T) {
	n := nativeSetup(t)
	n.running, n.i.Active = true, true
	n.f.r.Items[0] = n.i
	for _, special := range []bool{false, true} {
		selection := Selection{Action: "update", IDs: []string{n.i.ID}, Special: special}
		targets, err := n.resolver(context.Background(), n.f.r, selection)
		if err != nil {
			t.Fatal(err)
		}
		n.f.o.ResolvedTargets = targets
		plan, err := NewPlan(n.f.r, selection, n.f.o)
		if err != nil || plan.Items[0].Status == "ready" || !plan.Items[0].RequiresQuit || len(plan.Items[0].Applications) != 1 || plan.Items[0].Applications[0].ID != n.i.ID {
			t.Fatalf("missing exact exit choice accepted: %#v, %v", plan, err)
		}
	}
}
