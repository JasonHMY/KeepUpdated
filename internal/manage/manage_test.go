package manage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

type runFunc func(context.Context, Command) ([]byte, error)

func (f runFunc) Run(ctx context.Context, c Command) ([]byte, error) { return f(ctx, c) }

type fixture struct {
	t     *testing.T
	home  string
	o     Options
	r     audit.Report
	state string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	h, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, home: h, state: filepath.Join(h, "state"), r: audit.Report{Schema: audit.Schema, Items: []audit.Item{}}}
	f.o = Options{Home: h, BrewRoots: []string{filepath.Join(h, "brew")}, ManagerPaths: map[string]string{"npm": filepath.Join(h, ".npm-global/bin/npm")}, Now: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)}
	f.write(f.o.ManagerPaths["npm"], "fake manager\n", 0755)
	return f
}
func (f *fixture) write(p, s string, mode os.FileMode) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) npm(id, name string) audit.Item {
	f.t.Helper()
	p := filepath.Join(f.home, ".npm-global/lib/node_modules", name)
	f.write(filepath.Join(p, "package.json"), `{"name":"`+name+`","version":"1.0.0","_resolved":"https://registry.npmjs.org/`+name+`/-/`+filepath.Base(name)+`-1.0.0.tgz"}`, 0644)
	i := audit.Item{ID: id, Kind: "node-package", Name: name, Identity: name, Path: p, RealPath: p, Owner: "npm", Source: "npm-public", Channel: "latest", Version: "1.0.0", Latest: "1.1.0", Health: audit.Result{Status: "ok"}, Update: audit.Result{Status: "available"}}
	f.r.Items = append(f.r.Items, i)
	if f.o.ResolvedTargets == nil {
		f.o.ResolvedTargets = map[string]ResolvedTarget{}
	}
	f.o.ResolvedTargets[i.ID] = ResolvedTarget{Provider: "npm", ManagerPath: f.o.ManagerPaths["npm"], Scope: filepath.Join(f.home, ".npm-global"), OriginVerified: true, Source: "fixture native configuration", Version: i.Latest, Node: f.nodePolicy(i, filepath.Join(f.home, ".npm-global"), f.o.ManagerPaths["npm"], "11.19.1")}
	return i
}
func (f *fixture) plan(s Selection) Plan {
	f.t.Helper()
	p, err := NewPlan(f.r, s, f.o)
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}
func (f *fixture) engine(run Runner) Engine {
	return Engine{Options: f.o, StateDir: f.state, Runner: run, Now: func() time.Time { return f.o.Now.Add(time.Minute) }, Snapshot: func(context.Context) (audit.Report, error) { return f.r, nil }, ResolveTargets: func(context.Context, audit.Report, Selection) (map[string]ResolvedTarget, error) {
		return f.o.ResolvedTargets, nil
	}}
}

func TestPreviewFixedArgumentsAndExactVersion(t *testing.T) {
	f := setup(t)
	i := f.npm("one", "@scope/example")
	p := f.plan(Selection{Action: "update", IDs: []string{i.ID}})
	if p.Items[0].Status != "ready" {
		t.Fatal(p.Items)
	}
	c, err := commandFor(p.Items[0], p.Action, normalizeOptions(f.o), f.state)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"install", "--global", "--prefix", filepath.Join(f.home, ".npm-global"), "--ignore-scripts", "--no-audit", "--no-fund", "--no-save", "--", "@scope/example@1.1.0"}
	if c.Path != f.o.ManagerPaths["npm"] || !reflect.DeepEqual(c.Args, want) {
		t.Fatalf("%#v", c)
	}
	if strings.Contains(strings.Join(c.Env, "\n"), "TOKEN=") {
		t.Fatal("inherited credentials")
	}
	if p.PlanID != planHash(p) || p.Schema != PlanSchema {
		t.Fatal("invalid immutable preview")
	}
}

func TestApplyRequiresFreshStateAndVerifiesUpdate(t *testing.T) {
	f := setup(t)
	i := f.npm("one", "example")
	p := f.plan(Selection{Action: "update", IDs: []string{i.ID}})
	calls := 0
	e := f.engine(runFunc(func(ctx context.Context, c Command) ([]byte, error) {
		calls++
		f.r.Items[0].Version = "1.1.0"
		f.write(filepath.Join(i.Path, "package.json"), `{"name":"example","version":"1.1.0"}`, 0644)
		return nil, nil
	}))
	out, err := Apply(context.Background(), p, p.PlanID, e)
	if err != nil || calls != 1 || len(out.Results) != 1 || out.Results[0].Status != "updated" || !out.Results[0].Changed || !out.Results[0].Executed || !out.Results[0].CommandSucceeded {
		t.Fatalf("%+v %v", out, err)
	}
	if out.FinishedAt == "" {
		t.Fatal("missing finish timestamp")
	}
}

func TestTamperedStaleExpiredAndReplayedPlansDoNotRun(t *testing.T) {
	for _, scenario := range []string{"confirmation", "tampered", "rehash", "expired", "metadata", "manager", "replay"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			i := f.npm("one", "example")
			p := f.plan(Selection{Action: "update", IDs: []string{i.ID}})
			calls := 0
			e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) { calls++; return nil, nil }))
			confirm := p.PlanID
			switch scenario {
			case "confirmation":
				confirm = "yes"
			case "tampered":
				p.Items[0].Name = "other"
			case "rehash":
				p.Items[0].TargetVersion = "9.9.9"
				p.PlanID = planHash(p)
				confirm = p.PlanID
			case "expired":
				e.Now = func() time.Time { return f.o.Now.Add(time.Hour) }
			case "metadata":
				f.write(filepath.Join(i.Path, "package.json"), `{"name":"example","version":"2.0.0"}`, 0644)
			case "manager":
				f.write(f.o.ManagerPaths["npm"], "changed manager\n", 0755)
			case "replay":
				if err := os.MkdirAll(f.state, 0700); err != nil {
					t.Fatal(err)
				}
				f.write(filepath.Join(f.state, p.PlanID+".attempted"), "attempted", 0600)
			}
			if _, err := Apply(context.Background(), p, confirm, e); err == nil {
				t.Fatal("invalid plan accepted")
			}
			if calls != 0 {
				t.Fatal("executed invalid plan")
			}
		})
	}
}

func TestFailureIsolationAndVerificationUnknown(t *testing.T) {
	f := setup(t)
	f.npm("one", "first")
	f.npm("two", "second")
	p := f.plan(Selection{Action: "update", All: true})
	calls := 0
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("SECRET private manager output")
		}
		return nil, nil
	}))
	out, err := Apply(context.Background(), p, p.PlanID, e)
	if err != nil || calls != 2 || out.Results[0].Status != "failed" || out.Results[1].Status != "verification_unknown" {
		t.Fatalf("%+v %v", out, err)
	}
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "SECRET") {
		t.Fatal("error exposed private data")
	}
}

func TestUnknownCandidatePrivateSourceScriptsAndPermissions(t *testing.T) {
	for _, scenario := range []string{"unknown", "private", "script", "world-writable", "cleanup", "linked"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			i := f.npm("one", "example")
			s := Selection{Action: "update", All: true}
			switch scenario {
			case "unknown":
				f.r.Items[0].Latest = ""
				f.r.Items[0].Update.Status = "unknown"
				delete(f.o.ResolvedTargets, i.ID)
			case "private":
				f.r.Items[0].Source = "npm-public-candidate"
				delete(f.o.ResolvedTargets, i.ID)
			case "script":
				f.write(filepath.Join(i.Path, "package.json"), `{"name":"example","version":"1.0.0","scripts":{"postinstall":"arbitrary"}}`, 0644)
			case "world-writable":
				if err := os.Chmod(i.Path, 0777); err != nil {
					t.Fatal(err)
				}
			case "cleanup":
				s.Action = "uninstall"
				s.Cleanup = true
			case "linked":
				if err := os.Rename(i.Path, i.Path+"-other"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(i.Path+"-other", i.Path); err != nil {
					t.Fatal(err)
				}
			}
			p := f.plan(s)
			if p.Items[0].Status == "ready" {
				t.Fatal("unverified operation ready", p.Items)
			}
		})
	}
}

func TestDuplicateTargetAndReverseDependency(t *testing.T) {
	f := setup(t)
	i := f.npm("a", "example")
	copy := i
	copy.ID = "b"
	f.r.Items = append(f.r.Items, copy)
	f.o.ResolvedTargets[copy.ID] = f.o.ResolvedTargets[i.ID]
	p := f.plan(Selection{Action: "update", All: true})
	if p.Items[0].Status != "ready" || p.Items[1].Status != "skipped" {
		t.Fatal(p.Items)
	}
	f.r.Items[1] = audit.Item{ID: "dependent", Path: filepath.Join(filepath.Dir(i.Path), "consumer"), Dependencies: []audit.Dependency{{Name: "example"}}}
	p = f.plan(Selection{Action: "uninstall", IDs: []string{i.ID}})
	if p.Items[0].Reason != "required_by_other_installations" {
		t.Fatal(p.Items)
	}
}

func TestNoConcurrentApplyAndNoReplayAfterFailedVerification(t *testing.T) {
	f := setup(t)
	i := f.npm("one", "example")
	p := f.plan(Selection{Action: "update", IDs: []string{i.ID}})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
		once.Do(func() { close(entered) })
		<-release
		return nil, nil
	}))
	done := make(chan error, 1)
	go func() { _, err := Apply(context.Background(), p, p.PlanID, e); done <- err }()
	<-entered
	if _, err := Apply(context.Background(), p, p.PlanID, e); err == nil {
		t.Fatal("concurrent apply accepted")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), p, p.PlanID, e); err == nil {
		t.Fatal("replayed plan accepted")
	}
}

func TestDecodeRejectsArbitraryCommandsAndTrailingJSON(t *testing.T) {
	for _, raw := range []string{`{"schema":"keepupdated/plan/v1","command":"rm"}`, `{} {}`, `{"items":[{"argv":["sh"]}]}`} {
		if _, err := DecodePlan(strings.NewReader(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	f := setup(t)
	f.npm("a", "example")
	p := f.plan(Selection{Action: "update", All: true})
	b, _ := json.Marshal(p)
	decoded, err := DecodePlan(strings.NewReader(string(b)))
	if err != nil || !reflect.DeepEqual(decoded, p) {
		t.Fatalf("JSON round trip: %v", err)
	}
}

func TestResolverRevalidatedAndBoundToManagerScope(t *testing.T) {
	f := setup(t)
	i := f.npm("a", "example")
	f.r.Items[0].Source = "npm-public-candidate"
	target := ResolvedTarget{Provider: "npm", ManagerPath: f.o.ManagerPaths["npm"], Scope: filepath.Join(f.home, ".npm-global"), OriginVerified: true, Source: "installed registry digest", Version: "1.2.0", Node: f.o.ResolvedTargets[i.ID].Node}
	f.o.ResolvedTargets = map[string]ResolvedTarget{i.ID: target}
	p := f.plan(Selection{Action: "update", All: true})
	if p.Items[0].Status != "ready" || p.Items[0].TargetVersion != "1.2.0" {
		t.Fatal(p.Items)
	}
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) { t.Fatal("changed source executed"); return nil, nil }))
	e.ResolveTargets = func(context.Context, audit.Report, Selection) (map[string]ResolvedTarget, error) {
		target.Source = "different digest"
		return map[string]ResolvedTarget{i.ID: target}, nil
	}
	if _, err := Apply(context.Background(), p, p.PlanID, e); err == nil {
		t.Fatal("changed resolver source accepted")
	}
}

func TestApplyCannotReuseCachedOwnerQueryWithoutFreshResolver(t *testing.T) {
	f := setup(t)
	f.npm("a", "example")
	p := f.plan(Selection{Action: "update", All: true})
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) { t.Fatal("cached source executed"); return nil, nil }))
	e.ResolveTargets = nil
	if _, err := Apply(context.Background(), p, p.PlanID, e); err == nil {
		t.Fatal("cached candidate reused without requery")
	}
}

func TestApplyRequeriesOnlyTheCurrentItemAfterFullPlanPreflight(t *testing.T) {
	f := setup(t)
	f.npm("a", "first")
	f.npm("b", "second")
	p := f.plan(Selection{Action: "update", All: true})
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) { return nil, nil }))
	queries := []Selection{}
	e.ResolveTargets = func(_ context.Context, _ audit.Report, s Selection) (map[string]ResolvedTarget, error) {
		queries = append(queries, s)
		return f.o.ResolvedTargets, nil
	}
	if _, err := Apply(context.Background(), p, p.PlanID, e); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 3 || !queries[0].All || queries[1].All || queries[2].All || len(queries[1].IDs) != 1 || len(queries[2].IDs) != 1 || queries[1].IDs[0] == queries[2].IDs[0] {
		t.Fatal("quadratic resolver scope", queries)
	}
}

func TestSkillLinkUninstallPreservesSharedSource(t *testing.T) {
	f := setup(t)
	source := filepath.Join(f.home, "shared/example")
	f.write(filepath.Join(source, "SKILL.md"), "---\nname: example\n---\n", 0644)
	link := filepath.Join(f.home, ".agents/skills/example")
	if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	i := audit.Item{ID: "skill", Kind: "skill", Name: "example", Owner: ".agents", Source: "local", Path: filepath.Join(link, "SKILL.md")}
	f.r.Items = []audit.Item{i}
	p := f.plan(Selection{Action: "uninstall", All: true})
	if p.Items[0].Status != "ready" || p.Items[0].Binding.Scope != link {
		t.Fatal(p.Items)
	}
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) { t.Fatal("skill invoked manager"); return nil, nil }))
	e.Snapshot = func(context.Context) (audit.Report, error) {
		r := f.r
		if _, err := os.Lstat(i.Path); os.IsNotExist(err) {
			r.Items = nil
		}
		return r, nil
	}
	out, err := Apply(context.Background(), p, p.PlanID, e)
	if err != nil || out.Results[0].Status != "uninstalled" {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Fatal("shared source removed", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("link not removed")
	}
}

func TestSkillDirtyAndLinkedRootCannotBeRemoved(t *testing.T) {
	for _, linked := range []bool{false, true} {
		f := setup(t)
		root := filepath.Join(f.home, ".agents/skills")
		path := filepath.Join(root, "example/SKILL.md")
		f.write(path, "---\n---", 0644)
		i := audit.Item{ID: "s", Kind: "skill", Path: path, Owner: ".agents", Source: "git", Evidence: []string{"git-local-modifications"}}
		if linked {
			target := filepath.Join(f.home, "shared-skills")
			if err := os.Rename(root, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, root); err != nil {
				t.Fatal(err)
			}
			i.Source = "local"
			i.Evidence = nil
		}
		f.r.Items = []audit.Item{i}
		p := f.plan(Selection{Action: "uninstall", All: true})
		if p.Items[0].Status == "ready" {
			t.Fatal("unsafe skill removal ready", p.Items)
		}
	}
}

func TestCopiedSkillEvidenceWithoutVerifiedReceiptCannotExecute(t *testing.T) {
	for _, code := range []string{"skill_update_available_manual", "skill_upstream_changed_local_modified"} {
		t.Run(code, func(t *testing.T) {
			f := setup(t)
			path := filepath.Join(f.home, ".agents/skills/example/SKILL.md")
			f.write(path, "---\nname: example\n---\n", 0644)
			i := audit.Item{ID: "skill", Kind: "skill", Name: "example", Path: path, Owner: ".agents", Source: "local", Version: "111111111111", Latest: "222222222222", Health: audit.Result{Status: "ok"}, Update: audit.Result{Status: "available", Code: code}, Evidence: []string{"skill-update:manual-original-manager"}}
			if code != "skill_update_available_manual" {
				i.Update.Status = "unknown"
			}
			f.r.Items = []audit.Item{i}
			p := f.plan(Selection{Action: "update", All: true})
			if p.Items[0].Status == "ready" {
				t.Fatal("copied skill was executable", p.Items)
			}
			if p.Items[0].Status != "blocked" || p.Items[0].Reason != "skill_source_unverified" {
				t.Fatal(p.Items)
			}
			b, err := os.ReadFile(path)
			if err != nil || string(b) != "---\nname: example\n---\n" {
				t.Fatal("preview changed skill", err)
			}
		})
	}
}
