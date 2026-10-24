package manage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func TestCustomPythonRootsKeepReceiptAndConstraintGuards(t *testing.T) {
	for _, owner := range []string{"pipx", "uv"} {
		for _, scenario := range []string{"valid", "unconfigured", "bad-receipt", "pinned", "native-pin", "source-override"} {
			t.Run(owner+"/"+scenario, func(t *testing.T) {
				f := setup(t)
				i, oldScope := f.pythonTool(owner, "example-tool")
				root := filepath.Join(f.home, "custom", owner)
				if owner == "pipx" {
					root = filepath.Join(root, "venvs")
				}
				scope := filepath.Join(root, "example-tool")
				if err := os.MkdirAll(root, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(oldScope, scope); err != nil {
					t.Fatal(err)
				}
				i.Path = strings.Replace(i.Path, oldScope, scope, 1)
				i.Evidence = []string{"environment:" + scope}
				f.r.Items = []audit.Item{i}
				target := f.o.ResolvedTargets[i.ID]
				target.Scope = scope
				f.o.ResolvedTargets[i.ID] = target
				f.o.ManagerRoots = map[string]string{owner: root}
				f.o.ManagerBins = map[string]string{owner: filepath.Join(f.home, "custom-bin")}
				f.bindPythonPolicy(i, scope, "update")
				switch scenario {
				case "unconfigured":
					f.o.ManagerRoots = nil
				case "bad-receipt":
					name := "uv-receipt.toml"
					if owner == "pipx" {
						name = "pipx_metadata.json"
					}
					f.write(filepath.Join(scope, name), "invalid", 0644)
				case "pinned":
					if owner == "pipx" {
						f.writePythonReceipt(owner, scope, "example-tool==1.0.0", "1.0.0")
					} else {
						f.write(filepath.Join(scope, "uv-receipt.toml"), "[tool]\nrequirements = [{ name = \"example-tool\", specifier = \"==1.0.0\" }]\n", 0644)
					}
				case "native-pin":
					if owner == "pipx" {
						f.write(filepath.Join(scope, "pipx_metadata.json"), `{"main_package":{"package":"example-tool","package_or_url":"example-tool","package_version":"1.0.0","pinned":true,"pip_args":[]}}`, 0644)
					} else {
						f.write(filepath.Join(scope, "uv-receipt.toml"), `[tool]
requirements = [{ name = "example-tool", specifier = "==1.0.0" }]
`, 0644)
					}
				case "source-override":
					if owner == "pipx" {
						f.write(filepath.Join(scope, "pipx_metadata.json"), `{"main_package":{"package":"example-tool","package_or_url":"example-tool","package_version":"1.0.0","pip_args":["--index-url","https://private.invalid"]}}`, 0644)
					} else {
						f.write(filepath.Join(scope, "uv-receipt.toml"), "[tool]\nrequirements = [{ name = \"example-tool\" }]\n[tool.options]\nindex-url = \"https://private.invalid\"\n", 0644)
					}
				}
				p := f.plan(Selection{Action: "update", All: true}).Items[0]
				if (p.Status == "ready") != (scenario == "valid") {
					t.Fatalf("%+v", p)
				}
				if scenario == "valid" {
					c, err := commandFor(p, "update", f.o, f.state)
					if err != nil {
						t.Fatal(err)
					}
					key := "UV_TOOL_DIR=" + root
					bin := "UV_TOOL_BIN_DIR="
					if owner == "pipx" {
						key = "PIPX_HOME=" + filepath.Dir(root)
						bin = "PIPX_BIN_DIR="
					}
					if !contains(c.Env, key) || !contains(c.Env, bin+f.o.ManagerBins[owner]) {
						t.Fatal(c.Env)
					}
				}
			})
		}
	}
}

func TestPnpmCommandsUseConfigRootRatherThanVersionedInstallRoot(t *testing.T) {
	f := setup(t)
	scope := filepath.Join(f.home, "Library/pnpm/global/5")
	p := PlanItem{Status: "ready", Name: "example", Provider: "pnpm", TargetVersion: "2.0.0", Binding: Binding{Scope: scope, ManagerPath: filepath.Join(f.home, "bin/pnpm"), NodeVirtualStore: ".pnpm", ResolvedTarget: &ResolvedTarget{Node: &NodePolicy{VirtualStore: ".pnpm"}}}}
	for _, action := range []string{"update", "uninstall"} {
		c, err := commandFor(p, action, f.o, f.state)
		if err != nil || (action == "uninstall" && !contains(c.Args, "--config.global-dir="+filepath.Dir(scope))) || (action == "update" && (!contains(c.Args, "--dir") || !contains(c.Args, scope) || !contains(c.Args, "--no-save") || !contains(c.Args, "--config.virtual-store-dir=.pnpm"))) || contains(c.Args, "--global-dir") || !contains(c.Env, "PNPM_HOME="+filepath.Join(f.home, "Library/pnpm")) {
			t.Fatal(c, err)
		}
		var path string
		for _, v := range c.Env {
			if strings.HasPrefix(v, "PATH=") {
				path = v
			}
		}
		if !strings.Contains(path, filepath.Join(f.home, "Library/pnpm")) {
			t.Fatal(path)
		}
	}
}

func TestPnpmVirtualStoreLayoutsRejectExternalLinksAndVerifyAfterUpdate(t *testing.T) {
	for _, layout := range []string{".pnpm", "node_modules/.pnpm", "outside", "wrong-version"} {
		t.Run(layout, func(t *testing.T) {
			f := setup(t)
			scope := filepath.Join(f.home, "Library/pnpm/global/5")
			i := audit.Item{ID: "pnpm", Name: "example", Identity: "example", Owner: "pnpm", Kind: "node-package", Version: "1.0.0", Path: filepath.Join(scope, "node_modules/example"), Health: audit.Result{Status: "ok"}}
			target := filepath.Join(scope, layout, "example@1.0.0/node_modules/example")
			if layout == "outside" {
				target = filepath.Join(f.home, "outside/example")
			}
			if layout == "wrong-version" {
				target = filepath.Join(scope, ".pnpm/example@9.0.0/node_modules/example")
			}
			f.write(filepath.Join(target, "package.json"), `{"name":"example","version":"1.0.0"}`, 0644)
			if err := os.MkdirAll(filepath.Dir(i.Path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, i.Path); err != nil {
				t.Fatal(err)
			}
			valid := layout == ".pnpm" || layout == "node_modules/.pnpm"
			if pnpmPackagePath(i, scope) != valid {
				t.Fatal("wrong layout decision")
			}
			p := PlanItem{ID: i.ID, Provider: "pnpm", FromVersion: "0.9.0", TargetVersion: "1.0.0", Binding: Binding{TargetID: i.ID, Path: i.Path, Scope: scope, Identity: i.Identity, Owner: i.Owner}}
			f.write(filepath.Join(scope, "package.json"), `{"dependencies":{"example":"^0.9.0"}}`, 0644)
			selector, err := audit.ReadNodeSelector(i.Owner, i.Name, i.Path, scope)
			if err != nil {
				t.Fatal(err)
			}
			p.Binding.ResolvedTarget = &ResolvedTarget{Node: &NodePolicy{Selection: selector, VirtualStore: layout}}
			relative := "../.pnpm"
			if layout == "node_modules/.pnpm" {
				relative = ".pnpm"
			}
			f.write(filepath.Join(scope, "node_modules/.modules.yaml"), "layoutVersion: 5\nnodeLinker: isolated\npackageManager: pnpm@10.18.3\nvirtualStoreDir: "+relative+"\n", 0644)
			result := ItemResult{}
			verifyResult(&result, p, "update", audit.Report{Items: []audit.Item{i}}, nil)
			if (result.Status == "updated") != valid {
				t.Fatal(result)
			}
			p.FromVersion = "1.0.0"
			p.TargetVersion = "2.0.0"
			verifyResult(&result, p, "update", audit.Report{Items: []audit.Item{i}}, nil)
			if result.Status != "verification_unknown" {
				t.Fatal("exit zero without update reported success", result)
			}
		})
	}
}

func TestChangedManagerBinRejectsApprovedUpdateAndUninstallBeforeExecution(t *testing.T) {
	for _, action := range []string{"update", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			f := setup(t)
			f.pythonTool("pipx", "example-tool")
			f.o.ManagerBins = map[string]string{"pipx": filepath.Join(f.home, "first-bin")}
			f.bindPythonPolicy(f.r.Items[0], pythonEnvironment(f.r.Items[0]), "update")
			p := f.plan(Selection{Action: action, All: true})
			if p.Items[0].Status != "ready" || p.Items[0].Binding.ManagerEnvFingerprint == "" {
				t.Fatal(p.Items)
			}
			f.o.ManagerBins = map[string]string{"pipx": filepath.Join(f.home, "second-bin")}
			called := false
			e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) { called = true; return nil, nil }))
			if _, err := Apply(context.Background(), p, p.PlanID, e); err == nil || called {
				t.Fatal("changed executable scope was accepted", err)
			}
		})
	}
}

func TestConfiguredManagerBinIsDiscoveredWithoutPATHOrManagerPaths(t *testing.T) {
	for _, owner := range []string{"pnpm", "bun", "pipx", "uv"} {
		t.Run(owner, func(t *testing.T) {
			f := setup(t)
			bin := filepath.Join(f.home, "custom-bin")
			path := filepath.Join(bin, owner)
			f.write(path, "#!/bin/sh\nexit 0\n", 0755)
			f.o.ManagerPaths = nil
			f.o.ManagerBins = map[string]string{owner: bin}
			if got := findManager(owner, audit.Report{}, f.o, filepath.Join(f.home, "custom-root")); got != path {
				t.Fatalf("got %q", got)
			}
			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
			if got := findManager(owner, audit.Report{}, f.o, filepath.Join(f.home, "custom-root")); got == path {
				t.Fatal("accepted non-executable manager")
			}
		})
	}
}
