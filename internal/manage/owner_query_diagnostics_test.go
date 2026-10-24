package manage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

var ownerQueryFailures = []struct {
	name, reason string
	err          error
}{
	{"timeout", "owner_query_timeout", context.DeadlineExceeded},
	{"permission", "owner_permission_denied", os.ErrPermission},
	{"network", "owner_network_unavailable", CommandFailure{Code: "owner_network_unavailable"}},
	{"unknown", "owner_resolution_failed", errors.New("/Users/private SECRET https://user:secret@host?token=SECRET")},
	{"invalid-code", "owner_resolution_failed", CommandFailure{Code: "SECRET /Users/private"}},
}

func assertUnresolvedQuery(t *testing.T, target ResolvedTarget, reason string) {
	t.Helper()
	if target.ResolutionError != reason || target.Reason != reason || target.OriginVerified || target.Version != "" || target.FromVersion != "" || target.Source != "" || target.ApplicationUpdate != nil || target.ApplicationLifecycle != nil || target.ApplicationState != "" {
		t.Fatalf("failed query retained a candidate: %+v", target)
	}
	data, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"SECRET", "/Users/private", "user:secret"} {
		if strings.Contains(string(data), private) {
			t.Fatal("raw query error leaked")
		}
	}
}

func assertQueryPlan(t *testing.T, plan Plan, id, reason string) {
	t.Helper()
	for _, item := range plan.Items {
		if item.ID != id {
			continue
		}
		if item.Reason != reason || item.Status != "manual" && item.Status != "blocked" || item.TargetVersion != "" || item.Binding.ResolvedTarget != nil || len(item.ApplicationLifecycle) != 0 {
			t.Fatalf("failed query plan: %+v", item)
		}
		return
	}
	t.Fatal("missing plan item", id)
}

func TestApplicationQueryFailurePreservesReasonAndOtherPackage(t *testing.T) {
	for _, tc := range ownerQueryFailures {
		for _, action := range []string{"update", "uninstall"} {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				f := setup(t)
				_, app := f.cask()
				app.Owner, app.Source, app.Active = "standalone", "sparkle", true
				f.r.Items = []audit.Item{app}
				other := f.npm("other", "other")
				f.o.ResolvedTargets[app.ID] = ResolvedTarget{Provider: "native-app", Scope: app.Path, OriginVerified: true, Version: "9", ApplicationUpdate: &ApplicationUpdatePreview{}, ApplicationLifecycle: &ApplicationLifecycleTarget{}, ApplicationState: "application_instances_verified"}
				selection := Selection{Action: action, IDs: []string{app.ID, other.ID}}
				targets, err := ResolveApplicationTargets(context.Background(), f.r, selection, f.o, runFunc(func(_ context.Context, c Command) ([]byte, error) {
					if c.Path != "/bin/ps" {
						t.Fatal("unexpected command", c.Path)
					}
					return []byte("SECRET"), tc.err
				}))
				if err != nil || len(targets) != 1 {
					t.Fatal(targets, err)
				}
				assertUnresolvedQuery(t, targets[app.ID], tc.reason)
				f.o.ResolvedTargets[app.ID] = targets[app.ID]
				plan := f.plan(selection)
				assertQueryPlan(t, plan, app.ID, tc.reason)
				for _, item := range plan.Items {
					if item.ID == other.ID && item.Status != "ready" {
						t.Fatal("unrelated package blocked", item)
					}
				}
				// A plan containing only the failed app must not quit, update or trash it.
				selection.IDs = []string{app.ID}
				plan = f.plan(selection)
				out, err := Apply(context.Background(), plan, plan.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
					t.Fatal("failed app dispatched a command")
					return nil, nil
				})))
				if err != nil {
					t.Fatal(err)
				}
				if len(out.Results) != 1 || out.Results[0].Executed {
					t.Fatal(out)
				}
				if _, err := os.Stat(app.Path); err != nil {
					t.Fatal("failed app was removed", err)
				}
			})
		}
	}
}

func TestCaskApplicationQueryFailurePrecedesQuitFallback(t *testing.T) {
	for _, quit := range []bool{false, true} {
		f := newLifecycleFixture(t)
		selection := Selection{Action: "update", IDs: []string{f.cask.ID}}
		if quit {
			selection.QuitApps = []string{f.app.ID}
		}
		f.f.r.Items[1].Active, f.f.r.Items[1].ActivityKnown = true, false
		targets, err := ResolveApplicationTargets(context.Background(), f.f.r, selection, f.f.o, runFunc(func(context.Context, Command) ([]byte, error) { return nil, os.ErrPermission }))
		if err != nil {
			t.Fatal(err)
		}
		f.f.o.ResolvedTargets[f.app.ID] = targets[f.app.ID]
		assertQueryPlan(t, f.f.plan(selection), f.cask.ID, "owner_permission_denied")
		selection.IDs = append(selection.IDs, f.app.ID)
		assertQueryPlan(t, f.f.plan(selection), f.app.ID, "owner_permission_denied")
	}
}

func TestApplicationQueryMembershipAndCancellation(t *testing.T) {
	for _, action := range []string{"update", "uninstall"} {
		for _, scenario := range []string{"unselected", "app-store", "system", "unknown", "kind", "action"} {
			if action == "uninstall" && scenario == "app-store" {
				continue // Store updates are manual; existing user-owned uninstall remains supported.
			}
			f := setup(t)
			_, app := f.cask()
			app.Owner, app.Source = "standalone", "sparkle"
			selection := Selection{Action: action, IDs: []string{app.ID}}
			switch scenario {
			case "unselected":
				selection.IDs = []string{"other"}
			case "kind":
				app.Kind = "formula"
			case "action":
				selection.Action = "check"
			default:
				app.Owner = scenario
			}
			targets, err := ResolveApplicationTargets(context.Background(), audit.Report{Items: []audit.Item{app}}, selection, f.o, runFunc(func(context.Context, Command) ([]byte, error) {
				t.Fatal("ineligible app queried", scenario, action)
				return nil, nil
			}))
			if err != nil || len(targets) != 0 {
				t.Fatal(targets, err)
			}
		}
	}
	f := setup(t)
	_, app := f.cask()
	for _, before := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if before {
			cancel()
		}
		calls := 0
		targets, err := ResolveApplicationTargets(ctx, f.r, Selection{Action: "update", IDs: []string{app.ID}}, f.o, runFunc(func(context.Context, Command) ([]byte, error) {
			calls++
			cancel()
			return nil, context.DeadlineExceeded
		}))
		cancel()
		if !errors.Is(err, context.Canceled) || len(targets) != 0 || before && calls != 0 || !before && calls != 1 {
			t.Fatal(targets, err, calls)
		}
	}
}

func TestApplicationFailureMergeCannotRestoreCandidate(t *testing.T) {
	candidate := ResolvedTarget{Provider: "native-app", Scope: "/fixture.app", OriginVerified: true, Version: "2", ApplicationUpdate: &ApplicationUpdatePreview{}, ApplicationLifecycle: &ApplicationLifecycleTarget{}, ApplicationState: "application_instances_verified"}
	failure := queryFailureTarget("native-app", candidate.Scope, os.ErrPermission)
	for _, target := range []ResolvedTarget{MergeApplicationTarget(candidate, failure), MergeApplicationTarget(failure, candidate)} {
		assertUnresolvedQuery(t, target, "owner_permission_denied")
	}
}

func addOtherGitSkill(t *testing.T, f *fixture, item audit.Item, root string) audit.Item {
	t.Helper()
	other := item
	other.ID, other.Name = "other-skill", "other-skill"
	otherRoot := filepath.Join(filepath.Dir(root), "other")
	other.Path = filepath.Join(otherRoot, "SKILL.md")
	for _, file := range []string{"SKILL.md", ".git/HEAD", ".git/config"} {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		f.write(filepath.Join(otherRoot, file), string(data), 0644)
	}
	f.r.Items = append(f.r.Items, other)
	return other
}

func successfulGitQuery(c Command) ([]byte, error) {
	if contains(c.Args, "rev-parse") {
		return []byte(strings.Repeat("a", 40)), nil
	}
	if contains(c.Args, "ls-remote") {
		return []byte(strings.Repeat("b", 40) + "\trefs/heads/main"), nil
	}
	return nil, nil
}

func TestGitQueryFailuresPreserveReasonAndOtherSkill(t *testing.T) {
	for _, tc := range ownerQueryFailures {
		for _, stage := range []string{"HEAD", "tracking", "status", "merge-base", "ls-remote"} {
			t.Run(tc.name+"/"+stage, func(t *testing.T) {
				f := setup(t)
				item, root := f.gitSkill()
				other := addOtherGitSkill(t, f, item, root)
				selection := Selection{Action: "update", All: true}
				runner := runFunc(func(_ context.Context, c Command) ([]byte, error) {
					if c.Dir == root && (stage == "HEAD" && contains(c.Args, "HEAD") || stage == "tracking" && contains(c.Args, "refs/remotes/origin/main") || contains(c.Args, stage)) {
						return []byte("SECRET"), tc.err
					}
					return successfulGitQuery(c)
				})
				targets, err := ResolveGitTargets(context.Background(), f.r, selection, f.o, runner)
				if err != nil || len(targets) != 2 {
					t.Fatal(targets, err)
				}
				assertUnresolvedQuery(t, targets[item.ID], tc.reason)
				if !targets[other.ID].OriginVerified {
					t.Fatal("unrelated skill unresolved", targets)
				}
				f.o.ResolvedTargets = targets
				plan := f.plan(selection)
				assertQueryPlan(t, plan, item.ID, tc.reason)
				for _, p := range plan.Items {
					if p.ID == other.ID && p.Status != "ready" {
						t.Fatal(p)
					}
				}
				selection.All, selection.IDs = false, []string{item.ID}
				plan = f.plan(selection)
				out, err := Apply(context.Background(), plan, plan.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
					t.Fatal("failed skill dispatched mutation")
					return nil, nil
				})))
				if err != nil || len(out.Results) != 1 || out.Results[0].Executed {
					t.Fatal(out, err)
				}
			})
		}
	}
}

func TestGitQueryPolicyAndCancellation(t *testing.T) {
	for _, scenario := range []string{"dirty", "non-ancestor", "no-tracking", "no-remote-branch", "filter", "unselected", "unknown-owner", "system", "kind", "source", "action"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			item, root := f.gitSkill()
			selection := Selection{Action: "update", All: true}
			allowed := true
			switch scenario {
			case "filter":
				f.write(filepath.Join(root, ".git/config"), "[filter \"custom\"]\n clean = execute\n", 0644)
				allowed = false
			case "unselected":
				selection.All = false
				selection.IDs = []string{"other"}
				allowed = false
			case "unknown-owner", "system":
				f.r.Items[0].Owner = scenario
				allowed = false
			case "kind":
				f.r.Items[0].Kind = "application"
				allowed = false
			case "source":
				f.r.Items[0].Source = "local"
				allowed = false
			case "action":
				selection.Action = "uninstall"
				allowed = false
			}
			targets, err := ResolveGitTargets(context.Background(), f.r, selection, f.o, runFunc(func(_ context.Context, c Command) ([]byte, error) {
				if !allowed {
					t.Fatal("ineligible skill queried", scenario)
				}
				switch {
				case scenario == "dirty" && contains(c.Args, "status"):
					return []byte(" M SKILL.md"), nil
				case scenario == "non-ancestor" && contains(c.Args, "merge-base"):
					return nil, CommandFailure{Code: "owner_query_failed", exitCode: 1}
				case scenario == "no-tracking" && contains(c.Args, "refs/remotes/origin/main"):
					return nil, CommandFailure{Code: "owner_query_failed", exitCode: 1}
				case scenario == "no-remote-branch" && contains(c.Args, "ls-remote"):
					return nil, CommandFailure{Code: "owner_query_failed", exitCode: 2}
				}
				return successfulGitQuery(c)
			}))
			if err != nil || len(targets) != 0 {
				t.Fatal(item.ID, targets, err)
			}
		})
	}
	for _, stage := range []string{"HEAD", "refs/remotes/origin/main", "status", "merge-base", "ls-remote"} {
		f := setup(t)
		item, root := f.gitSkill()
		addOtherGitSkill(t, f, item, root)
		ctx, cancel := context.WithCancel(context.Background())
		stopped := false
		targets, err := ResolveGitTargets(ctx, f.r, Selection{Action: "update", All: true}, f.o, runFunc(func(_ context.Context, c Command) ([]byte, error) {
			if stopped {
				t.Fatal("query dispatched after cancellation")
			}
			if contains(c.Args, stage) {
				stopped = true
				cancel()
				return nil, context.DeadlineExceeded
			}
			return successfulGitQuery(c)
		}))
		cancel()
		if !errors.Is(err, context.Canceled) || len(targets) != 0 {
			t.Fatal(targets, err)
		}
	}
}

func TestQueryFailureStillHonorsPathAndScopeGuards(t *testing.T) {
	f := setup(t)
	_, app := f.cask()
	app.Owner, app.Source = "standalone", "sparkle"
	f.r.Items = []audit.Item{app}
	f.o.ResolvedTargets[app.ID] = queryFailureTarget("native-app", app.Path, os.ErrPermission)
	f.r.Items[0].RealPath = app.Path + "-changed"
	if p := f.plan(Selection{Action: "update", All: true}); p.Items[0].Reason != "application_path_changed" {
		t.Fatal(p.Items)
	}
	f = setup(t)
	item, root := f.gitSkill()
	f.o.ResolvedTargets = map[string]ResolvedTarget{item.ID: queryFailureTarget("git-skill", root, os.ErrPermission)}
	f.write(filepath.Join(root, ".git/config"), "[filter \"custom\"]\n clean = execute\n", 0644)
	if p := f.plan(Selection{Action: "update", All: true}); p.Items[0].Reason != "skill_repository_requires_review" {
		t.Fatal(p.Items)
	}
}

func TestGitPredicateExitRetainsOnlyStatusAndPreservesFailurePriority(t *testing.T) {
	for _, tc := range []struct {
		stderr, reason string
		code           int
		predicate      bool
	}{
		{"", "owner_query_failed", 1, true},
		{"", "owner_query_failed", 2, true},
		{"Permission denied SECRET", "owner_permission_denied", 1, false},
		{"Could not resolve host SECRET", "owner_network_unavailable", 2, false},
	} {
		script := filepath.Join(t.TempDir(), "predicate")
		// Only this controlled process runs; no real remote or application is queried.
		exit := "1"
		if tc.code == 2 {
			exit = "2"
		}
		if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '"+tc.stderr+"' >&2\nexit "+exit+"\n"), 0755); err != nil {
			t.Fatal(err)
		}
		_, err := (ExecRunner{}).Run(context.Background(), Command{Path: script})
		if queryFailureReason(err) != tc.reason || gitPredicateExit(err, tc.code) != tc.predicate || strings.Contains(err.Error(), "SECRET") {
			t.Fatal(err)
		}
	}
}

func TestApplicationQueryPreservesStoreUninstallAndPermissionPriority(t *testing.T) {
	f := setup(t)
	_, app := f.cask()
	app.Owner = "app-store"
	f.r.Items = []audit.Item{app}
	selection := Selection{Action: "uninstall", IDs: []string{app.ID}}
	targets, err := ResolveApplicationTargets(context.Background(), f.r, selection, f.o, runFunc(func(context.Context, Command) ([]byte, error) { return nil, nil }))
	if err != nil || !targets[app.ID].OriginVerified {
		t.Fatal(targets, err)
	}
	f.o.ResolvedTargets = targets
	if plan := f.plan(selection); plan.Items[0].Status != "ready" {
		t.Fatal(plan.Items)
	}
	f.o.ResolvedTargets[app.ID] = queryFailureTarget("native-app", app.Path, context.DeadlineExceeded)
	if err := os.Chmod(app.Path, 0777); err != nil {
		t.Fatal(err)
	}
	if plan := f.plan(selection); plan.Items[0].Reason != "application_requires_owner_permission" || !plan.Items[0].RequiresAdmin {
		t.Fatal(plan.Items)
	}
}
