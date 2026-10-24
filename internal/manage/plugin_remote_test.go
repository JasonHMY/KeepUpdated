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

type remotePluginFixture struct {
	*pluginFixture
	remote, tip         string
	refreshes, installs int
	scenario            string
}

func setupRemotePlugin(t *testing.T) *remotePluginFixture {
	f := &remotePluginFixture{pluginFixture: setupPlugin(t)}
	f.remote = filepath.Join(f.home, "remote")
	c := exec.Command("/usr/bin/git", "clone", "--quiet", f.market, f.remote)
	c.Env = []string{"HOME=" + f.home, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if b, e := c.CombinedOutput(); e != nil {
		t.Fatal(string(b), e)
	}
	f.advance("1.2.0")
	return f
}
func (f *remotePluginFixture) advance(v string) {
	old := f.market
	f.market = f.remote
	f.write(filepath.Join(f.remote, "example/.claude-plugin/plugin.json"), `{"name":"example","version":"`+v+`"}`, 0644)
	f.write(filepath.Join(f.remote, "example/skills/demo/SKILL.md"), "---\nname: demo\n---\nremote "+v+"\n", 0644)
	f.git("add", ".")
	f.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "new remote version")
	f.tip = f.git("rev-parse", "HEAD")
	f.market = old
}
func (f *remotePluginFixture) run(ctx context.Context, c Command) ([]byte, error) {
	if c.Path == "/usr/bin/git" {
		// Controlled transport only: real isolated Git objects, no public network.
		c.Args = append([]string(nil), c.Args...)
		c.Env = append([]string(nil), c.Env...)
		for i, a := range c.Args {
			if a == "https://github.com/example/fixture-market.git" {
				c.Args[i] = f.remote
			}
		}
		for i, e := range c.Env {
			if e == "GIT_ALLOW_PROTOCOL=https" {
				c.Env[i] = "GIT_ALLOW_PROTOCOL=file"
			}
		}
		return (ExecRunner{}).Run(ctx, c)
	}
	isolated := c.Path == "/usr/bin/sandbox-exec"
	if isolated {
		if len(c.Args) < 4 || c.Args[0] != "-p" || c.Args[1] != pluginNetworkDenyProfile {
			f.t.Fatal(c)
		}
		c.Path, c.Args = c.Args[2], c.Args[3:]
	}
	if c.Path != f.manager {
		f.t.Fatal("unexpected command", c)
	}
	if reflect.DeepEqual(c.Args, []string{"--version"}) {
		return []byte("2.1.302 (Claude Code)\n"), nil
	}
	if reflect.DeepEqual(c.Args, []string{"plugin", "marketplace", "update", "fixture-market", "--json"}) {
		f.refreshes++
		if isolated || !contains(c.Env, "GIT_ALLOW_PROTOCOL=https") {
			f.t.Fatal("refresh policy", c)
		}
		if f.scenario == "refresh-file-only" {
			f.write(filepath.Join(f.market, "example/skills/demo/SKILL.md"), "partial marketplace file edit", 0644)
			return nil, errors.New("refresh failed after writing content")
		}
		if f.scenario == "refresh-failed" {
			return nil, errors.New("refresh failed")
		}
		if f.scenario == "drift" {
			f.advance("1.3.0")
		}
		f.git("-c", "protocol.file.allow=always", "fetch", f.remote, "main")
		f.git("reset", "--hard", "FETCH_HEAD")
		f.git("update-ref", "refs/remotes/origin/main", "HEAD")
		if f.scenario == "refresh-partial" {
			return nil, errors.New("refresh partially changed state")
		}
		if f.scenario == "source-drift" {
			f.git("remote", "set-url", "origin", "https://github.com/other/market.git")
		}
		if f.scenario == "archive" {
			if e := os.RemoveAll(filepath.Join(f.market, ".git")); e != nil {
				f.t.Fatal(e)
			}
			f.write(filepath.Join(f.market, ".gcs-sha"), f.tip, 0644)
		}
		return []byte(`{"command":"marketplace-update","outcome":"ok","marketplace":"fixture-market"}`), nil
	}
	dest := filepath.Join(filepath.Dir(f.installed), "1.2.0")
	if reflect.DeepEqual(c.Args, []string{"plugin", "update", "example@fixture-market", "--scope", "user", "--json"}) {
		f.installs++
		if !isolated {
			f.t.Fatal("plugin update lacks network sandbox")
		}
		snap, e := audit.ReadSkillUpdateSnapshot(filepath.Join(f.market, "example"))
		if e != nil {
			f.t.Fatal(e)
		}
		if _, e := os.Stat(dest); os.IsNotExist(e) {
			if e = writeSkillFiles(dest, snap); e != nil {
				f.t.Fatal(e)
			}
		} else if existing, e := audit.ReadSkillUpdateSnapshot(dest); e != nil || !samePluginFiles(existing, snap) {
			f.t.Fatal("owner received modified target cache")
		}
		f.registry("1.2.0", dest, f.tip)
		return []byte(`{"command":"update","outcome":"ok","pluginId":"example@fixture-market","scope":"user","newVersion":"1.2.0"}`), nil
	}
	if reflect.DeepEqual(c.Args, []string{"plugin", "list", "--json"}) {
		if !isolated {
			f.t.Fatal("readback lacks network sandbox")
		}
		records, e := audit.ReadClaudePluginRegistry(f.home)
		if e != nil {
			f.t.Fatal(e)
		}
		plugins := []nativePlugin{}
		if f.scenario == "precache" {
			for id, rows := range records {
				for _, row := range rows {
					name := strings.Split(id, "@")[0]
					snapshot, e := audit.ReadSkillUpdateSnapshot(filepath.Join(f.market, name))
					if e != nil {
						f.t.Fatal(e)
					}
					v, _, e := pluginContent(snapshot, name)
					if e != nil {
						f.t.Fatal(e)
					}
					target := filepath.Join(filepath.Dir(row.InstallPath), v)
					if v != row.Version {
						if _, e := os.Stat(target); os.IsNotExist(e) {
							if e = writeSkillFiles(target, snapshot); e != nil {
								f.t.Fatal(e)
							}
						}
					}
				}
			}
		}
		for id, rows := range records {
			for _, row := range rows {
				plugins = append(plugins, nativePlugin{ID: id, Version: row.Version, Scope: row.Scope, InstallPath: row.InstallPath, Enabled: true})
			}
		}
		b, _ := json.Marshal(plugins)
		return b, nil
	}
	f.t.Fatal("unexpected owner command", c)
	return nil, nil
}
func (f *remotePluginFixture) planRemote() Plan {
	f.t.Helper()
	s := Selection{Action: "update", All: true}
	var e error
	f.o.ResolvedTargets, e = ResolvePluginTargets(context.Background(), f.r, s, f.o, runFunc(f.run))
	if e != nil {
		f.t.Fatal(e)
	}
	p := f.plan(s)
	if p.Items[0].Status != "ready" || !p.Items[0].PluginUpdate.RefreshMarketplace || p.Items[0].TargetVersion != "1.2.0" {
		f.t.Fatal(p.Items)
	}
	return p
}
func (f *remotePluginFixture) engineRemote() Engine {
	e := f.pluginEngine(runFunc(f.run))
	e.ResolveTargets = func(ctx context.Context, r audit.Report, s Selection) (map[string]ResolvedTarget, error) {
		return ResolvePluginTargets(ctx, r, s, f.o, runFunc(f.run))
	}
	return e
}

func TestPluginRemotePreviewDoesNotRefreshOwner(t *testing.T) {
	f := setupRemotePlugin(t)
	before := f.git("rev-parse", "HEAD")
	p := f.planRemote()
	if f.refreshes != 0 || f.installs != 0 || f.git("rev-parse", "HEAD") != before {
		t.Fatal("preview changed source")
	}
	v := p.Items[0].PluginUpdate
	if v.Commit != f.tip || v.MarketplaceBeforeCommit != before || !v.RemoteVerified || !contains(p.Items[0].AffectedPaths, f.market) {
		t.Fatal(v)
	}
}

func TestPluginRemoteRefreshGuardsAndSideEffects(t *testing.T) {
	for _, scenario := range []string{"success", "archive", "drift", "refresh-failed", "refresh-partial", "source-drift", "refresh-file-only"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupRemotePlugin(t)
			p := f.planRemote()
			f.scenario = scenario
			out, e := Apply(context.Background(), p, p.PlanID, f.engineRemote())
			if e != nil {
				t.Fatal(e)
			}
			r := out.Results[0]
			effect := r.MarketplaceRefresh
			if effect == nil || !effect.Attempted || f.refreshes != 1 {
				t.Fatal(r)
			}
			if scenario == "success" || scenario == "archive" {
				if !effect.StateKnown || r.Status != "updated" || !r.ReloadRequired || f.installs != 1 || !effect.Changed || effect.AfterCommit != p.Items[0].PluginUpdate.Commit {
					t.Fatal(r)
				}
			} else {
				if r.Status != "verification_unknown" || r.Executed || f.installs != 0 || effect.Changed != (scenario != "refresh-failed" && scenario != "refresh-file-only") {
					t.Fatal(r)
				}
				if effect.StateKnown || r.Reason != "plugin_marketplace_refresh_stopped_install" {
					t.Fatal(r)
				}
			}
		})
	}
}

func TestPluginRemoteDriftBeforeApplyDoesNotMutateOwner(t *testing.T) {
	f := setupRemotePlugin(t)
	p := f.planRemote()
	f.advance("1.3.0")
	_, e := Apply(context.Background(), p, p.PlanID, f.engineRemote())
	if e == nil || f.refreshes != 0 || f.installs != 0 {
		t.Fatal(e, f.refreshes, f.installs)
	}
}

func TestPluginArchiveLocalModificationIsNotOverwritten(t *testing.T) {
	f := setupRemotePlugin(t)
	f.git("-c", "protocol.file.allow=always", "fetch", f.remote, "main")
	f.git("reset", "--hard", "FETCH_HEAD")
	if e := os.RemoveAll(filepath.Join(f.market, ".git")); e != nil {
		t.Fatal(e)
	}
	f.write(filepath.Join(f.market, ".gcs-sha"), f.tip, 0644)
	f.write(filepath.Join(f.market, "example/skills/demo/SKILL.md"), "local source edit", 0644)
	targets, e := ResolvePluginTargets(context.Background(), f.r, Selection{Action: "update", All: true}, f.o, runFunc(f.run))
	if e != nil {
		t.Fatal(e)
	}
	for _, target := range targets {
		if target.OriginVerified || !strings.Contains(target.Reason, "modified") {
			t.Fatal(target)
		}
	}
	if f.refreshes != 0 || f.installs != 0 {
		t.Fatal("modified archive updated")
	}
}
