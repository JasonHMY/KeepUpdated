package manage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func TestLifecycleSelectionRequiresExactApplicationConsent(t *testing.T) {
	for _, s := range []Selection{
		{Action: "update", All: true, QuitApps: []string{"app"}},
		{Action: "uninstall", IDs: []string{"cask"}, QuitApps: []string{"app"}},
		{Action: "update", IDs: []string{"cask"}, ReopenApps: []string{"app"}},
		{Action: "update", IDs: []string{"cask"}, QuitApps: []string{"app", "app"}},
		{Action: "update", IDs: []string{"cask"}, QuitApps: []string{""}},
	} {
		if ValidateLifecycleSelection(s) == nil {
			t.Fatal("accepted invalid consent", s)
		}
	}
	f := setup(t)
	i, _ := f.cask()
	if _, err := NewPlan(f.r, Selection{Action: "update", IDs: []string{i.ID}, QuitApps: []string{"other"}}, f.o); err == nil {
		t.Fatal("unrelated exit accepted")
	}
}

type lifecycleFixture struct {
	f                     *fixture
	cask, app             audit.Item
	running               []ApplicationInstance
	mode                  string
	quits, updates, opens int
	cancel                context.CancelFunc
	afterExit             func()
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	f := setup(t)
	cask, app := f.cask()
	app.Health = audit.Result{Status: "ok"}
	f.r.Items[1] = app
	f.o.ResolvedTargets[cask.ID] = ResolvedTarget{Provider: "homebrew", ManagerPath: filepath.Join(f.o.BrewRoots[0], "bin/brew"), Scope: f.o.BrewRoots[0], OriginVerified: true, Source: "fixture approved cask", Version: cask.Latest}
	return &lifecycleFixture{f: f, cask: cask, app: app, running: []ApplicationInstance{{PID: 1234, LaunchedAt: "1234.5", Path: app.Path, Executable: filepath.Join(app.Path, "Contents/MacOS/Example"), Identity: app.Identity}}}
}

func (f *lifecycleFixture) Run(ctx context.Context, command Command) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	encode := func(v any) ([]byte, error) { return json.Marshal(v) }
	switch command.Path {
	case "/bin/ps":
		data := "/usr/bin/unrelated\n"
		for _, instance := range f.running {
			data += instance.Executable + "\n"
		}
		if f.mode == "helper" && f.quits > 0 {
			data += f.app.Path + "/Contents/Helpers/Worker\n"
		}
		return []byte(data), nil
	case "/usr/bin/osascript":
		var input struct {
			Operation, Path, Identity string
			Instance                  ApplicationInstance
		}
		if json.Unmarshal([]byte(command.Args[len(command.Args)-1]), &input) != nil {
			f.f.t.Fatal("invalid native input")
		}
		if input.Path != f.app.Path {
			f.f.t.Fatal("wrong application path", input.Path)
		}
		switch input.Operation {
		case "inspect":
			live := append([]ApplicationInstance{}, f.running...)
			return encode(map[string]any{"instances": live})
		case "quit":
			f.quits++
			if len(f.running) == 0 || input.Instance != f.running[0] {
				f.f.t.Fatal("wrong instance requested", input.Instance)
			}
			if f.mode == "refused" {
				return encode(map[string]string{"status": "refused"})
			}
			if f.mode == "native_race" {
				return encode(map[string]string{"status": "instance_changed"})
			}
			if f.mode == "request_error" {
				return nil, errors.New("request unavailable")
			}
			if f.mode == "timeout" || f.mode == "unsaved" {
				return encode(map[string]string{"status": "requested"})
			}
			if f.mode == "restart" {
				f.running[0].LaunchedAt = "new-launch"
			} else {
				f.running = nil
			}
			if f.mode == "cancel" {
				f.cancel()
			}
			if f.afterExit != nil {
				f.afterExit()
			}
			return encode(map[string]string{"status": "requested"})
		}
	case "/usr/bin/plutil":
		return encode(map[string]string{"CFBundleIdentifier": f.app.Identity, "CFBundleShortVersionString": f.f.r.Items[1].Version})
	case "/usr/bin/open":
		f.opens++
		if !reflect.DeepEqual(command.Args, []string{"-n", "-a", f.app.Path}) {
			f.f.t.Fatal("reopen not exact", command.Args)
		}
		if f.mode == "reopen_failed" {
			return nil, errors.New("launch failed")
		}
		if f.mode != "reopen_unknown" {
			f.running = []ApplicationInstance{{PID: 2345, LaunchedAt: "2345.6", Path: f.app.Path, Executable: filepath.Join(f.app.Path, "Contents/MacOS/Example"), Identity: f.app.Identity}}
		}
		return nil, nil
	default:
		if command.Path == filepath.Join(f.f.o.BrewRoots[0], "bin/brew") {
			if len(f.running) > 0 {
				f.f.t.Fatal("manager ran while target active")
			}
			if !reflect.DeepEqual(command.Args, []string{"upgrade", "--cask", "--no-quit", "example"}) {
				f.f.t.Fatal("manager allowed implicit quit", command.Args)
			}
			f.updates++
			if f.mode == "update_failed" {
				return nil, errors.New("controlled manager failure")
			}
			if f.mode == "update_unverified" {
				return nil, nil
			}
			f.f.r.Items[0].Version = f.cask.Latest
			f.f.r.Items[1].Version = f.cask.Latest
			f.f.write(filepath.Join(f.app.Path, "Contents/Info.plist"), "updated fixture", 0644)
			if f.mode == "wrong_post_identity" {
				f.f.r.Items[1].Identity = "com.example.Other"
			}
			if f.mode == "failed_but_changed" {
				return nil, errors.New("manager failed after change")
			}
			return nil, nil
		}
	}
	f.f.t.Fatal("unexpected command", command.Path)
	return nil, errors.New("unexpected command")
}

func (f *lifecycleFixture) snapshot(context.Context) (audit.Report, error) {
	report := f.f.r
	report.Items = append([]audit.Item(nil), report.Items...)
	report.Items[1].Active = len(f.running) > 0
	report.Items[1].ActivityKnown = true
	return report, nil
}

func (f *lifecycleFixture) resolve(ctx context.Context, report audit.Report, selection Selection) (map[string]ResolvedTarget, error) {
	out, err := ResolveApplicationTargets(ctx, report, selection, f.f.o, f)
	out[f.cask.ID] = f.f.o.ResolvedTargets[f.cask.ID]
	return out, err
}

func (f *lifecycleFixture) plan(reopen bool) Plan {
	s := Selection{Action: "update", IDs: []string{f.cask.ID}, QuitApps: []string{f.app.ID}}
	if reopen {
		s.ReopenApps = []string{f.app.ID}
	}
	report, _ := f.snapshot(context.Background())
	o := f.f.o
	var err error
	o.ResolvedTargets, err = f.resolve(context.Background(), report, s)
	if err != nil {
		f.f.t.Fatal(err)
	}
	plan, err := NewPlan(report, s, o)
	if err != nil || plan.Items[0].Status != "ready" {
		f.f.t.Fatal(plan, err)
	}
	if f.quits != 0 || f.updates != 0 || f.opens != 0 {
		f.f.t.Fatal("preview mutated applications")
	}
	if _, err := commandFor(plan.Items[0], "update", o, f.f.state); err == nil {
		f.f.t.Fatal("undischarged quit guard bypassed")
	}
	return plan
}

func (f *lifecycleFixture) engine() Engine {
	e := f.f.engine(f)
	e.Snapshot, e.ResolveTargets, e.LifecycleTimeout = f.snapshot, f.resolve, 30*time.Millisecond
	return e
}

func TestCaskLifecycleApplyAndSeparateReopenOutcome(t *testing.T) {
	cases := []struct {
		mode, status, quit, reopen string
		updates, opens             int
	}{
		{"success", "updated", "exited", "reopened", 1, 1},
		{"refused", "blocked", "refused", "skipped", 0, 0},
		{"native_race", "blocked", "refused", "skipped", 0, 0},
		{"request_error", "blocked", "verification_unknown", "skipped", 0, 0},
		{"timeout", "blocked", "requested", "skipped", 0, 0},
		{"unsaved", "blocked", "requested", "skipped", 0, 0},
		{"helper", "blocked", "requested", "skipped", 0, 0},
		{"restart", "blocked", "requested", "skipped", 0, 0},
		{"cancel", "blocked", "requested", "skipped", 0, 0},
		{"update_failed", "failed", "exited", "skipped", 1, 0},
		{"update_unverified", "verification_unknown", "exited", "skipped", 1, 0},
		{"failed_but_changed", "updated", "exited", "skipped", 1, 0},
		{"reopen_failed", "updated", "exited", "failed", 1, 1},
		{"reopen_unknown", "updated", "exited", "verification_unknown", 1, 1},
		{"wrong_post_identity", "verification_unknown", "exited", "skipped", 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			f := newLifecycleFixture(t)
			plan := f.plan(true)
			f.mode = tc.mode
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.cancel = cancel
			out, err := Apply(ctx, plan, plan.PlanID, f.engine())
			if err != nil || len(out.Results) != 1 {
				t.Fatal(out, err)
			}
			result := out.Results[0]
			if result.Status != tc.status || len(result.ApplicationLifecycle) != 1 || f.updates != tc.updates || f.opens != tc.opens {
				t.Fatal(out, f.updates, f.opens)
			}
			lifecycle := result.ApplicationLifecycle[0]
			if lifecycle.QuitStatus != tc.quit || lifecycle.ReopenStatus != tc.reopen {
				t.Fatal(lifecycle)
			}
			if f.updates == 0 && result.Executed {
				t.Fatal("quit confused with update execution")
			}
		})
	}
}

func TestCaskLifecycleDoesNotReopenWithoutSelection(t *testing.T) {
	f := newLifecycleFixture(t)
	plan := f.plan(false)
	out, err := Apply(context.Background(), plan, plan.PlanID, f.engine())
	if err != nil || out.Results[0].Status != "updated" || f.opens != 0 || out.Results[0].ApplicationLifecycle[0].ReopenStatus != "not_requested" {
		t.Fatal(out, err)
	}
}

func TestLifecycleRejectsStaleContentInstancePathAndConsent(t *testing.T) {
	for _, mode := range []string{"content", "same_pid_new_launch", "new_pid", "symlink", "consent", "candidate"} {
		t.Run(mode, func(t *testing.T) {
			f := newLifecycleFixture(t)
			plan := f.plan(true)
			switch mode {
			case "content":
				f.f.write(filepath.Join(f.app.Path, "Contents/new-file"), "changed", 0644)
			case "same_pid_new_launch":
				f.running[0].LaunchedAt = "later"
			case "new_pid":
				f.running[0].PID++
			case "symlink":
				moved := f.app.Path + "-other"
				if err := os.Rename(f.app.Path, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, f.app.Path); err != nil {
					t.Fatal(err)
				}
			case "consent":
				plan.Selection.ReopenApps = nil
			case "candidate":
				target := f.f.o.ResolvedTargets[f.cask.ID]
				target.Version = "1.2.0"
				f.f.o.ResolvedTargets[f.cask.ID] = target
			}
			if _, err := Apply(context.Background(), plan, plan.PlanID, f.engine()); err == nil || f.quits+f.updates+f.opens != 0 {
				t.Fatal("stale approval executed", err)
			}
		})
	}
}

func TestLifecycleRevalidatesAfterExitWithoutMaskingOtherChanges(t *testing.T) {
	for _, mode := range []string{"content", "source", "candidate", "manager", "late_restart"} {
		t.Run(mode, func(t *testing.T) {
			f := newLifecycleFixture(t)
			plan := f.plan(true)
			f.afterExit = func() {
				switch mode {
				case "content":
					f.f.write(filepath.Join(f.app.Path, "Contents/changed"), "changed", 0644)
				case "source":
					f.f.r.Items[0].Source = "other/tap"
				case "candidate":
					target := f.f.o.ResolvedTargets[f.cask.ID]
					target.Version = "1.2.0"
					f.f.o.ResolvedTargets[f.cask.ID] = target
				case "manager":
					f.f.write(filepath.Join(f.f.o.BrewRoots[0], "bin/brew"), "different manager", 0755)
				}
			}
			e := f.engine()
			if mode == "late_restart" {
				e.ResolveTargets = func(ctx context.Context, report audit.Report, selection Selection) (map[string]ResolvedTarget, error) {
					targets, err := f.resolve(ctx, report, selection)
					if len(selection.QuitApps) == 0 {
						f.running = []ApplicationInstance{{PID: 3333, LaunchedAt: "later", Path: f.app.Path, Identity: f.app.Identity, Executable: filepath.Join(f.app.Path, "Contents/MacOS/Example")}}
					}
					return targets, err
				}
			}
			out, err := Apply(context.Background(), plan, plan.PlanID, e)
			if err != nil || out.Results[0].Status != "blocked" || f.updates+f.opens != 0 || f.quits != 1 {
				t.Fatal(out, err, f.quits, f.updates)
			}
		})
	}
}

func TestApplicationProofMergesWithoutErasingCandidate(t *testing.T) {
	f := newLifecycleFixture(t)
	candidate := ResolvedTarget{Provider: "native-app", Version: "2.0", Source: "verified vendor feed", Scope: f.app.Path, OriginVerified: true, Reason: "candidate_verified"}
	f.f.o.ResolvedTargets[f.app.ID] = candidate
	report, _ := f.snapshot(context.Background())
	targets, err := f.resolve(context.Background(), report, Selection{Action: "update", IDs: []string{f.cask.ID}, QuitApps: []string{f.app.ID}})
	result := targets[f.app.ID]
	if err != nil || result.Version != candidate.Version || result.Source != candidate.Source || result.Reason != candidate.Reason || result.ApplicationLifecycle == nil {
		t.Fatal(result, err)
	}
	f.running = nil
	targets, err = f.resolve(context.Background(), report, Selection{Action: "update", IDs: []string{f.cask.ID}})
	result = targets[f.app.ID]
	if err != nil || result.Version != candidate.Version || result.ApplicationLifecycle != nil || !applicationClosed(result) {
		t.Fatal(result, err)
	}
}

func TestLifecycleScriptNeverTargetsByNameOrForcesExit(t *testing.T) {
	for _, forbidden := range []string{"forceTerminate", "Application(", "killall", "pkill"} {
		if strings.Contains(lifecycleScript, forbidden) {
			t.Fatal("unsafe native bridge", forbidden)
		}
	}
}
