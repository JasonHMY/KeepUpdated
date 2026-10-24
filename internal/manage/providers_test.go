package manage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func TestSpecialLifecycleLaneRequiresSeparatePlan(t *testing.T) {
	f := setup(t)
	i := f.npm("one", "example")
	f.write(filepath.Join(i.Path, "package.json"), `{"name":"example","version":"1.0.0","scripts":{"postinstall":"set up executable"}}`, 0644)
	ordinary := f.plan(Selection{Action: "update", All: true})
	if ordinary.Items[0].Status != "manual" || !ordinary.Items[0].RequiresScripts {
		t.Fatal(ordinary.Items)
	}
	special := f.plan(Selection{Action: "update", All: true, Special: true})
	if special.Items[0].Status != "ready" || !special.Items[0].SpecialApproved || ordinary.PlanID == special.PlanID {
		t.Fatal(special.Items)
	}
	c, err := commandFor(special.Items[0], special.Action, f.o, f.state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(c.Args, " "), "ignore-scripts") {
		t.Fatal("approved lifecycle scripts silently disabled")
	}
}

func TestCurrentNativeCandidateSkipsBeforeScriptWarningAndManualShowsVersion(t *testing.T) {
	f := setup(t)
	i := f.npm("one", "example")
	target := f.o.ResolvedTargets[i.ID]
	target.HasLifecycleScripts = true
	target.Version = i.Version
	f.o.ResolvedTargets[i.ID] = target
	p := f.plan(Selection{Action: "update", All: true})
	if p.Items[0].Status != "skipped" || p.Items[0].Reason != "already_current" || p.Items[0].TargetVersion != i.Version {
		t.Fatal(p.Items)
	}
	target.Version = "1.2.3"
	f.o.ResolvedTargets[i.ID] = target
	p = f.plan(Selection{Action: "update", All: true})
	if p.Items[0].Status != "manual" || p.Items[0].TargetVersion != "1.2.3" {
		t.Fatal("manual candidate version hidden", p.Items)
	}
}

func TestNativeConfiguredRegistryIsNotMigrated(t *testing.T) {
	f := setup(t)
	i := f.npm("a", "example")
	f.r.Items[0].Source = "npm-public-candidate"
	f.o.ResolvedTargets = map[string]ResolvedTarget{i.ID: {Provider: "npm", ManagerPath: f.o.ManagerPaths["npm"], Scope: filepath.Join(f.home, ".npm-global"), OriginVerified: true, Source: "configured registry digest", Version: "1.2.0", Node: f.o.ResolvedTargets[i.ID].Node}}
	p := f.plan(Selection{Action: "update", All: true})
	c, err := commandFor(p.Items[0], p.Action, f.o, f.state)
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range c.Args {
		if strings.Contains(arg, "registry=") || strings.Contains(arg, "config=/dev/null") {
			t.Fatal("owner config replaced", c.Args)
		}
	}
}

func TestUninstallUsesOriginalOwnerWithoutPublicSource(t *testing.T) {
	f := setup(t)
	f.npm("a", "example")
	f.r.Items[0].Source = "npm-public-candidate"
	f.r.Items[0].Latest = ""
	f.r.Items[0].Update.Status = "unknown"
	p := f.plan(Selection{Action: "uninstall", All: true})
	if p.Items[0].Status != "ready" {
		t.Fatal(p.Items)
	}
	c, err := commandFor(p.Items[0], p.Action, f.o, f.state)
	if err != nil || c.Args[0] != "uninstall" {
		t.Fatal(c, err)
	}
}

func TestPostMetadataVersionAloneDoesNotVerifyMissingExecutable(t *testing.T) {
	f := setup(t)
	i := f.npm("one", "example")
	p := f.plan(Selection{Action: "update", All: true})
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
		f.r.Items[0].Version = "1.1.0"
		f.write(filepath.Join(i.Path, "package.json"), `{"name":"example","version":"1.1.0","bin":{"example":"missing.js"}}`, 0644)
		return nil, nil
	}))
	out, err := Apply(context.Background(), p, p.PlanID, e)
	if err != nil || out.Results[0].Status != "verification_unknown" {
		t.Fatal(out, err)
	}
}

func TestNativeUpdateAllowsProvenanceEvidenceGradeToImprove(t *testing.T) {
	f := setup(t)
	i := f.npm("one", "example")
	f.r.Items[0].Source = "npm-public-candidate"
	p := f.plan(Selection{Action: "update", All: true})
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
		f.r.Items[0].Version = "1.1.0"
		f.r.Items[0].Source = "npm-public"
		f.write(filepath.Join(i.Path, "package.json"), `{"name":"example","version":"1.1.0"}`, 0644)
		return nil, nil
	}))
	out, err := Apply(context.Background(), p, p.PlanID, e)
	if err != nil || out.Results[0].Status != "updated" {
		t.Fatal("evidence grade mistaken for source migration", out, err)
	}
}

func (f *fixture) cask() (audit.Item, audit.Item) {
	root := f.o.BrewRoots[0]
	p := filepath.Join(root, "Caskroom/example")
	f.write(filepath.Join(root, "bin/brew"), "fake brew", 0755)
	f.write(filepath.Join(p, ".metadata/INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/cask"}}`, 0644)
	f.write(filepath.Join(p, ".metadata/1.0.0/now/Casks/example.json"), `{"token":"example","version":"1.0.0","artifacts":[{"app":["Example.app"]}]}`, 0644)
	app := filepath.Join(f.home, "Applications/Example.app")
	f.write(filepath.Join(app, "Contents/Info.plist"), "fixture plist", 0644)
	i := audit.Item{ID: "cask", Kind: "cask", Name: "example", Path: p, Owner: "homebrew", Source: "homebrew/cask", Version: "1.0.0", Latest: "1.1.0", Versions: []string{"1.0.0"}, Health: audit.Result{Status: "ok"}, Update: audit.Result{Status: "available"}}
	a := audit.Item{ID: "application", Kind: "application", Name: "Example", Path: app, Identity: "com.example.Example", Version: "1.0.0", Owner: "homebrew", ActivityKnown: true, Related: []string{i.ID}}
	f.r.Items = []audit.Item{i, a}
	f.o.ResolvedTargets = map[string]ResolvedTarget{a.ID: {Provider: "native-app", Scope: a.Path, Source: "fixture closed app process check", OriginVerified: true, Reason: "closed_application_verified"}}
	return i, a
}

func TestCaskCleanupRequiresExplicitSelectionAndUsesTrash(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve", true: "selected_cleanup"}[cleanup], func(t *testing.T) {
			f := setup(t)
			i, a := f.cask()
			cache := filepath.Join(f.home, "Library/Caches", a.Identity)
			f.write(filepath.Join(cache, "state"), "customer data", 0644)
			unrelated := filepath.Join(f.home, "Library/Caches/com.example.Other/state")
			f.write(unrelated, "other product data", 0644)
			p := f.plan(Selection{Action: "uninstall", IDs: []string{i.ID}, Cleanup: cleanup})
			if p.Items[0].Status != "ready" {
				t.Fatal(p.Items)
			}
			if len(p.Items[0].CleanupPaths) != map[bool]int{false: 0, true: 1}[cleanup] {
				t.Fatal(p.Items)
			}
			e := f.engine(runFunc(func(_ context.Context, c Command) ([]byte, error) {
				if strings.Contains(strings.Join(c.Args, " "), "zap") {
					t.Fatal("zap used")
				}
				if err := os.RemoveAll(i.Path); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(a.Path); err != nil {
					t.Fatal(err)
				}
				f.r.Items = nil
				return nil, nil
			}))
			out, err := Apply(context.Background(), p, p.PlanID, e)
			if err != nil || out.Results[0].Status != "uninstalled" {
				t.Fatal(out, err)
			}
			_, err = os.Stat(cache)
			if cleanup && !os.IsNotExist(err) || !cleanup && err != nil {
				t.Fatal("wrong cleanup behavior", err)
			}
			if _, err = os.Stat(unrelated); err != nil {
				t.Fatal("unrelated data touched", err)
			}
			if cleanup {
				entries, err := os.ReadDir(filepath.Join(f.home, ".Trash"))
				if err != nil || len(entries) != 1 {
					t.Fatal("cleanup not recoverable", err)
				}
			}
		})
	}
}

func TestCaskScriptsRunningAppsAndSharedCleanupAreManual(t *testing.T) {
	for _, scenario := range []string{"script", "running", "shared_cleanup", "changed_cleanup"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			i, a := f.cask()
			cleanup := false
			switch scenario {
			case "script":
				f.write(filepath.Join(i.Path, ".metadata/1.0.0/now/Casks/example.json"), `{"token":"example","version":"1.0.0","artifacts":[{"app":["Example.app"]},{"uninstall":[{"quit":"com.example.Example"}]}]}`, 0644)
			case "running":
				f.r.Items[1].Active = true
			case "shared_cleanup":
				cleanup = true
				copy := a
				copy.ID = "duplicate bundle"
				copy.Path = filepath.Join(f.home, "Applications/Other.app")
				f.r.Items = append(f.r.Items, copy)
			case "changed_cleanup":
				cleanup = true
				f.write(filepath.Join(f.home, "Library/Caches", a.Identity, "state"), "before", 0644)
			}
			p := f.plan(Selection{Action: "uninstall", IDs: []string{i.ID}, Cleanup: cleanup})
			if scenario != "changed_cleanup" {
				if p.Items[0].Status == "ready" {
					t.Fatal(p.Items)
				}
				return
			}
			f.write(filepath.Join(f.home, "Library/Caches", a.Identity, "state"), "after", 0644)
			if _, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) { t.Fatal("changed cleanup executed"); return nil, nil }))); err == nil {
				t.Fatal("stale cleanup accepted")
			}
		})
	}
}

func TestNativeApplicationUninstallChecksAllProcessesAndPreservesData(t *testing.T) {
	f := setup(t)
	path := filepath.Join(f.home, "Applications/Standalone.app")
	f.write(filepath.Join(path, "Contents/Info.plist"), "fixture metadata", 0644)
	// Symlinks inside an app bundle are moved as part of the bundle, never followed.
	if err := os.Symlink("Versions/Current", filepath.Join(path, "Contents/framework-link")); err != nil {
		t.Fatal(err)
	}
	i := audit.Item{ID: "app", Kind: "application", Path: path, Identity: "com.example.Standalone", Owner: "user", ActivityKnown: true, Health: audit.Result{Status: "ok"}}
	f.r.Items = []audit.Item{i}
	config := filepath.Join(f.home, "Library/Preferences", i.Identity+".plist")
	f.write(config, "customer config", 0644)
	s := Selection{Action: "uninstall", All: true}
	active, err := ResolveApplicationTargets(context.Background(), f.r, s, f.o, runFunc(func(context.Context, Command) ([]byte, error) {
		return []byte(path + "/Contents/Helpers/Background"), nil
	}))
	if err != nil || len(active) != 0 {
		t.Fatal(active, err)
	}
	closed, err := ResolveApplicationTargets(context.Background(), f.r, s, f.o, runFunc(func(context.Context, Command) ([]byte, error) { return []byte("/usr/bin/other\n"), nil }))
	if err != nil || len(closed) != 1 {
		t.Fatal(closed, err)
	}
	f.o.ResolvedTargets = closed
	p := f.plan(s)
	if p.Items[0].Status != "ready" {
		t.Fatal(p.Items)
	}
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
		t.Fatal("native app invoked package manager")
		return nil, nil
	}))
	e.Snapshot = func(context.Context) (audit.Report, error) {
		r := f.r
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			r.Items = nil
		}
		return r, nil
	}
	out, err := Apply(context.Background(), p, p.PlanID, e)
	if err != nil || out.Results[0].Status != "uninstalled" {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(config); err != nil {
		t.Fatal("default removed customer data", err)
	}
}

func TestMCPRemovesOnlySelectedDefinitionsAndAllowsSameFileBatch(t *testing.T) {
	f := setup(t)
	path := filepath.Join(f.home, ".config/opencode/opencode.jsonc")
	f.write(path, `{"mcp":{"one":{"type":"remote","url":"https://example.invalid/?token=SECRET"},"two":{"type":"local","command":["example"]}},"unrelated":{"keep":true}}`, 0600)
	f.r.Items = []audit.Item{}
	for _, name := range []string{"one", "two"} {
		f.r.Items = append(f.r.Items, audit.Item{ID: name, Name: name, Kind: "mcp-server", Owner: "opencode", Path: url.PathEscape(path) + "#" + url.PathEscape("user-or-explicit-project") + "#" + url.PathEscape(name)})
	}
	p := f.plan(Selection{Action: "uninstall", All: true})
	for _, pi := range p.Items {
		if pi.Status != "ready" {
			t.Fatal(p.Items)
		}
	}
	encoded, _ := json.Marshal(p)
	if strings.Contains(string(encoded), "SECRET") {
		t.Fatal("plan exposed server config")
	}
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) { t.Fatal("MCP server started"); return nil, nil }))
	e.Snapshot = func(context.Context) (audit.Report, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return audit.Report{}, err
		}
		var m map[string]map[string]any
		if err = json.Unmarshal(b, &m); err != nil {
			return audit.Report{}, err
		}
		r := f.r
		r.Items = nil
		for _, i := range f.r.Items {
			if _, ok := m["mcp"][i.Name]; ok {
				r.Items = append(r.Items, i)
			}
		}
		return r, nil
	}
	out, err := Apply(context.Background(), p, p.PlanID, e)
	if err != nil || len(out.Results) != 2 {
		t.Fatal(out, err)
	}
	for _, r := range out.Results {
		if r.Status != "uninstalled" || r.BackupPath == "" {
			t.Fatal(out)
		}
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"unrelated":{"keep":true}`) {
		t.Fatal("unrelated config changed", string(b))
	}
}

func TestMCPUpdatesVerifiedOwnerAndDeduplicatesSharedPackage(t *testing.T) {
	f := setup(t)
	pkg := f.npm("a-package", "example")
	command := filepath.Join(pkg.Path, "serve.js")
	f.write(command, "#!/bin/sh\nexit 99\n", 0755)
	f.write(filepath.Join(pkg.Path, "package.json"), `{"name":"example","version":"1.0.0","bin":{"example":"serve.js"}}`, 0644)
	if err := os.Symlink(command, filepath.Join(f.o.ResolvedTargets[pkg.ID].Node.Bin, "example")); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(f.home, ".config/opencode/opencode.json")
	f.write(config, fmt.Sprintf(`{"mcp":{"Customer MCP":{"type":"local","command":[%q]}}}`, command), 0600)
	server := audit.Item{ID: "b-server", Name: "Customer MCP", Kind: "mcp-server", Owner: "configuration", Path: url.PathEscape(config) + "#mcp#" + url.PathEscape("Customer MCP"), Related: []string{pkg.ID}}
	f.bindMCPServer(&server, config, command)
	f.r.Items = append(f.r.Items, server)
	s := Selection{Action: "update", IDs: []string{server.ID}}
	expanded := ResolutionSelection(f.r, s)
	if !contains(expanded.IDs, pkg.ID) || len(s.IDs) != 1 {
		t.Fatal("resolver did not expand owner independently", expanded, s)
	}
	p := f.plan(s)
	if p.Items[0].ID != server.ID || p.Items[0].Binding.TargetID != pkg.ID || p.Items[0].Status != "ready" {
		t.Fatal(p.Items)
	}
	c, err := commandFor(p.Items[0], p.Action, f.o, f.state)
	if err != nil || !contains(c.Args, "example@1.1.0") || strings.Contains(strings.Join(c.Args, " "), "Customer MCP") {
		t.Fatal("display name used as install target", c, err)
	}
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
		f.r.Items[0].Version = "1.1.0"
		f.write(filepath.Join(pkg.Path, "package.json"), `{"name":"example","version":"1.1.0","bin":{"example":"serve.js"}}`, 0644)
		return nil, nil
	}))
	out, err := Apply(context.Background(), p, p.PlanID, e)
	if err != nil || out.Results[0].Status != "updated" || out.Results[0].ID != server.ID {
		t.Fatal(out, err)
	}
	f.r.Items[0].Version = "1.0.0"
	f.write(filepath.Join(pkg.Path, "package.json"), `{"name":"example","version":"1.0.0","bin":{"example":"serve.js"}}`, 0644)
	all := f.plan(Selection{Action: "update", All: true})
	ready := 0
	for _, pi := range all.Items {
		if pi.Status == "ready" {
			ready++
		}
	}
	if ready != 1 {
		t.Fatal("shared owner would execute twice", all.Items)
	}
	uninstall := f.plan(Selection{Action: "uninstall", IDs: []string{pkg.ID}})
	if uninstall.Items[0].Status != "blocked" || uninstall.Items[0].Reason != "mcp_configuration_still_uses_installation" {
		t.Fatal("shared MCP installation removable without review", uninstall.Items)
	}
}

func (f *fixture) gitSkill() (audit.Item, string) {
	root := filepath.Join(f.home, ".agents/skills/example")
	f.write(filepath.Join(root, "SKILL.md"), "---\nname: example\n---\n", 0644)
	f.write(filepath.Join(root, ".git/HEAD"), "ref: refs/heads/main\n", 0644)
	f.write(filepath.Join(root, ".git/config"), "[core]\n repositoryformatversion = 0\n bare = false\n[remote \"origin\"]\n url = https://github.com/example/skill.git\n fetch = +refs/heads/*:refs/remotes/origin/*\n[branch \"main\"]\n remote = origin\n merge = refs/heads/main\n", 0644)
	i := audit.Item{ID: "skill", Name: "example", Kind: "skill", Owner: ".agents", Source: "git", Path: filepath.Join(root, "SKILL.md"), Evidence: []string{"git-worktree-clean"}}
	f.r.Items = []audit.Item{i}
	return i, root
}

func TestGitSkillPreviewDoesNotFetchAndApplyPinsExactCommit(t *testing.T) {
	f := setup(t)
	_, root := f.gitSkill()
	head, target := strings.Repeat("a", 40), strings.Repeat("b", 40)
	queries := []string{}
	resolver := runFunc(func(_ context.Context, c Command) ([]byte, error) {
		line := strings.Join(c.Args, " ")
		queries = append(queries, line)
		switch {
		case strings.Contains(line, "rev-parse"):
			return []byte(head), nil
		case strings.Contains(line, "ls-remote"):
			return []byte(target + "\trefs/heads/main\n"), nil
		case strings.Contains(line, "status"), strings.Contains(line, "merge-base"):
			return nil, nil
		}
		return nil, errors.New("unexpected query")
	})
	selection := Selection{Action: "update", All: true}
	targets, err := ResolveGitTargets(context.Background(), f.r, selection, f.o, resolver)
	if err != nil || len(targets) != 1 {
		t.Fatal(targets, err)
	}
	for _, q := range queries {
		if strings.Contains(q, "fetch") {
			t.Fatal("preview wrote git objects", q)
		}
	}
	f.o.ResolvedTargets = targets
	p := f.plan(selection)
	if p.Items[0].Status != "ready" {
		t.Fatal(p.Items)
	}
	merged := false
	tracking := head
	commands := []string{}
	runner := runFunc(func(_ context.Context, c Command) ([]byte, error) {
		line := strings.Join(c.Args, " ")
		commands = append(commands, line)
		if c.Path != "/usr/bin/git" || c.Dir != root || !strings.Contains(line, "core.hooksPath=/dev/null") {
			t.Fatal("unsafe git invocation", c)
		}
		switch {
		case strings.Contains(line, "merge --ff-only"):
			if !strings.HasSuffix(line, target) {
				t.Fatal("unpinned merge", line)
			}
			merged = true
			return nil, nil
		case strings.Contains(line, "rev-parse --verify refs/remotes/origin/main"):
			return []byte(tracking), nil
		case strings.Contains(line, "rev-parse --verify HEAD"):
			if merged {
				return []byte(target), nil
			}
			return []byte(head), nil
		case strings.Contains(line, "update-ref"):
			if !contains(c.Args, "--no-deref") || !strings.HasSuffix(line, "refs/remotes/origin/main "+target+" "+head) {
				t.Fatal("tracking not bound to approved origin/ref", c.Args)
			}
			tracking = target
			return nil, nil
		case strings.Contains(line, "rev-parse"):
			return []byte(target), nil
		case strings.Contains(line, "status"), strings.Contains(line, "fetch"), strings.Contains(line, "merge-base"):
			return nil, nil
		}
		return nil, errors.New("unexpected command")
	})
	e := f.engine(runner)
	out, err := Apply(context.Background(), p, p.PlanID, e)
	if err != nil || !merged || out.Results[0].Status != "updated" {
		t.Fatal(out, err, commands)
	}
}

func TestGitSkillRejectsFiltersDirtyStateAndMovedRemote(t *testing.T) {
	for _, scenario := range []string{"filter", "dirty", "remote_moved"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			_, root := f.gitSkill()
			head, target := strings.Repeat("a", 40), strings.Repeat("b", 40)
			if scenario == "filter" {
				file, err := os.OpenFile(filepath.Join(root, ".git/config"), os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = file.WriteString("[filter \"custom\"]\n clean = execute\n")
				file.Close()
			}
			resolver := runFunc(func(_ context.Context, c Command) ([]byte, error) {
				line := strings.Join(c.Args, " ")
				switch {
				case strings.Contains(line, "rev-parse"):
					return []byte(head), nil
				case strings.Contains(line, "ls-remote"):
					return []byte(target + "\trefs/heads/main"), nil
				case strings.Contains(line, "status"):
					if scenario == "dirty" {
						return []byte(" M SKILL.md"), nil
					}
					return nil, nil
				case strings.Contains(line, "merge-base"):
					return nil, nil
				}
				t.Fatal("unexpected", line)
				return nil, nil
			})
			targets, _ := ResolveGitTargets(context.Background(), f.r, Selection{Action: "update", All: true}, f.o, resolver)
			if scenario != "remote_moved" {
				if len(targets) != 0 {
					t.Fatal("unsafe repository eligible", targets)
				}
				return
			}
			f.o.ResolvedTargets = targets
			p := f.plan(Selection{Action: "update", All: true})
			runner := runFunc(func(_ context.Context, c Command) ([]byte, error) {
				line := strings.Join(c.Args, " ")
				if strings.Contains(line, "merge --ff-only") {
					t.Fatal("moved remote merged")
				}
				if strings.Contains(line, "rev-parse") {
					if strings.Contains(line, "FETCH_HEAD") {
						return []byte(strings.Repeat("c", 40)), nil
					}
					return []byte(head), nil
				}
				if strings.Contains(line, "status") {
					return nil, nil
				}
				return nil, nil
			})
			out, err := Apply(context.Background(), p, p.PlanID, f.engine(runner))
			if err != nil || out.Results[0].Status != "failed" {
				t.Fatal(out, err)
			}
		})
	}
}
