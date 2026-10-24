package resolve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func pythonResolverFixture(t *testing.T, owner, name string, runner queryRunner) (Resolver, audit.Item, manage.Binding) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(home, ".local/share/uv/tools", name)
	if owner == "pipx" {
		env = filepath.Join(home, ".local/share/pipx/venvs", name)
	}
	dist := filepath.Join(env, "lib/python3.13/site-packages", name+"-1.0.0.dist-info")
	write := func(path, data string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), mode); err != nil {
			t.Fatal(err)
		}
	}
	manager := filepath.Join(home, "bin", owner)
	write(manager, "controlled manager", 0755)
	write(filepath.Join(env, "bin/python"), "controlled interpreter", 0755)
	write(filepath.Join(env, "pyvenv.cfg"), "home = "+home+"\n", 0644)
	write(filepath.Join(dist, "METADATA"), "Name: "+name+"\nVersion: 1.0.0\n", 0644)
	if owner == "pipx" {
		data, _ := json.Marshal(map[string]any{"pipx_metadata_version": "0.5", "python_version": "Python 3.13.0", "source_interpreter": filepath.Join(env, "bin/python"), "venv_args": []any{}, "injected_packages": map[string]any{}, "main_package": map[string]any{"package": name, "package_or_url": name, "package_version": "1.0.0", "include_apps": true, "include_dependencies": false, "apps": []any{}, "app_paths": []any{}, "pip_args": []any{}}})
		write(filepath.Join(env, "pipx_metadata.json"), string(data), 0644)
	} else {
		write(filepath.Join(env, "uv-receipt.toml"), "[tool]\nrequirements = [{ name = \""+name+"\" }]\nentrypoints = []\n", 0644)
	}
	i := audit.Item{ID: "python", Name: name, Identity: name, Kind: "python-package", Owner: owner, Version: "1.0.0", Path: dist, Source: "pypi-public-candidate", Evidence: []string{"environment:" + env}}
	r := Resolver{Options: manage.Options{Home: home, ManagerPaths: map[string]string{owner: manager}}, Timeout: time.Second, Runner: queryRunner(func(c manage.Command) ([]byte, error) {
		if len(c.Args) == 1 && c.Args[0] == "--version" {
			if owner == "pipx" {
				return []byte("1.8.0\n"), nil
			}
			return []byte("uv 0.13.1 (fixture)\n"), nil
		}
		return runner(c)
	})}
	b, err := manage.ResolutionBinding(i, audit.Report{Items: []audit.Item{i}}, r.Options)
	if err != nil {
		t.Fatal(err)
	}
	return r, i, b
}

func TestPythonConfiguredConstraintsCannotUseOutdatedAsASolve(t *testing.T) {
	for _, owner := range []string{"pipx", "uv"} {
		r, item, b := pythonResolverFixture(t, owner, "example", queryRunner(func(c manage.Command) ([]byte, error) {
			t.Fatal("queried candidate under unhandled configuration", c.Args)
			return nil, nil
		}))
		path := filepath.Join(r.Options.Home, ".config/pip/pip.conf")
		content := "[install]\nconstraint = /synthetic/limits.txt\n"
		if owner == "uv" {
			path = filepath.Join(r.Options.Home, ".config/uv/uv.toml")
			content = "constraint-dependencies = [\"example<2\"]\n"
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		_, err := r.python(context.Background(), item, b)
		if err == nil || failureReason(err) != "python_configuration_requires_review" {
			t.Fatal(err)
		}
	}
}

func TestPythonUVIndexTablesRequireReview(t *testing.T) {
	for _, content := range []string{"[[index]]\nurl = \"https://private.invalid\"\n", "[[tool.uv.index]]\nurl = \"https://private.invalid\"\n", "[tool.uv.sources]\nexample = { index = \"private\" }\n"} {
		path := filepath.Join(t.TempDir(), "uv.toml")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		if err := pythonConfigSupportsQuery("uv", []string{path}); err == nil {
			t.Fatal("index table ignored", content)
		}
	}
}

func TestPythonConfigurationScopeWholeResolverPlan(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		blocked      bool
	}{
		{"default source", "", false},
		{"comments only", "# Keep the manager defaults.\n", false},
		{"default source with scalar option", "no-build = true\n", false},
		{"pip section", "[pip]\nindex-url = \"https://private.invalid/simple\"\n", true},
		{"quoted dotted key", `"pip"."index-url" = "https://private.invalid/simple"`, true},
		{"inline pip table", `pip = { index-url = "https://private.invalid/simple" }`, true},
		{"escaped quoted key", `"p\u0069p"."index-url" = "https://private.invalid/simple"`, true},
		{"quoted table", "[\"pip\"]\nindex-url = \"https://private.invalid/simple\"\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queries := 0
			r, item, _ := pythonResolverFixture(t, "uv", "example", queryRunner(func(c manage.Command) ([]byte, error) {
				queries++
				return []byte(`[{"name":"example","version":"1.0.0","latest_version":"2.0.0"}]`), nil
			}))
			if tc.config != "" {
				path := filepath.Join(r.Options.Home, ".config/uv/uv.toml")
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.config), 0644); err != nil {
					t.Fatal(err)
				}
			}
			report := audit.Report{Items: []audit.Item{item}}
			selection := manage.Selection{Action: "update", All: true}
			targets, err := r.Resolve(context.Background(), report, selection)
			if err != nil {
				t.Fatal(err)
			}
			options := r.Options
			options.ResolvedTargets = targets
			plan, err := manage.NewPlan(report, selection, options)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Items) != 1 {
				t.Fatal("missing Python preview")
			}
			result := plan.Items[0]
			if tc.blocked {
				if queries != 0 || targets[item.ID].OriginVerified || result.Status != "manual" || result.Reason != "python_configuration_requires_review" {
					t.Fatalf("unverified pip/tool source scope: queries=%d status=%s reason=%s", queries, result.Status, result.Reason)
				}
			} else if queries != 1 || !targets[item.ID].OriginVerified || result.Status != "ready" || result.TargetVersion != "2.0.0" {
				t.Fatalf("default source lost support: queries=%d status=%s reason=%s", queries, result.Status, result.Reason)
			}
		})
	}
}
