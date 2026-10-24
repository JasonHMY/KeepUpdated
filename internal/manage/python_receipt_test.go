package manage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func (f *fixture) editPipx(scope string, edit func(map[string]any, map[string]any)) {
	f.t.Helper()
	path := filepath.Join(scope, "pipx_metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(data, &m); err != nil {
		f.t.Fatal(err)
	}
	edit(m, m["main_package"].(map[string]any))
	data, err = json.Marshal(m)
	if err != nil {
		f.t.Fatal(err)
	}
	f.write(path, string(data), 0644)
}
func (f *fixture) pythonEntry(i audit.Item, scope string) string {
	f.t.Helper()
	name := "example-tool"
	script := filepath.Join(scope, "bin", name)
	payload := "#!" + filepath.Join(scope, "bin/python") + "\n# " + i.Version + "\n"
	f.write(script, payload, 0755)
	f.write(filepath.Join(i.Path, "entry_points.txt"), "[console_scripts]\nexample-tool = example:main\n", 0644)
	sum := sha256.Sum256([]byte(payload))
	f.write(filepath.Join(i.Path, "RECORD"), fmt.Sprintf("../../../bin/example-tool,sha256=%s,%d\n", base64.RawURLEncoding.EncodeToString(sum[:]), len(payload)), 0644)
	exposed := filepath.Join(f.home, ".local/bin", name)
	if err := os.MkdirAll(filepath.Dir(exposed), 0755); err != nil {
		f.t.Fatal(err)
	}
	if _, err := os.Lstat(exposed); os.IsNotExist(err) {
		if err := os.Symlink(script, exposed); err != nil {
			f.t.Fatal(err)
		}
	}
	if i.Owner == "pipx" {
		f.editPipx(scope, func(m, main map[string]any) {
			main["apps"] = []string{name}
			main["app_paths"] = []map[string]string{{"__type__": "Path", "__Path__": script}}
		})
	} else {
		path := filepath.Join(scope, "uv-receipt.toml")
		data, err := os.ReadFile(path)
		if err != nil {
			f.t.Fatal(err)
		}
		f.write(path, strings.Replace(string(data), "entrypoints = []", fmt.Sprintf("entrypoints = [\n { name = %q, install-path = %q, from = %q },\n]", name, exposed, i.Identity), 1), 0644)
	}
	return exposed
}

func TestPythonReceiptRejectsUnknownSemanticsBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		edit         func(map[string]any, map[string]any)
	}{
		{"schema", "python_receipt_schema_unsupported", func(m, p map[string]any) { m["pipx_metadata_version"] = "99" }},
		{"backend", "python_backend_unsupported", func(m, p map[string]any) {
			m["pipx_metadata_version"] = "0.12"
			m["backend"] = "uv"
			m["exposure_enabled"] = true
		}},
		{"lock", "python_locked_tool_requires_review", func(m, p map[string]any) {
			p["lock_file"] = map[string]string{"__type__": "Path", "__Path__": "/synthetic/requirements.lock"}
		}},
		{"injected", "python_injected_tools_require_review", func(m, p map[string]any) {
			m["injected_packages"] = map[string]any{"other": map[string]string{"package": "other"}}
		}},
		{"exposure", "python_entrypoints_require_review", func(m, p map[string]any) { p["include_dependencies"] = true }},
		{"unknown", "python_receipt_schema_unsupported", func(m, p map[string]any) { p["future_policy"] = true }},
		{"pinned", "owner_version_pinned", func(m, p map[string]any) { p["pinned"] = true }},
		{"missing interpreter", "python_interpreter_unverified", func(m, p map[string]any) { delete(m, "source_interpreter") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			_, scope := f.pythonTool("pipx", "example-tool[feature]")
			f.editPipx(scope, tc.edit)
			p := f.plan(Selection{Action: "update", All: true}).Items[0]
			if p.Status != "manual" || p.Reason != tc.reason {
				t.Fatal(p)
			}
		})
	}
	for _, tail := range []string{
		"constraints = [{ name = \"example-tool\", specifier = \"<1.1\" }]\n",
		"overrides = [{ name = \"dependency\", specifier = \"==2\" }]\n",
		"excludes = [\"dependency\"]\n",
		"build-constraint-dependencies = [\"setuptools<70\"]\n",
		"unknown = true\n", "requirements = [{ name = \"other\" }]\n",
		"[tool.lock]\nversion = \"1\"\n",
	} {
		t.Run("uv/"+tail, func(t *testing.T) {
			f := setup(t)
			_, scope := f.pythonTool("uv", "")
			path := filepath.Join(scope, "uv-receipt.toml")
			data, _ := os.ReadFile(path)
			f.write(path, string(data)+tail, 0644)
			p := f.plan(Selection{Action: "update", All: true}).Items[0]
			if p.Status != "manual" {
				t.Fatal(p)
			}
		})
	}
}

func TestPythonPolicyDriftBlocksUpdateAndUninstallBeforeRunner(t *testing.T) {
	for _, owner := range []string{"pipx", "uv"} {
		for _, action := range []string{"update", "uninstall"} {
			for _, change := range []string{"receipt", "interpreter", "interpreter-link", "venv-config", "manager", "entry-link", "entry-content", "index", "injection"} {
				t.Run(owner+"/"+action+"/"+change, func(t *testing.T) {
					f := setup(t)
					i, scope := f.pythonTool(owner, "example-tool[feature]")
					exposed := f.pythonEntry(i, scope)
					f.bindPythonPolicy(i, scope, action)
					p := f.plan(Selection{Action: action, All: true})
					if p.Items[0].Status != "ready" {
						t.Fatal(p.Items)
					}
					switch change {
					case "receipt":
						if owner == "pipx" {
							f.editPipx(scope, func(m, p map[string]any) { p["package_or_url"] = "example-tool[other]" })
						} else {
							path := filepath.Join(scope, "uv-receipt.toml")
							data, _ := os.ReadFile(path)
							f.write(path, strings.Replace(string(data), "feature", "other", 1), 0644)
						}
					case "interpreter":
						f.write(filepath.Join(f.home, "python"), "changed interpreter", 0755)
					case "interpreter-link":
						path := filepath.Join(f.home, "other-python")
						f.write(path, "controlled python interpreter", 0755)
						if err := os.Remove(filepath.Join(scope, "bin/python")); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(path, filepath.Join(scope, "bin/python")); err != nil {
							t.Fatal(err)
						}
					case "venv-config":
						f.write(filepath.Join(scope, "pyvenv.cfg"), "home = /other\n", 0644)
					case "manager":
						f.write(f.o.ManagerPaths[owner], "new manager binary", 0755)
					case "entry-link":
						if err := os.Remove(exposed); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(filepath.Join(scope, "bin/python"), exposed); err != nil {
							t.Fatal(err)
						}
					case "entry-content":
						f.write(filepath.Join(scope, "bin/example-tool"), "changed script", 0755)
					case "index":
						if owner == "pipx" {
							f.editPipx(scope, func(m, p map[string]any) { p["pip_args"] = []string{"--index-url", "https://private.invalid"} })
						} else {
							path := filepath.Join(scope, "uv-receipt.toml")
							data, _ := os.ReadFile(path)
							f.write(path, string(data)+"[tool.options]\nindex-url = \"https://private.invalid\"\n", 0644)
						}
					case "injection":
						if owner == "pipx" {
							f.editPipx(scope, func(m, p map[string]any) { m["injected_packages"] = map[string]any{"other": map[string]any{}} })
						} else {
							path := filepath.Join(scope, "uv-receipt.toml")
							data, _ := os.ReadFile(path)
							f.write(path, strings.Replace(string(data), "extras = [\"feature\"] }]", "extras = [\"feature\"] }, { name = \"other\" }]", 1), 0644)
						}
					}
					calls := 0
					out, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) { calls++; return nil, nil })))
					if calls != 0 {
						t.Fatal("mutation after drift", calls, out, err)
					}
					if err == nil {
						for _, r := range out.Results {
							if r.Executed || r.Status == "updated" || r.Status == "uninstalled" {
								t.Fatal(out)
							}
						}
					}
				})
			}
		}
	}
}

func TestPythonEntrypointsAndIntentSurviveExactUpgrade(t *testing.T) {
	for _, owner := range []string{"pipx", "uv"} {
		t.Run(owner, func(t *testing.T) {
			f := setup(t)
			old, scope := f.pythonTool(owner, "example-tool[feature]")
			f.pythonEntry(old, scope)
			f.bindPythonPolicy(old, scope, "update")
			p := f.plan(Selection{Action: "update", All: true})
			if p.Items[0].Status != "ready" {
				t.Fatal(p.Items)
			}
			out, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
				if err := os.RemoveAll(old.Path); err != nil {
					t.Fatal(err)
				}
				next := old
				next.ID = "new-dist-info"
				next.Version = "1.1.0"
				next.Path = filepath.Join(scope, "lib/python3.13/site-packages/example_tool-1.1.0.dist-info")
				f.write(filepath.Join(next.Path, "METADATA"), "Name: example-tool\nVersion: 1.1.0\nProvides-Extra: feature\n", 0644)
				f.writePythonReceipt(owner, scope, "example-tool[feature]", "1.1.0")
				f.pythonEntry(next, scope)
				if owner == "uv" {
					path := filepath.Join(scope, "uv-receipt.toml")
					data, _ := os.ReadFile(path)
					f.write(path, string(data)+"[tool.options]\nno-build = true\n", 0644)
				}
				f.r.Items = []audit.Item{next}
				return nil, nil
			})))
			if err != nil || out.Results[0].Status != "updated" {
				t.Fatal(out, err)
			}
		})
	}
}

func TestPythonNoopAndWrongTargetNeverReportUpdated(t *testing.T) {
	for _, owner := range []string{"pipx", "uv"} {
		for _, v := range []string{"1.0.0", "1.2.0"} {
			t.Run(owner+v, func(t *testing.T) {
				f := setup(t)
				i, scope := f.pythonTool(owner, "example-tool")
				p := f.plan(Selection{Action: "update", All: true})
				out, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
					f.r.Items[0].Version = v
					f.write(filepath.Join(i.Path, "METADATA"), "Name: example-tool\nVersion: "+v+"\nProvides-Extra: feature\n", 0644)
					f.writePythonReceipt(owner, scope, "example-tool", v)
					return nil, nil
				})))
				if err != nil || out.Results[0].Status != "verification_unknown" || !out.Results[0].CommandSucceeded {
					t.Fatal(out, err)
				}
			})
		}
	}
}

func TestPythonUninstallRequiresExposedEntryRemoval(t *testing.T) {
	for _, owner := range []string{"pipx", "uv"} {
		t.Run(owner, func(t *testing.T) {
			f := setup(t)
			i, scope := f.pythonTool(owner, "example-tool")
			exposed := f.pythonEntry(i, scope)
			f.bindPythonPolicy(i, scope, "uninstall")
			p := f.plan(Selection{Action: "uninstall", All: true}).Items[0]
			if p.Status != "ready" {
				t.Fatal(p)
			}
			if err := os.RemoveAll(scope); err != nil {
				t.Fatal(err)
			}
			r := ItemResult{}
			verifyResult(&r, p, "uninstall", audit.Report{}, nil)
			if r.Status == "uninstalled" {
				t.Fatal("dangling exposed entry accepted")
			}
			if err := os.Remove(exposed); err != nil {
				t.Fatal(err)
			}
			verifyResult(&r, p, "uninstall", audit.Report{}, nil)
			if r.Status != "uninstalled" {
				t.Fatal(r)
			}
		})
	}
}

func TestPythonManagerCapabilitiesAndPipxExposureTransform(t *testing.T) {
	f := setup(t)
	i, scope := f.pythonTool("pipx", "example-tool[feature]")
	f.pythonEntry(i, scope)
	f.editPipx(scope, func(m, p map[string]any) {
		m["pipx_metadata_version"] = "0.12"
		m["backend"] = "pip"
		m["environment"] = filepath.Base(scope)
		m["exposure_enabled"] = true
		p["lock_file"] = nil
		p["expected_apps"] = []any{}
		p["cooldown_days"] = nil
	})
	script := filepath.Join(scope, "bin/example-tool")
	data, _ := os.ReadFile(script)
	patched := strings.Replace(string(data), "bin/python\n", "bin/python -E\n", 1)
	f.write(script, patched, 0755)
	digest, _ := fileHash(f.o.ManagerPaths[i.Owner])
	b := Binding{Scope: scope, ManagerPath: f.o.ManagerPaths[i.Owner], ManagerFingerprint: digest}
	policy, err := ReadPythonPolicy(i, b, f.o, "1.18.4", "update")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Schema != "0.12" || policy.Backend != "pip" {
		t.Fatal(policy)
	}
	if _, err := ReadPythonPolicy(i, b, f.o, "1.18.5", "update"); err == nil {
		t.Fatal("unknown manager accepted")
	}
	f.write(script, patched+"# unrelated change\n", 0755)
	if _, err := ReadPythonPolicy(i, b, f.o, "1.18.4", "update"); err == nil {
		t.Fatal("script change accepted beyond native -E transform")
	}
}

func TestPythonLockAndLegacyPlanEvidence(t *testing.T) {
	f := setup(t)
	i, scope := f.pythonTool("uv", "")
	p := f.plan(Selection{Action: "update", All: true})
	if p.Items[0].Status != "ready" {
		t.Fatal(p.Items)
	}
	// Resolved lock versions are not user pins; an unhandled lock is a separate result.
	f.write(filepath.Join(scope, "uv.lock"), "version = 1\n[[package]]\nname = \"example-tool\"\nversion = \"1.0.0\"\n", 0644)
	locked := f.plan(Selection{Action: "update", All: true}).Items[0]
	if locked.Status != "manual" || locked.Reason != "python_locked_tool_requires_review" {
		t.Fatal(locked)
	}
	if err := os.Remove(filepath.Join(scope, "uv.lock")); err != nil {
		t.Fatal(err)
	}
	target := f.o.ResolvedTargets[i.ID]
	target.Python = nil
	f.o.ResolvedTargets[i.ID] = target
	old := f.plan(Selection{Action: "update", All: true}).Items[0]
	if old.Status != "manual" || old.Reason != "python_policy_requires_new_preview" {
		t.Fatal(old)
	}
}

func TestUVTransientBuildOptionRestoresOnlyApprovedReceipt(t *testing.T) {
	for _, scenario := range []string{"only-no-build", "already-no-build", "source-change", "requirements-change", "interpreter-change", "duplicate", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			i, scope := f.pythonTool("uv", "")
			path := filepath.Join(scope, "uv-receipt.toml")
			if scenario == "already-no-build" {
				data, _ := os.ReadFile(path)
				f.write(path, string(data)+"[tool.options]\nno-build = true\n", 0644)
				f.bindPythonPolicy(i, scope, "update")
			}
			p := f.plan(Selection{Action: "update", All: true}).Items[0]
			if p.Status != "ready" {
				t.Fatal(p)
			}
			if err := os.MkdirAll(f.state, 0700); err != nil {
				t.Fatal(err)
			}
			original, backup, err := captureUVReceipt(p, f.state, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(backup)
			if err != nil || string(saved) != string(original) {
				t.Fatal("original backup unavailable", err)
			}
			actual := string(original)
			if scenario != "already-no-build" {
				actual += "[tool.options]\nno-build = true\n"
			}
			switch scenario {
			case "source-change":
				actual += "index-url = \"https://other.invalid\"\n"
			case "requirements-change":
				actual = strings.Replace(actual, "feature", "changed", 1)
			case "duplicate":
				actual += "no-build = false\n"
			case "interpreter-change":
				f.write(filepath.Join(f.home, "python"), "changed", 0755)
			}
			f.write(path, actual, 0644)
			if scenario == "symlink" {
				target := filepath.Join(f.home, "other-receipt")
				f.write(target, actual, 0644)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			err = restoreUVInvocationOption(p, original)
			wantSuccess := scenario == "only-no-build" || scenario == "already-no-build"
			if (err == nil) != wantSuccess {
				t.Fatal(scenario, err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			expected := actual
			if wantSuccess {
				expected = string(original)
			}
			if string(after) != expected {
				t.Fatal("unapproved receipt edit")
			}
		})
	}
}

func TestPythonOfficialReceiptSamples(t *testing.T) {
	for _, tc := range []struct{ owner, version, file string }{{"pipx", "1.8.0", "pipx-1.8.0.json"}, {"pipx", "1.18.4", "pipx-1.18.4.json"}, {"uv", "0.13.1", "uv-0.13.1.toml"}} {
		t.Run(tc.file, func(t *testing.T) {
			f := setup(t)
			i, scope := f.pythonTool(tc.owner, "example-tool[feature]")
			f.pythonEntry(i, scope)
			data, err := os.ReadFile(filepath.Join("testdata/python", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			content := strings.NewReplacer("$ENV", scope, "$PYTHON", filepath.Join(f.home, "python"), "$BIN", filepath.Join(f.home, ".local/bin")).Replace(string(data))
			filename := "uv-receipt.toml"
			if tc.owner == "pipx" {
				filename = "pipx_metadata.json"
			}
			f.write(filepath.Join(scope, filename), content, 0644)
			digest, _ := fileHash(f.o.ManagerPaths[tc.owner])
			b := Binding{Scope: scope, ManagerPath: f.o.ManagerPaths[tc.owner], ManagerFingerprint: digest}
			policy, err := ReadPythonPolicy(i, b, f.o, tc.version, "update")
			if err != nil {
				t.Fatal(err)
			}
			if len(policy.Entrypoints) != 1 || policy.ManagerVersion != tc.version {
				t.Fatal(policy)
			}
		})
	}
}
