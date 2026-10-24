//go:build darwin

package manage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

// These tests launch only a freshly compiled fixture in t.TempDir. They never target installed apps.
func TestIsolatedNativeApplicationLifecycle(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "fixture")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/xcrun", "swiftc", "-module-cache-path", filepath.Join(root, "swift-cache"), "testdata/LifecycleFixture.swift", "-o", binary)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture compilation failed: %v\n%s", err, output)
	}
	executable, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"allow", "cancel", "unsaved", "timeout", "cancel_context"} {
		t.Run(mode, func(t *testing.T) {
			app, control := launchLifecycleFixture(t, executable, mode)
			target, err := ResolveApplicationLifecycle(context.Background(), app, Options{AppRoots: []string{filepath.Dir(app.Path)}}, ExecRunner{})
			if err != nil {
				t.Fatal(err)
			}
			work, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel_context" {
				if err := os.WriteFile(filepath.Join(control, "mode"), []byte("timeout"), 0600); err != nil {
					t.Fatal(err)
				}
				done := make(chan struct{})
				defer close(done)
				go func() {
					ticker := time.NewTicker(20 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-done:
							return
						case <-ticker.C:
							if _, err := os.Stat(filepath.Join(control, "requested")); err == nil {
								cancel()
								return
							}
						}
					}
				}()
			}
			results, err := QuitApplications(work, []ApplicationLifecycleTarget{target}, ExecRunner{}, 1500*time.Millisecond)
			if mode == "allow" {
				if err != nil || results[0].QuitStatus != "exited" {
					t.Fatal(results, err)
				}
				// Same installed path/identity after a synthetic version replacement; native launch is real.
				target.Reopen = true
				results[0].ReopenStatus = "skipped"
				app.Owner, app.Health, app.Related = "homebrew", audit.Result{Status: "ok"}, []string{"fixture-cask"}
				item := PlanItem{Binding: Binding{TargetID: "fixture-cask"}, ApplicationLifecycle: []ApplicationLifecycleTarget{target}}
				if err := os.Remove(filepath.Join(control, "stopped")); err != nil {
					t.Fatal(err)
				}
				reopenApplications(context.Background(), item, audit.Report{Items: []audit.Item{app}}, ExecRunner{}, results)
				if results[0].ReopenStatus != "reopened" {
					t.Fatal(results)
				}
				if err := VerifyApplicationsClosed(context.Background(), []ApplicationLifecycleTarget{target}, ExecRunner{}); err == nil {
					t.Fatal("reopened instance treated as closed")
				}
			} else {
				if err == nil || results[0].QuitStatus == "exited" {
					t.Fatal("refusal/cancellation treated as exit", results, err)
				}
				live, inspectErr := inspectApplication(context.Background(), ExecRunner{}, app.Path)
				if inspectErr != nil || len(live) != 1 {
					t.Fatal("test app no longer running", live, inspectErr)
				}
				if mode == "unsaved" {
					if _, err := os.Stat(filepath.Join(control, "unsaved-refused")); err != nil {
						t.Fatal("edited document did not reject normal termination", err)
					}
				}
				if mode == "cancel_context" && !strings.Contains(err.Error(), "cancel") && results[0].QuitStatus != "verification_unknown" {
					t.Fatal(results, err)
				}
			}
		})
	}
	t.Run("cask_apply", func(t *testing.T) {
		app, _ := launchLifecycleFixture(t, executable, "allow")
		f := setup(t)
		cask, _ := f.cask()
		app.Owner, app.Related, app.Health = "homebrew", []string{cask.ID}, audit.Result{Status: "ok"}
		f.r.Items[1] = app
		f.o.AppRoots = []string{filepath.Dir(app.Path)}
		manager := filepath.Join(f.o.BrewRoots[0], "bin/brew")
		candidate := ResolvedTarget{Provider: "homebrew", ManagerPath: manager, Scope: f.o.BrewRoots[0], Version: cask.Latest, OriginVerified: true, Source: "controlled cask candidate"}
		updated, calls := false, 0
		runner := runFunc(func(ctx context.Context, command Command) ([]byte, error) {
			if command.Path != manager {
				return (ExecRunner{}).Run(ctx, command)
			}
			calls++
			if !applicationProcessesClosed(ctx, ExecRunner{}, app.Path) {
				t.Fatal("cask manager ran before native app exited")
			}
			// Only the manager is controlled. Native quit, process inspection and reopen are real.
			output, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-replace", "CFBundleShortVersionString", "-string", cask.Latest, filepath.Join(app.Path, "Contents/Info.plist")).CombinedOutput()
			updated = err == nil
			return output, err
		})
		snapshot := func(ctx context.Context) (audit.Report, error) {
			report := f.r
			report.Items = append([]audit.Item(nil), report.Items...)
			live, err := inspectApplication(ctx, ExecRunner{}, app.Path)
			if err != nil {
				return report, err
			}
			report.Items[1].Active = len(live) > 0
			if updated {
				report.Items[0].Version = cask.Latest
				report.Items[1].Version = cask.Latest
			}
			return report, nil
		}
		resolve := func(ctx context.Context, report audit.Report, selection Selection) (map[string]ResolvedTarget, error) {
			targets, err := ResolveApplicationTargets(ctx, report, selection, f.o, runner)
			targets[cask.ID] = candidate
			return targets, err
		}
		selection := Selection{Action: "update", IDs: []string{cask.ID}, QuitApps: []string{app.ID}, ReopenApps: []string{app.ID}}
		report, err := snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		o := f.o
		o.ResolvedTargets, err = resolve(context.Background(), report, selection)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := NewPlan(report, selection, o)
		if err != nil || plan.Items[0].Status != "ready" {
			t.Fatal(plan, err)
		}
		e := f.engine(runner)
		e.Snapshot, e.ResolveTargets = snapshot, resolve
		out, err := Apply(context.Background(), plan, plan.PlanID, e)
		if err != nil || len(out.Results) != 1 || out.Results[0].Status != "updated" || calls != 1 {
			t.Fatal(out, err, calls)
		}
		result := out.Results[0].ApplicationLifecycle
		if len(result) != 1 || result[0].QuitStatus != "exited" || result[0].ReopenStatus != "reopened" {
			t.Fatal(result)
		}
	})
	t.Run("same_identity_other_path", func(t *testing.T) {
		first, _ := launchLifecycleFixture(t, executable, "allow")
		other, _ := launchLifecycleFixture(t, executable, "allow")
		target, err := ResolveApplicationLifecycle(context.Background(), first, Options{AppRoots: []string{filepath.Dir(first.Path)}}, ExecRunner{})
		if err != nil {
			t.Fatal(err)
		}
		results, err := QuitApplications(context.Background(), []ApplicationLifecycleTarget{target}, ExecRunner{}, 3*time.Second)
		if err != nil || results[0].QuitStatus != "exited" {
			t.Fatal(results, err)
		}
		live, err := inspectApplication(context.Background(), ExecRunner{}, other.Path)
		if err != nil || len(live) != 1 {
			t.Fatal("same-name app in other path was affected", live, err)
		}
		first.Owner, first.Health, first.Related = "homebrew", audit.Result{Status: "ok"}, []string{"fixture-cask"}
		target.Reopen = true
		reopenApplications(context.Background(), PlanItem{Binding: Binding{TargetID: "fixture-cask"}, ApplicationLifecycle: []ApplicationLifecycleTarget{target}}, audit.Report{Items: []audit.Item{first}}, ExecRunner{}, results)
		if results[0].ReopenStatus != "reopened" {
			t.Fatal("exact path did not reopen alongside other copy", results)
		}
		live, err = inspectApplication(context.Background(), ExecRunner{}, other.Path)
		if err != nil || len(live) != 1 {
			t.Fatal("other copy changed during reopen", live, err)
		}
	})
}

func launchLifecycleFixture(t *testing.T, executable []byte, mode string) (audit.Item, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "Lifecycle Fixture.app")
	control := filepath.Join(root, "control")
	for _, dir := range []string{filepath.Join(path, "Contents/MacOS"), control} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	plist := fmt.Sprintf(`<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.example.KeepUpdatedLifecycleFixture</string><key>CFBundleName</key><string>Lifecycle Fixture</string><key>CFBundleExecutable</key><string>Fixture</string><key>CFBundlePackageType</key><string>APPL</string><key>CFBundleShortVersionString</key><string>1.0</string><key>KeepUpdatedFixtureControl</key><string>%s</string><key>LSUIElement</key><true/></dict></plist>`, control)
	if err := os.WriteFile(filepath.Join(path, "Contents/Info.plist"), []byte(plist), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "Contents/MacOS/Fixture"), executable, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(control, "mode"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
	// Cleanup asks the fixture itself to end through NSApplication; it never kills a PID.
	t.Cleanup(func() {
		if err := os.WriteFile(filepath.Join(control, "stop"), nil, 0600); err != nil {
			t.Error(err)
			return
		}
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			live, err := inspectApplication(context.Background(), ExecRunner{}, path)
			if err == nil && len(live) == 0 && applicationProcessesClosed(context.Background(), ExecRunner{}, path) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Error("isolated fixture did not shut down through its control file")
	})
	if output, err := exec.Command("/usr/bin/open", "-n", "-a", path).CombinedOutput(); err != nil {
		t.Fatalf("fixture launch failed: %v %s", err, output)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(control, "ready")); err == nil {
			return audit.Item{ID: "fixture-app", Kind: "application", Path: path, Identity: "com.example.KeepUpdatedLifecycleFixture", Version: "1.0", Owner: "standalone", ActivityKnown: true, Active: true}, control
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("isolated fixture did not start")
	return audit.Item{}, control
}
