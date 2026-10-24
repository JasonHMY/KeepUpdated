package manage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func TestGlobalManagersBindScopeUseExactNamesAndNeverUseShell(t *testing.T) {
	for _, provider := range []string{"pnpm", "bun", "pipx", "uv"} {
		for _, action := range []string{"update", "uninstall"} {
			t.Run(provider+"/"+action, func(t *testing.T) {
				f := setup(t)
				manager := filepath.Join(f.home, ".local/bin", provider)
				f.write(manager, "fake "+provider, 0755)
				f.o.ManagerPaths[provider] = manager
				i := audit.Item{ID: "tool", Name: "example", Identity: "example", Owner: provider, Version: "1.0.0", Latest: "1.1.0", Health: audit.Result{Status: "ok"}, Update: audit.Result{Status: "available"}}
				var scope string
				switch provider {
				case "pnpm", "bun":
					i.Kind = "node-package"
					i.Source = "npm-public-candidate"
					i.Channel = "latest"
					if provider == "pnpm" {
						scope = filepath.Join(f.home, "Library/pnpm/global/5")
					} else {
						scope = filepath.Join(f.home, ".bun/install/global")
					}
					i.Path = filepath.Join(scope, "node_modules/example")
					f.write(filepath.Join(i.Path, "package.json"), `{"name":"example","version":"1.0.0","_resolved":"https://registry.npmjs.org/example/-/example-1.0.0.tgz"}`, 0644)
					f.write(filepath.Join(scope, "package.json"), `{"dependencies":{"example":"^1.0.0"}}`, 0644)
				case "pipx", "uv":
					i.Kind = "python-package"
					i.Source = "pypi-public-candidate"
					i.Channel = "stable"
					if provider == "pipx" {
						scope = filepath.Join(f.home, ".local/share/pipx/venvs/example")
						f.write(filepath.Join(scope, "pipx_metadata.json"), `{"main_package":{"package":"example","package_or_url":"example[feature]","package_version":"1.0.0","pip_args":[],"suffix":""}}`, 0644)
					} else {
						scope = filepath.Join(f.home, ".local/share/uv/tools/example")
						f.write(filepath.Join(scope, "uv-receipt.toml"), "[tool]\nrequirements = [{ name = \"example\", extras = [\"feature\"] }]\nentrypoints = []\n", 0644)
					}
					i.Path = filepath.Join(scope, "lib/python3.13/site-packages/example-1.0.0.dist-info")
					i.Evidence = []string{"environment:" + scope}
					f.write(filepath.Join(i.Path, "METADATA"), "Name: example\nVersion: 1.0.0\nProvides-Extra: feature\n", 0644)
				}
				if i.Kind == "python-package" {
					f.writePythonReceipt(provider, scope, "example-tool[feature]", "1.0.0")
					filename := "uv-receipt.toml"
					if provider == "pipx" {
						filename = "pipx_metadata.json"
					}
					data, err := os.ReadFile(filepath.Join(scope, filename))
					if err != nil {
						t.Fatal(err)
					}
					f.write(filepath.Join(scope, filename), strings.ReplaceAll(string(data), "example-tool", "example"), 0644)
				}
				f.r.Items = []audit.Item{i}
				f.o.ResolvedTargets = map[string]ResolvedTarget{i.ID: {Version: "1.1.0", Source: "native-config-digest", Provider: provider, ManagerPath: manager, Scope: scope, OriginVerified: true}}
				if i.Kind == "python-package" {
					f.bindPythonPolicy(i, scope, action)
				}
				if provider == "pnpm" {
					f.pnpmStore(i, scope, ".pnpm")
				}
				if provider == "pnpm" || provider == "bun" {
					target := f.o.ResolvedTargets[i.ID]
					v := "1.4.3"
					if provider == "pnpm" {
						v = "10.18.3"
					}
					target.Node = f.nodePolicy(i, scope, manager, v)
					if provider == "pnpm" {
						target.Node.VirtualStore = ".pnpm"
					}
					f.o.ResolvedTargets[i.ID] = target
				}
				p := f.plan(Selection{Action: action, All: true})
				if p.Items[0].Status != "ready" {
					t.Fatal(p.Items)
				}
				c, err := commandFor(p.Items[0], action, f.o, f.state)
				if err != nil {
					t.Fatal(err)
				}
				expectedDir := f.home
				if provider == "bun" {
					expectedDir = scope
				}
				if c.Path != manager || c.Dir != expectedDir || strings.Contains(strings.Join(c.Args, " "), "sudo") || strings.Contains(strings.Join(c.Args, " "), "sh -c") {
					t.Fatal("unsafe manager invocation", c)
				}
				if action == "update" && (provider == "pnpm" || provider == "bun") && !contains(c.Args, "example@1.1.0") {
					t.Fatal("unbound version", c.Args)
				}
				if action == "uninstall" && !contains(c.Args, "example") {
					t.Fatal("unbound removal", c.Args)
				}
				if strings.Contains(strings.Join(c.Args, " "), "https://pypi.org") || strings.Contains(strings.Join(c.Args, " "), "registry.npmjs.org") {
					t.Fatal("native source replaced", c.Args)
				}
				if provider == "pipx" && action == "update" && (!contains(c.Env, "PIP_ONLY_BINARY=:all:") || !contains(c.Env, "PIP_CONSTRAINT="+pythonConstraintPath(f.state, p.Items[0]))) {
					t.Fatal("source builds enabled", c.Args)
				}
				if provider == "uv" && action == "update" && (!contains(c.Args, "--no-build") || !contains(c.Args, "example==1.1.0")) {
					t.Fatal("source builds enabled", c.Args)
				}
				if provider == "pnpm" && !contains(c.Args, PnpmDisableRuntime) {
					t.Fatal("implicit runtime provisioning enabled", c.Args)
				}
				if provider == "pnpm" && !contains(c.Env, "COREPACK_ENABLE_NETWORK=0") {
					t.Fatal("implicit manager downloads enabled", c.Env)
				}
				if provider == "bun" && !contains(c.Env, "BUN_INSTALL_GLOBAL_DIR="+scope) {
					t.Fatal("global root unbound", c.Env)
				}
			})
		}
	}
}

func TestPythonDependenciesAreNotIndependentToolTargets(t *testing.T) {
	f := setup(t)
	env := filepath.Join(f.home, ".local/share/pipx/venvs/main")
	f.write(filepath.Join(env, "pipx_metadata.json"), `{"main_package":{"package":"main","package_or_url":"main","package_version":"1.0.0","pip_args":[],"suffix":""}}`, 0644)
	p := filepath.Join(env, "lib/python3.13/site-packages/dependency-1.0.0.dist-info")
	f.write(filepath.Join(p, "METADATA"), "Name: dependency\nVersion: 1.0.0", 0644)
	f.r.Items = []audit.Item{{ID: "dependency", Name: "dependency", Identity: "dependency", Kind: "python-package", Owner: "pipx", Path: p, Source: "pypi-public-candidate", Version: "1.0.0", Evidence: []string{"environment:" + env}}}
	plan := f.plan(Selection{Action: "uninstall", All: true})
	if plan.Items[0].Status == "ready" {
		t.Fatal("dependency independently removed", plan.Items)
	}
}

func TestBrewReverseDependenciesAndSharedRuntimesAreProtected(t *testing.T) {
	for _, name := range []string{"example", "node"} {
		f := setup(t)
		root := f.o.BrewRoots[0]
		path := filepath.Join(root, "Cellar", name)
		f.write(filepath.Join(root, "bin/brew"), "fake brew", 0755)
		f.write(filepath.Join(path, "1.0.0/INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"}}`, 0644)
		i := audit.Item{ID: name, Name: name, Kind: "formula", Owner: "homebrew", Path: path, Source: "homebrew/core", Version: "1.0.0", ActiveVersion: "1.0.0", Latest: "1.1.0", Health: audit.Result{Status: "ok"}, Update: audit.Result{Status: "available"}}
		f.r.Items = []audit.Item{i}
		if name == "example" {
			f.r.Items = append(f.r.Items, audit.Item{ID: "consumer", Name: "consumer", Kind: "formula", Path: filepath.Join(root, "Cellar/consumer"), Dependencies: []audit.Dependency{{Name: name}}})
		}
		p := f.plan(Selection{Action: "uninstall", IDs: []string{name}})
		if p.Items[0].Status == "ready" {
			t.Fatal("shared package removal ready", p.Items)
		}
		if name == "example" {
			f.r.Items = f.r.Items[:1]
			f.o.ResolvedTargets = map[string]ResolvedTarget{i.ID: {Provider: "homebrew", ManagerPath: filepath.Join(root, "bin/brew"), Scope: root, OriginVerified: true, Source: "fixture brew metadata", Version: i.Latest}}
			p = f.plan(Selection{Action: "update", All: true})
			c, err := commandFor(p.Items[0], p.Action, f.o, f.state)
			if err != nil {
				t.Fatal(err)
			}
			if !contains(c.Env, "HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK=1") || !contains(c.Env, "HOMEBREW_NO_AUTOREMOVE=1") {
				t.Fatal("unlisted dependent rebuild enabled", c.Env)
			}
		}
	}
}

func TestUninstallNeedsAbsenceAndReportsManagerFailureSeparately(t *testing.T) {
	f := setup(t)
	i := f.npm("one", "example")
	p := f.plan(Selection{Action: "uninstall", All: true})
	e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) { f.r.Items = nil; return nil, nil }))
	out, err := Apply(context.Background(), p, p.PlanID, e)
	if err != nil || out.Results[0].Status != "verification_unknown" {
		t.Fatal("inventory omission treated as deletion", out, err)
	}
	// A new preview gets a new durable attempt identity without changing the installation.
	f.r.Items = []audit.Item{i}
	f.o.Now = f.o.Now.Add(1)
	p = f.plan(Selection{Action: "uninstall", All: true})
	e = f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
		if err := os.RemoveAll(i.Path); err != nil {
			t.Fatal(err)
		}
		f.r.Items = nil
		return nil, fmt.Errorf("manager exited nonzero after removal")
	}))
	out, err = Apply(context.Background(), p, p.PlanID, e)
	if err != nil || out.Results[0].Status != "uninstalled" || out.Results[0].CommandSucceeded {
		t.Fatal("execution and verification conflated", out, err)
	}
}

func TestObservedChangesStayInsideApprovedImpactScope(t *testing.T) {
	p := PlanItem{ID: "selected", AffectedIDs: []string{"dependent"}, AffectedPaths: []string{"/synthetic/Cellar/new-dependency"}, Binding: Binding{TargetID: "selected"}}
	before := audit.Report{Items: []audit.Item{{ID: "selected", Version: "1"}, {ID: "dependent", Version: "1"}, {ID: "unrelated", Path: "/elsewhere/Other.app", Version: "1"}}}
	after := audit.Report{Items: []audit.Item{{ID: "selected", Version: "2"}, {ID: "dependent", Version: "2"}, {ID: "unrelated", Path: "/elsewhere/Other.app", Version: "2"}, {ID: "new", Path: "/synthetic/Cellar/new-dependency", Version: "1"}, {ID: "new-unrelated", Path: "/other/new", Version: "1"}}}
	changes := observedChanges(before, after, p)
	if len(changes) != 2 || changes[0].ID != "dependent" || changes[1].ID != "new" {
		t.Fatal("unrelated concurrent activity attributed to operation", changes)
	}
}

func TestCandidateBrewDependencyClosureAppearsInPreview(t *testing.T) {
	f := setup(t)
	root := f.o.BrewRoots[0]
	path := filepath.Join(root, "Cellar/example")
	f.write(filepath.Join(root, "bin/brew"), "fake brew", 0755)
	f.write(filepath.Join(path, "1.0.0/INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"}}`, 0644)
	i := audit.Item{ID: "primary", Name: "example", Kind: "formula", Owner: "homebrew", Path: path, Source: "homebrew/core", Version: "1.0.0", ActiveVersion: "1.0.0", Health: audit.Result{Status: "ok"}}
	f.r.Items = []audit.Item{i, {ID: "installed-dep", Name: "existing", Kind: "formula", Path: filepath.Join(root, "Cellar/existing")}}
	f.o.ResolvedTargets = map[string]ResolvedTarget{i.ID: {Provider: "homebrew", ManagerPath: filepath.Join(root, "bin/brew"), Scope: root, OriginVerified: true, Source: "resolved dependency closure digest", Version: "1.1.0", AffectedPackages: []string{"existing", "new-dep"}}}
	p := f.plan(Selection{Action: "update", IDs: []string{i.ID}})
	if p.Items[0].Status != "ready" || !contains(p.Items[0].AffectedIDs, "installed-dep") || !contains(p.Items[0].AffectedPaths, filepath.Join(root, "Cellar/new-dep")) {
		t.Fatal("candidate dependency scope hidden", p.Items)
	}
}
