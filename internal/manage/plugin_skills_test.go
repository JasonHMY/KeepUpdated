package manage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

type pluginFixture struct {
	*fixture
	market, installed, commit, nextCommit, manager string
	commands                                       []Command
}

func setupPlugin(t *testing.T) *pluginFixture {
	t.Helper()
	f := &pluginFixture{fixture: setup(t)}
	f.market = filepath.Join(f.home, ".claude/plugins/marketplaces/fixture-market")
	f.installed = filepath.Join(f.home, ".claude/plugins/cache/fixture-market/example/1.0.0")
	f.manager = filepath.Join(f.home, "bin/claude")
	f.o.ManagerPaths["claude"] = f.manager
	f.write(f.manager, "controlled owner", 0755)
	f.write(filepath.Join(f.market, ".claude-plugin/marketplace.json"), `{"name":"fixture-market","plugins":[{"name":"example","source":"./example"}]}`, 0644)
	f.write(filepath.Join(f.market, "example/.claude-plugin/plugin.json"), `{"name":"example","version":"1.0.0"}`, 0644)
	f.write(filepath.Join(f.market, "example/skills/demo/SKILL.md"), "---\nname: demo\n---\nold\n", 0644)
	f.write(filepath.Join(f.market, "example/skills/other/SKILL.md"), "---\nname: other\n---\nold\n", 0644)
	f.git("init", "-b", "main")
	f.git("remote", "add", "origin", "https://github.com/example/fixture-market.git")
	f.git("add", ".")
	f.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "initial")
	f.commit = f.git("rev-parse", "HEAD")
	snap, e := audit.ReadSkillUpdateSnapshot(filepath.Join(f.market, "example"))
	if e != nil {
		t.Fatal(e)
	}
	if e = writeSkillFiles(f.installed, snap); e != nil {
		t.Fatal(e)
	}
	f.registry("1.0.0", f.installed, f.commit)
	if e := os.Mkdir(filepath.Join(f.installed, ".in_use"), 0755); e != nil {
		t.Fatal(e)
	}
	f.write(filepath.Join(f.home, ".claude/plugins/known_marketplaces.json"), `{"fixture-market":{"source":{"source":"git","url":"https://github.com/example/fixture-market.git"},"installLocation":`+quote(f.market)+`,"lastUpdated":"2026-10-19T00:00:00Z"}}`, 0600)
	f.write(filepath.Join(f.home, ".claude/settings.json"), `{"enabledPlugins":{"example@fixture-market":true}}`, 0600)
	f.write(filepath.Join(f.market, "example/.claude-plugin/plugin.json"), `{"name":"example","version":"1.1.0"}`, 0644)
	f.write(filepath.Join(f.market, "example/skills/demo/SKILL.md"), "---\nname: demo\n---\nnew\n", 0644)
	f.git("add", ".")
	f.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "candidate")
	f.nextCommit = f.git("rev-parse", "HEAD")
	f.git("update-ref", "refs/remotes/origin/main", f.nextCommit)
	f.refresh()
	return f
}
func quote(s string) string { b, _ := json.Marshal(s); return string(b) }
func (f *pluginFixture) git(args ...string) string {
	f.t.Helper()
	c := exec.Command("/usr/bin/git", append([]string{"-C", f.market}, args...)...)
	c.Env = []string{"HOME=" + f.home, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	b, e := c.CombinedOutput()
	if e != nil {
		f.t.Fatalf("git %v: %s %v", args, b, e)
	}
	return strings.TrimSpace(string(b))
}
func (f *pluginFixture) registry(v, p, c string) {
	f.t.Helper()
	records, _ := audit.ReadClaudePluginRegistry(f.home)
	if records == nil {
		records = map[string][]audit.ClaudePluginRecord{}
	}
	records["example@fixture-market"] = []audit.ClaudePluginRecord{{Scope: "user", InstallPath: p, Version: v, GitCommitSHA: c}}
	b, _ := json.Marshal(map[string]any{"version": 2, "plugins": records})
	f.write(filepath.Join(f.home, ".claude/plugins/installed_plugins.json"), string(b), 0600)
}

func (f *pluginFixture) refresh() {
	f.t.Helper()
	r := audit.New(audit.Config{Home: f.home, Path: "", Timeout: 0}).Scan(false)
	f.r = audit.Report{Schema: audit.Schema}
	for _, i := range r.Items {
		if i.Kind == "skill" {
			f.r.Items = append(f.r.Items, i)
		}
	}
}
func (f *pluginFixture) run(ctx context.Context, c Command) ([]byte, error) {
	if c.Path == "/usr/bin/git" {
		return (ExecRunner{}).Run(ctx, c)
	}
	f.commands = append(f.commands, c)
	if reflect.DeepEqual(c.Args, []string{"--version"}) {
		return []byte("2.1.302 (Claude Code)\n"), nil
	}
	return nil, errors.New("unexpected mutation")
}
func (f *pluginFixture) planPlugin() Plan {
	f.t.Helper()
	var e error
	f.o.ResolvedTargets, e = ResolvePluginTargets(context.Background(), f.r, Selection{Action: "update", All: true, CachedPlugins: true}, f.o, runFunc(f.run))
	if e != nil {
		f.t.Fatal(e)
	}
	return f.plan(Selection{Action: "update", All: true, CachedPlugins: true})
}
func (f *pluginFixture) pluginEngine(run Runner) Engine {
	e := f.engine(run)
	e.Snapshot = func(context.Context) (audit.Report, error) { f.refresh(); return f.r, nil }
	e.ResolveTargets = func(ctx context.Context, r audit.Report, s Selection) (map[string]ResolvedTarget, error) {
		return ResolvePluginTargets(ctx, r, s, f.o, runFunc(f.run))
	}
	return e
}

func TestPluginOwnerPlanDeduplicatesSkills(t *testing.T) {
	f := setupPlugin(t)
	p := f.planPlugin()
	if len(p.Items) != 2 || p.Items[0].Status != "ready" || p.Items[1].Reason != "duplicate_installation_target" {
		t.Fatalf("%+v", p.Items)
	}
	pi := p.Items[0]
	if pi.Provider != "claude-plugin" || pi.PluginUpdate == nil || !pi.PluginUpdate.ReloadRequired || len(pi.AffectedIDs) != 1 || pi.TargetVersion != "1.1.0" {
		t.Fatal(pi)
	}
	for _, c := range f.commands {
		if !reflect.DeepEqual(c.Args, []string{"--version"}) {
			t.Fatal("preview mutated owner", c)
		}
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "https://github.com") {
		t.Fatal("raw registry source leaked")
	}
}

func TestPluginSourceAndContentGuards(t *testing.T) {
	for _, scenario := range []string{"local-modification", "dirty-market", "wrong-owner", "pin", "different-origin", "hooks", "dependency", "unchanged-version", "unregistered", "existing-target", "untracked-commit", "source-override", "disabled", "unknown-source-field"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupPlugin(t)
			switch scenario {
			case "existing-target":
				f.write(filepath.Join(filepath.Dir(f.installed), "1.1.0/skills/demo/SKILL.md"), "prior local work", 0644)
			case "untracked-commit":
				f.git("update-ref", "refs/remotes/origin/main", f.commit)
			case "source-override":
				f.write(filepath.Join(f.home, ".claude/settings.json"), `{"enabledPlugins":{"example@fixture-market":true},"extraKnownMarketplaces":{"fixture-market":{"source":{"source":"git","url":"https://github.com/other/fixture-market.git"}}}}`, 0600)
			case "disabled":
				f.write(filepath.Join(f.home, ".claude/settings.json"), `{"enabledPlugins":{"example@fixture-market":false}}`, 0600)
			case "unknown-source-field":
				f.write(filepath.Join(f.home, ".claude/plugins/known_marketplaces.json"), `{"fixture-market":{"source":{"source":"git","url":"https://github.com/example/fixture-market.git","headersHelper":"arbitrary-command"},"installLocation":`+quote(f.market)+`}}`, 0600)
			case "local-modification":
				f.write(filepath.Join(f.installed, "skills/demo/SKILL.md"), "user edit", 0644)
			case "dirty-market":
				f.write(filepath.Join(f.market, "example/skills/demo/SKILL.md"), "uncommitted", 0644)
			case "wrong-owner":
				for n := range f.r.Items {
					f.r.Items[n].Plugin.ID = "other@fixture-market"
				}
			case "pin":
				f.write(filepath.Join(f.home, ".claude/plugins/known_marketplaces.json"), `{"fixture-market":{"source":{"source":"git","url":"https://github.com/example/fixture-market.git","ref":"v1"},"installLocation":`+quote(f.market)+`}}`, 0600)
			case "different-origin":
				f.git("remote", "set-url", "origin", "https://github.com/other/fixture-market.git")
			case "hooks", "dependency":
				field := map[string]string{"hooks": `"hooks":{"SessionStart":[]}`, "dependency": `"dependencies":["other"]`}[scenario]
				f.write(filepath.Join(f.market, "example/.claude-plugin/plugin.json"), `{"name":"example","version":"1.1.0",`+field+`}`, 0644)
				f.git("add", ".")
				f.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "unsafe")
			case "unchanged-version":
				f.write(filepath.Join(f.market, "example/.claude-plugin/plugin.json"), `{"name":"example","version":"1.0.0"}`, 0644)
				f.git("add", ".")
				f.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "same-version")
			case "unregistered":
				f.registry("1.0.0", f.installed, "unverified")
			}
			if scenario == "hooks" || scenario == "dependency" || scenario == "unchanged-version" {
				f.git("update-ref", "refs/remotes/origin/main", "HEAD")
			}
			p := f.planPlugin()
			for _, pi := range p.Items {
				if pi.Status == "ready" {
					t.Fatal("unsafe plan", scenario, pi)
				}
			}
		})
	}
}

func TestPluginApplyRechecksSourceAndContent(t *testing.T) {
	for _, scenario := range []string{"source", "local", "manager", "registry"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupPlugin(t)
			if scenario == "hooks" || scenario == "dependency" || scenario == "unchanged-version" {
				f.git("update-ref", "refs/remotes/origin/main", "HEAD")
			}
			p := f.planPlugin()
			switch scenario {
			case "source":
				f.git("remote", "set-url", "origin", "https://github.com/other/fixture-market.git")
			case "local":
				f.write(filepath.Join(f.installed, "skills/demo/SKILL.md"), "edit", 0644)
			case "manager":
				f.write(f.manager, "changed executable", 0755)
			case "registry":
				f.registry("1.1.0", f.installed, f.nextCommit)
			}
			called := false
			_, e := Apply(context.Background(), p, p.PlanID, f.pluginEngine(runFunc(func(context.Context, Command) ([]byte, error) { called = true; return nil, nil })))
			if e == nil || called {
				t.Fatal("drift executed", e, called)
			}
		})
	}
}

func TestPluginApplyOwnerReadbackAndRecovery(t *testing.T) {
	for _, scenario := range []string{"success", "failure-preserved", "failure-mutated", "failure-old-content", "not-activated", "bad-content", "disabled", "in-place", "wrong-version"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupPlugin(t)
			if scenario == "hooks" || scenario == "dependency" || scenario == "unchanged-version" {
				f.git("update-ref", "refs/remotes/origin/main", "HEAD")
			}
			p := f.planPlugin()
			calls := 0
			runner := runFunc(func(ctx context.Context, c Command) ([]byte, error) {
				if c.Path == "/usr/bin/sandbox-exec" {
					if len(c.Args) < 4 || c.Args[0] != "-p" || c.Args[1] != pluginNetworkDenyProfile {
						t.Fatal(c)
					}
					c.Path, c.Args = c.Args[2], c.Args[3:]
				}
				if c.Path == "/usr/bin/git" || reflect.DeepEqual(c.Args, []string{"--version"}) {
					return f.run(ctx, c)
				}
				calls++
				if c.Path != f.manager || c.Dir != f.home || !contains(c.Env, "GIT_ALLOW_PROTOCOL=") || !contains(c.Env, "CLAUDE_CONFIG_DIR="+filepath.Join(f.home, ".claude")) {
					t.Fatal(c)
				}
				dest := filepath.Join(f.home, ".claude/plugins/cache/fixture-market/example/1.1.0")
				if reflect.DeepEqual(c.Args, []string{"plugin", "list", "--json"}) {
					n := nativePlugin{ID: "example@fixture-market", Version: "1.1.0", Scope: "user", Enabled: true, InstallPath: dest}
					if scenario == "disabled" {
						n.Enabled = false
					}
					if scenario == "in-place" {
						n.ReadFromFolder = f.market
					}
					b, _ := json.Marshal([]nativePlugin{n})
					return b, nil
				}
				if !reflect.DeepEqual(c.Args, []string{"plugin", "update", "example@fixture-market", "--scope", "user", "--json"}) {
					t.Fatal(c.Args)
				}
				if scenario == "failure-old-content" {
					f.write(filepath.Join(f.installed, "skills/demo/SKILL.md"), "partial edit", 0644)
					return nil, errors.New("native partial failure")
				}
				if scenario == "failure-preserved" {
					return nil, errors.New("native failure")
				}
				if scenario != "not-activated" {
					snap, e := audit.ReadSkillUpdateSnapshot(filepath.Join(f.market, "example"))
					if e != nil {
						t.Fatal(e)
					}
					if e = writeSkillFiles(dest, snap); e != nil {
						t.Fatal(e)
					}
					f.registry("1.1.0", dest, f.nextCommit)
					if scenario == "bad-content" {
						f.write(filepath.Join(dest, "skills/demo/SKILL.md"), "corrupt", 0644)
					}
					if scenario == "wrong-version" {
						f.registry("1.2.0", dest, f.nextCommit)
					}
				}
				if scenario == "failure-mutated" {
					return nil, errors.New("native failure")
				}
				return []byte(`{"command":"update","outcome":"ok","pluginId":"example@fixture-market","scope":"user","newVersion":"1.1.0"}`), nil
			})
			out, e := Apply(context.Background(), p, p.PlanID, f.pluginEngine(runner))
			if e != nil || len(out.Results) != 2 {
				t.Fatal(out, e)
			}
			r := out.Results[0]
			if !r.Executed || r.BackupPath == "" || calls < 1 {
				t.Fatal(r)
			}
			switch scenario {
			case "success":
				if r.Status != "updated" || !r.ReloadRequired {
					t.Fatal(r)
				}
			case "failure-preserved":
				if r.Status != "failed" || r.Reason != "plugin_owner_failed_original_preserved" {
					t.Fatal(r)
				}
			default:
				if scenario == "failure-old-content" && !r.Changed {
					t.Fatal("changed original reported unchanged", r)
				}
				if r.Status != "verification_unknown" {
					t.Fatal(r)
				}
			}
			preserved, e := audit.ReadSkillUpdateSnapshot(filepath.Join(r.BackupPath, "plugin"))
			if e != nil || preserved.Tree != p.Items[0].PluginUpdate.CurrentTree {
				t.Fatal("backup missing", e)
			}
			if _, e = os.Stat(filepath.Join(r.BackupPath, "installed_plugins.json")); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestPluginRechecksSourceAfterBackup(t *testing.T) {
	f := setupPlugin(t)
	p := f.planPlugin()
	raced, mutated := false, false
	runner := runFunc(func(ctx context.Context, c Command) ([]byte, error) {
		if c.Path == "/usr/bin/sandbox-exec" {
			if len(c.Args) < 4 || c.Args[0] != "-p" || c.Args[1] != pluginNetworkDenyProfile {
				t.Fatal(c)
			}
			c.Path, c.Args = c.Args[2], c.Args[3:]
		}
		if c.Path == "/usr/bin/git" {
			b, e := (ExecRunner{}).Run(ctx, c)
			if !raced {
				raced = true
				f.write(filepath.Join(f.home, ".claude/settings.json"), `{"enabledPlugins":{"example@fixture-market":false}}`, 0600)
			}
			return b, e
		}
		if reflect.DeepEqual(c.Args, []string{"--version"}) {
			return f.run(ctx, c)
		}
		mutated = true
		return nil, errors.New("must not update")
	})
	out, e := Apply(context.Background(), p, p.PlanID, f.pluginEngine(runner))
	if e != nil || !raced || mutated || out.Results[0].Status != "blocked" || out.Results[0].Executed {
		t.Fatal(out, e, raced, mutated)
	}
}

func TestPluginRequiresDocumentedClientVersion(t *testing.T) {
	f := setupPlugin(t)
	for _, v := range []string{"2.1.267 (Claude Code)", "unrelated executable", "2.1.302 (Claude Code)"} {
		targets, e := ResolvePluginTargets(context.Background(), f.r, Selection{Action: "update", All: true, CachedPlugins: true}, f.o, runFunc(func(ctx context.Context, c Command) ([]byte, error) {
			if c.Path == f.manager {
				return []byte(v), nil
			}
			return f.run(ctx, c)
		}))
		if e != nil {
			t.Fatal(e)
		}
		for _, target := range targets {
			if target.OriginVerified != (v == "2.1.302 (Claude Code)") {
				t.Fatal(v, target)
			}
		}
	}
}

func TestPluginFixedCacheAliasDoesNotClaimOwnerUpdateChangesLink(t *testing.T) {
	f := setupPlugin(t)
	alias := filepath.Join(f.home, ".agents/skills/fixed-demo")
	if e := os.MkdirAll(filepath.Dir(alias), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(filepath.Join(f.installed, "skills/demo"), alias); e != nil {
		t.Fatal(e)
	}
	f.refresh()
	p := f.planPlugin()
	ready, manual := 0, 0
	for _, pi := range p.Items {
		if pi.Status == "ready" {
			ready++
		}
		if pi.Binding.Path == filepath.Join(alias, "SKILL.md") {
			if pi.Status != "manual" || pi.Reason != "plugin_skill_alias_version_bound" {
				t.Fatal(pi)
			}
			manual++
		}
	}
	if ready != 1 || manual != 1 {
		t.Fatal(p.Items)
	}
}

func TestPluginExactPreparedCacheIsBoundAndNotClaimedAsNewOnFailure(t *testing.T) {
	for _, scenario := range []string{"preserved", "modified-before", "deleted-during", "unreadable-during"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupPlugin(t)
			snapshot, e := audit.ReadSkillUpdateSnapshot(filepath.Join(f.market, "example"))
			if e != nil {
				t.Fatal(e)
			}
			target := filepath.Join(filepath.Dir(f.installed), "1.1.0")
			if e = writeSkillFiles(target, snapshot); e != nil {
				t.Fatal(e)
			}
			f.refresh()
			selection := Selection{Action: "update", CachedPlugins: true}
			for _, item := range f.r.Items {
				if item.Plugin != nil {
					selection.IDs = append(selection.IDs, item.ID)
				}
			}
			f.o.ResolvedTargets, e = ResolvePluginTargets(context.Background(), f.r, selection, f.o, runFunc(f.run))
			if e != nil {
				t.Fatal(e)
			}
			p := f.plan(selection)
			if p.Items[0].Status != "ready" || p.Items[0].PluginUpdate.TargetCacheFingerprint == "" {
				t.Fatal(p.Items)
			}
			called := false
			if scenario == "modified-before" {
				f.write(filepath.Join(target, "skills/demo/SKILL.md"), "local target edit", 0644)
			}
			defer os.Chmod(filepath.Dir(target), 0755)
			runner := runFunc(func(ctx context.Context, c Command) ([]byte, error) {
				if c.Path == "/usr/bin/sandbox-exec" {
					called = true
					if scenario == "deleted-during" {
						if e := os.RemoveAll(target); e != nil {
							t.Fatal(e)
						}
					}
					if scenario == "unreadable-during" {
						if e := os.Chmod(filepath.Dir(target), 0000); e != nil {
							t.Fatal(e)
						}
					}
					return nil, errors.New("native failure")
				}
				return f.run(ctx, c)
			})
			out, e := Apply(context.Background(), p, p.PlanID, f.pluginEngine(runner))
			if scenario == "modified-before" {
				if e == nil || called {
					t.Fatal(out, e, called)
				}
				return
			}
			if e != nil || !called {
				t.Fatal(out, e, called)
			}
			r := out.Results[0]
			switch scenario {
			case "preserved":
				if r.Changed || r.Reason != "plugin_owner_failed_original_preserved" {
					t.Fatal(r)
				}
			case "deleted-during":
				if !r.Changed || r.Status != "verification_unknown" || r.Reason != "plugin_recovery_required" || r.BackupPath == "" {
					t.Fatal(r)
				}
			case "unreadable-during":
				if r.Status != "verification_unknown" || r.Reason != "plugin_recovery_required" || r.BackupPath == "" {
					t.Fatal(r)
				}
			}
		})
	}
}
