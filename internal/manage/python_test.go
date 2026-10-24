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

func (f *fixture) pythonTool(provider, spec string) (audit.Item, string) {
	f.t.Helper()
	name := "example-tool"
	scope := filepath.Join(f.home, ".local/share/uv/tools", name)
	if provider == "pipx" {
		scope = filepath.Join(f.home, ".local/share/pipx/venvs", name)
	}
	f.writePythonReceipt(provider, scope, spec, "1.0.0")
	path := filepath.Join(scope, "lib/python3.13/site-packages/example_tool-1.0.0.dist-info")
	f.write(filepath.Join(path, "METADATA"), "Name: Example_Tool\nVersion: 1.0.0\nProvides-Extra: feature\n", 0644)
	manager := filepath.Join(f.home, ".local/bin", provider)
	f.write(manager, "fake "+provider, 0755)
	f.o.ManagerPaths[provider] = manager
	i := audit.Item{ID: "old-dist-id", Name: name, Identity: "Example_Tool", Kind: "python-package", Owner: provider, Source: "pypi-public-candidate", Path: path, Version: "1.0.0", Channel: "stable", Health: audit.Result{Status: "ok"}, Evidence: []string{"environment:" + scope}}
	f.r.Items = []audit.Item{i}
	f.o.ResolvedTargets = map[string]ResolvedTarget{i.ID: {Provider: provider, ManagerPath: manager, Scope: scope, OriginVerified: true, Source: "native index configuration digest", Version: "1.1.0"}}
	f.bindPythonPolicy(i, scope, "update")
	return i, scope
}

func (f *fixture) writePythonReceipt(provider, scope, spec, v string) {
	f.t.Helper()
	interpreter := filepath.Join(f.home, "python")
	f.write(interpreter, "controlled python interpreter", 0755)
	f.write(filepath.Join(scope, "pyvenv.cfg"), "home = "+f.home+"\ninclude-system-site-packages = false\n", 0644)
	if _, err := os.Lstat(filepath.Join(scope, "bin/python")); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Join(scope, "bin"), 0755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.Symlink(interpreter, filepath.Join(scope, "bin/python")); err != nil {
			f.t.Fatal(err)
		}
	}
	if provider == "pipx" {
		f.write(filepath.Join(scope, "pipx_metadata.json"), fmt.Sprintf(`{"pipx_metadata_version":"0.5","python_version":"Python 3.13.0","source_interpreter":%q,"venv_args":[],"injected_packages":{},"main_package":{"package":"example-tool","package_or_url":%q,"package_version":%q,"pip_args":[],"suffix":"","include_apps":true,"include_dependencies":false,"apps":[],"app_paths":[]}}`, interpreter, spec, v), 0644)
	} else {
		f.write(filepath.Join(scope, "uv-receipt.toml"), "[tool]\nrequirements = [{ name = \"example-tool\", extras = [\"feature\"] }]\nentrypoints = []\n", 0644)
	}
}

func TestPythonUpgradeReidentifiesNewDistInfoOnlyWithinBoundEnvironment(t *testing.T) {
	for _, provider := range []string{"pipx", "uv"} {
		t.Run(provider, func(t *testing.T) {
			f := setup(t)
			old, scope := f.pythonTool(provider, "example-tool[feature]")
			p := f.plan(Selection{Action: "update", All: true})
			if p.Items[0].Status != "ready" {
				t.Fatal(p.Items)
			}
			e := f.engine(runFunc(func(_ context.Context, c Command) ([]byte, error) {
				if provider == "pipx" {
					if len(c.Args) != 2 || c.Args[0] != "upgrade" || c.Args[1] != "example-tool" {
						t.Fatal("pipx native receipt replaced", c.Args)
					}
					var file string
					for _, v := range c.Env {
						if strings.HasPrefix(v, "PIP_CONSTRAINT=") {
							file = strings.TrimPrefix(v, "PIP_CONSTRAINT=")
						}
					}
					content, err := os.ReadFile(file)
					if err != nil || string(content) != "example-tool==1.1.0\n" {
						t.Fatal("unbound pipx target", string(content), err)
					}
					st, err := os.Stat(file)
					if err != nil || st.Mode().Perm() != 0600 {
						t.Fatal("constraint not private", err)
					}
				} else if !contains(c.Args, "example-tool==1.1.0") || !contains(c.Args, "--no-build") || contains(c.Args, "--only-binary") {
					t.Fatal("uv target/flag incorrect", c.Args)
				}
				if err := os.RemoveAll(old.Path); err != nil {
					t.Fatal(err)
				}
				next := old
				next.ID = "new-dist-id"
				next.Version = "1.1.0"
				next.Name = "Example.Tool"
				next.Identity = "example-tool"
				next.Path = filepath.Join(scope, "lib/python3.13/site-packages/example_tool-1.1.0.dist-info")
				f.write(filepath.Join(next.Path, "METADATA"), "Name: Example.Tool\nVersion: 1.1.0\nProvides-Extra: feature\n", 0644)
				f.writePythonReceipt(provider, scope, "example-tool[feature]", "1.1.0")
				f.r.Items = []audit.Item{next}
				return nil, nil
			}))
			out, err := Apply(context.Background(), p, p.PlanID, e)
			if err != nil || out.Results[0].Status != "updated" || out.Results[0].AfterVersion != "1.1.0" || !out.Results[0].Changed {
				t.Fatalf("%+v %v", out, err)
			}
		})
	}
}

func TestPythonPostcheckRejectsAmbiguousWrongEnvironmentReceiptAndSource(t *testing.T) {
	for _, scenario := range []string{"duplicate", "other-env", "receipt-old", "receipt-extras-changed", "source-changed", "metadata-lies", "metadata-extras-missing", "environment-link", "interpreter-changed"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			old, scope := f.pythonTool("pipx", "example-tool[feature]")
			p := f.plan(Selection{Action: "update", All: true}).Items[0]
			next := old
			next.ID = "new-id"
			next.Version = "1.1.0"
			next.Path = filepath.Join(scope, "lib/python3.13/site-packages/example_tool-1.1.0.dist-info")
			f.write(filepath.Join(next.Path, "METADATA"), "Name: example-tool\nVersion: 1.1.0\nProvides-Extra: feature\n", 0644)
			f.writePythonReceipt("pipx", scope, "example-tool[feature]", "1.1.0")
			items := []audit.Item{next}
			switch scenario {
			case "duplicate":
				items = append(items, old)
			case "other-env":
				other := filepath.Join(f.home, "other-environment")
				next.Path = filepath.Join(other, "lib/python3.13/site-packages/example_tool-1.1.0.dist-info")
				next.Evidence = []string{"environment:" + other}
				f.write(filepath.Join(next.Path, "METADATA"), "Name: example-tool\nVersion: 1.1.0\nProvides-Extra: feature\n", 0644)
				items = []audit.Item{next}
			case "receipt-old":
				f.writePythonReceipt("pipx", scope, "example-tool[feature]", "1.0.0")
			case "receipt-extras-changed":
				f.writePythonReceipt("pipx", scope, "example-tool", "1.1.0")
			case "source-changed":
				next.Source = "direct-url"
				items = []audit.Item{next}
			case "metadata-extras-missing":
				f.write(filepath.Join(next.Path, "METADATA"), "Name: example-tool\nVersion: 1.1.0\n", 0644)
			case "environment-link":
				other := filepath.Join(f.home, "moved-environment")
				if err := os.Rename(scope, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, scope); err != nil {
					t.Fatal(err)
				}
			case "interpreter-changed":
				f.write(filepath.Join(f.home, "python"), "different interpreter", 0755)
			case "metadata-lies":
				f.write(filepath.Join(next.Path, "METADATA"), "Name: example-tool\nVersion: 1.2.0\n", 0644)
			}
			r := ItemResult{}
			verifyResult(&r, p, "update", audit.Report{Items: items}, nil)
			if r.Status == "updated" {
				t.Fatal("unverified Python upgrade accepted", scenario, r)
			}
		})
	}
}

func TestPythonUninstallDoesNotMistakeRenamedMetadataForToolRemoval(t *testing.T) {
	f := setup(t)
	old, scope := f.pythonTool("pipx", "example-tool")
	p := f.plan(Selection{Action: "uninstall", All: true}).Items[0]
	if err := os.RemoveAll(old.Path); err != nil {
		t.Fatal(err)
	}
	next := old
	next.ID = "new-id"
	next.Version = "1.1.0"
	next.Path = filepath.Join(scope, "lib/python3.13/site-packages/example_tool-1.1.0.dist-info")
	f.write(filepath.Join(next.Path, "METADATA"), "Name: example-tool\nVersion: 1.1.0\nProvides-Extra: feature\n", 0644)
	r := ItemResult{}
	verifyResult(&r, p, "uninstall", audit.Report{Items: []audit.Item{next}}, nil)
	if r.Status == "uninstalled" {
		t.Fatal("same tool still installed", r)
	}
	r = ItemResult{}
	verifyResult(&r, p, "uninstall", audit.Report{}, nil)
	if r.Status == "uninstalled" {
		t.Fatal("environment still exists", r)
	}
	if err := os.RemoveAll(scope); err != nil {
		t.Fatal(err)
	}
	r = ItemResult{}
	verifyResult(&r, p, "uninstall", audit.Report{}, nil)
	if r.Status != "uninstalled" {
		t.Fatal("complete tool removal not verified", r)
	}
}

func TestPipxOriginalRangeIsPreserved(t *testing.T) {
	f := setup(t)
	_, _ = f.pythonTool("pipx", "example-tool[feature]>=1,<2")
	p := f.plan(Selection{Action: "update", All: true})
	if p.Items[0].Status != "ready" {
		t.Fatal(p.Items)
	}
}

func TestUVReceiptSourceSettingsRequireUpdateReviewButAllowUninstall(t *testing.T) {
	for _, settings := range []string{"[tool.options]\nindex-url = \"https://private.invalid/simple\"\n", "[tool.options]\nindex = [{ url = \"https://private.invalid/simple\", default = true }]\n"} {
		f := setup(t)
		_, scope := f.pythonTool("uv", "")
		file, err := os.OpenFile(filepath.Join(scope, "uv-receipt.toml"), os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.WriteString(settings)
		file.Close()
		update := f.plan(Selection{Action: "update", All: true})
		if update.Items[0].Status == "ready" || update.Items[0].Reason != "uv_receipt_source_settings_require_review" {
			t.Fatal("receipt source ignored", update.Items)
		}
		f.bindPythonPolicy(f.r.Items[0], scope, "uninstall")
		remove := f.plan(Selection{Action: "uninstall", All: true})
		if remove.Items[0].Status != "ready" {
			t.Fatal("source setting blocked original-manager removal", remove.Items)
		}
	}
}

func TestUVOriginalVersionConstraintsAreManualOnlyForUpdate(t *testing.T) {
	for _, constraint := range []string{`, specifier = "<2"`, `, specifier = ""`, ""} {
		t.Run(constraint, func(t *testing.T) {
			f := setup(t)
			item, scope := f.pythonTool("uv", "")
			f.write(filepath.Join(scope, "uv-receipt.toml"), "[tool]\nrequirements = [{ name = \"example-tool\", extras = [\"feature\"]"+constraint+" }]\nentrypoints = []\n", 0644)
			target := f.o.ResolvedTargets[item.ID]
			target.Version = "3.0.0"
			f.o.ResolvedTargets[item.ID] = target
			f.bindPythonPolicy(item, scope, "update")
			p := f.plan(Selection{Action: "update", All: true})
			bounded := strings.Contains(constraint, "<2")
			if bounded {
				if p.Items[0].Status == "ready" || p.Items[0].Reason != "python_target_outside_constraints" {
					t.Fatal("incompatible native candidate ready", p.Items)
				}
			} else if p.Items[0].Status != "ready" {
				t.Fatal("bare requirement with extras blocked", p.Items)
			}
			p = f.plan(Selection{Action: "uninstall", All: true})
			if p.Items[0].Status != "ready" {
				t.Fatal("version constraint blocked uninstall", p.Items)
			}
		})
	}
}

func TestFinderEnvironmentFindsManagersByBoundPrefixAndKnownLocations(t *testing.T) {
	for _, provider := range []string{"npm", "pipx", "uv"} {
		t.Run(provider, func(t *testing.T) {
			f := setup(t)
			var i audit.Item
			if provider == "npm" {
				i = f.npm("npm", "example")
			} else {
				i, _ = f.pythonTool(provider, "example-tool")
			}
			f.o.ManagerPaths = nil // No PATH executable records or caller override.
			manager := filepath.Join(f.o.BrewRoots[0], "bin", provider)
			if provider == "npm" {
				manager = filepath.Join(f.home, ".npm-global/bin/npm")
			}
			f.write(manager, "fake managed "+provider, 0755)
			binding, err := ResolutionBinding(i, f.r, f.o)
			if err != nil || binding.ManagerPath != manager {
				t.Fatal("Finder-safe explicit manager discovery failed", binding, err)
			}
			if provider == "npm" {
				node := filepath.Join(f.o.BrewRoots[0], "bin/node")
				f.write(node, "fake node", 0755)
				env := ManagerEnvironment(provider, binding, f.o)
				joined := strings.Join(env, "\n")
				if !strings.Contains(joined, filepath.Dir(node)) {
					t.Fatal("Finder manager cannot find its existing Node runtime", env)
				}
			}
			if f.r.Items[0].Active {
				t.Fatal("invented PATH active state")
			}
			if err := os.Chmod(manager, 0777); err != nil {
				t.Fatal(err)
			}
			if provider == "npm" {
				if err := os.Remove(manager); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(filepath.Join(f.home, ".local/bin", provider)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ResolutionBinding(i, f.r, f.o); err == nil {
				t.Fatal("untrusted writable manager accepted")
			}
		})
	}
}

func (f *fixture) bindPythonPolicy(i audit.Item, scope, action string) {
	f.t.Helper()
	target := f.o.ResolvedTargets[i.ID]
	digest, err := fileHash(target.ManagerPath)
	if err != nil {
		f.t.Fatal(err)
	}
	v := "0.13.1"
	if i.Owner == "pipx" {
		v = "1.8.0"
	}
	target.Python, err = ReadPythonPolicy(i, Binding{Scope: scope, ManagerPath: target.ManagerPath, ManagerFingerprint: digest}, f.o, v, action)
	if err != nil {
		f.t.Fatal(err)
	}
	f.o.ResolvedTargets[i.ID] = target
}
