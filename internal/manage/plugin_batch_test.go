package manage

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func setupPluginBatch(t *testing.T) *remotePluginFixture {
	base := setupPlugin(t)
	base.write(filepath.Join(base.market, ".claude-plugin/marketplace.json"), `{"name":"fixture-market","plugins":[{"name":"example","source":"./example"},{"name":"second","source":"./second"}]}`, 0644)
	base.write(filepath.Join(base.market, "second/.claude-plugin/plugin.json"), `{"name":"second","version":"1.0.0"}`, 0644)
	base.write(filepath.Join(base.market, "second/skills/second/SKILL.md"), "---\nname: second\n---\noriginal second\n", 0644)
	base.git("add", ".")
	base.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "second plugin")
	commit := base.git("rev-parse", "HEAD")
	base.git("update-ref", "refs/remotes/origin/main", commit)
	installed := filepath.Join(base.home, ".claude/plugins/cache/fixture-market/second/1.0.0")
	snapshot, e := audit.ReadSkillUpdateSnapshot(filepath.Join(base.market, "second"))
	if e != nil {
		t.Fatal(e)
	}
	if e = writeSkillFiles(installed, snapshot); e != nil {
		t.Fatal(e)
	}
	pluginBatchRecord(t, base, "second@fixture-market", installed, "1.0.0", commit)
	base.write(filepath.Join(base.home, ".claude/settings.json"), `{"enabledPlugins":{"example@fixture-market":true,"second@fixture-market":true}}`, 0600)
	f := &remotePluginFixture{pluginFixture: base, remote: filepath.Join(base.home, "remote")}
	c := exec.Command("/usr/bin/git", "clone", "--quiet", f.market, f.remote)
	c.Env = []string{"HOME=" + f.home, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if b, e := c.CombinedOutput(); e != nil {
		t.Fatal(string(b), e)
	}
	f.advance("1.2.0")
	old := f.market
	f.market = f.remote
	f.write(filepath.Join(f.remote, "second/.claude-plugin/plugin.json"), `{"name":"second","version":"1.1.0"}`, 0644)
	f.write(filepath.Join(f.remote, "second/skills/second/SKILL.md"), "---\nname: second\n---\nupdated second\n", 0644)
	f.git("add", ".")
	f.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "update second")
	f.tip = f.git("rev-parse", "HEAD")
	f.market = old
	f.refresh()
	return f
}
func pluginBatchRecord(t *testing.T, f *pluginFixture, id, path, version, commit string) {
	t.Helper()
	records, e := audit.ReadClaudePluginRegistry(f.home)
	if e != nil {
		t.Fatal(e)
	}
	records[id] = []audit.ClaudePluginRecord{{Scope: "user", InstallPath: path, Version: version, GitCommitSHA: commit}}
	b, _ := json.Marshal(map[string]any{"version": 2, "plugins": records})
	f.write(filepath.Join(f.home, ".claude/plugins/installed_plugins.json"), string(b), 0600)
}
func pluginBatchRunner(f *remotePluginFixture) Runner {
	return runFunc(func(ctx context.Context, c Command) ([]byte, error) {
		args := c.Args
		if c.Path == "/usr/bin/sandbox-exec" && len(args) > 3 && args[0] == "-p" && args[1] == pluginNetworkDenyProfile {
			args = args[3:]
		}
		if reflect.DeepEqual(args, []string{"plugin", "update", "second@fixture-market", "--scope", "user", "--json"}) {
			if c.Path != "/usr/bin/sandbox-exec" {
				f.t.Fatal("second plugin not isolated")
			}
			f.installs++
			dest := filepath.Join(f.home, ".claude/plugins/cache/fixture-market/second/1.1.0")
			snapshot, e := audit.ReadSkillUpdateSnapshot(filepath.Join(f.market, "second"))
			if e != nil {
				f.t.Fatal(e)
			}
			if _, e := os.Stat(dest); os.IsNotExist(e) {
				if e = writeSkillFiles(dest, snapshot); e != nil {
					f.t.Fatal(e)
				}
			} else if existing, e := audit.ReadSkillUpdateSnapshot(dest); e != nil || !samePluginFiles(existing, snapshot) {
				f.t.Fatal("owner received changed second cache")
			}
			pluginBatchRecord(f.t, f.pluginFixture, "second@fixture-market", dest, "1.1.0", f.tip)
			return []byte(`{"command":"update","outcome":"ok","pluginId":"second@fixture-market","scope":"user","newVersion":"1.1.0"}`), nil
		}
		return f.run(ctx, c)
	})
}

func TestPluginBatchSharesOnlyVerifiedMarketplaceRefresh(t *testing.T) {
	for _, scenario := range []string{"git", "archive", "precache", "precache-drift", "source-drift", "remote-drift"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupPluginBatch(t)
			if scenario == "archive" {
				f.scenario = "archive"
			}
			if scenario == "precache" || scenario == "precache-drift" {
				f.scenario = "precache"
			}
			selection := Selection{Action: "update", All: true}
			targets, e := ResolvePluginTargets(context.Background(), f.r, selection, f.o, runFunc(f.run))
			if e != nil {
				t.Fatal(e)
			}
			f.o.ResolvedTargets = targets
			p := f.plan(selection)
			ready := 0
			for _, pi := range p.Items {
				if pi.Status == "ready" {
					ready++
				}
			}
			if ready != 2 {
				t.Fatal(p.Items)
			}
			engine := f.engineRemote()
			engine.Runner = pluginBatchRunner(f)
			originalSnapshot := engine.Snapshot
			afterFirst := 0
			engine.Snapshot = func(ctx context.Context) (audit.Report, error) {
				if f.installs == 1 {
					afterFirst++
					// First snapshot records the verified effect; the next is independent preflight.
					if afterFirst == 2 {
						if scenario == "source-drift" {
							f.write(filepath.Join(f.market, ".claude-plugin/marketplace.json"), `{"name":"changed","plugins":[]}`, 0644)
						}
						if scenario == "remote-drift" {
							f.advance("1.3.0")
						}
						if scenario == "precache-drift" {
							records, e := audit.ReadClaudePluginRegistry(f.home)
							if e != nil {
								t.Fatal(e)
							}
							for id, rows := range records {
								for _, row := range rows {
									name := strings.Split(id, "@")[0]
									target := map[string]string{"example": "1.2.0", "second": "1.1.0"}[name]
									if row.Version != target {
										skill := map[string]string{"example": "demo", "second": "second"}[name]
										f.write(filepath.Join(filepath.Dir(row.InstallPath), target, "skills", skill, "SKILL.md"), "local target cache edit", 0644)
									}
								}
							}
						}
					}
				}
				return originalSnapshot(ctx)
			}
			result, e := Apply(context.Background(), p, p.PlanID, engine)
			if e != nil {
				t.Fatal(e)
			}
			updated, blocked, refreshed := 0, 0, 0
			for _, r := range result.Results {
				if r.Status == "updated" {
					updated++
				}
				if r.Status == "blocked" {
					blocked++
				}
				if r.MarketplaceRefresh != nil && r.MarketplaceRefresh.Attempted {
					refreshed++
				}
			}
			if f.refreshes != 1 || refreshed != 1 {
				t.Fatal("marketplace refreshed more than once", result)
			}
			if scenario == "git" || scenario == "archive" || scenario == "precache" {
				if updated != 2 || blocked != 0 || f.installs != 2 {
					t.Fatal(result)
				}
			} else if updated != 1 || blocked != 1 || f.installs != 1 {
				t.Fatal("unexpected drift was accepted", result)
			}
		})
	}
}
