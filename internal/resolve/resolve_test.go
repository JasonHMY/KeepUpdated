package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

type queryRunner func(manage.Command) ([]byte, error)

func (f queryRunner) Run(_ context.Context, command manage.Command) ([]byte, error) {
	return f(command)
}

func nodeFixture(t *testing.T, candidate string) (Resolver, audit.Item, manage.Binding, *[]manage.Command) {
	t.Helper()
	home := t.TempDir()
	prefix := filepath.Join(home, "second-prefix")
	root := filepath.Join(prefix, "lib/node_modules")
	path := filepath.Join(root, "fixture")
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "package.json"), []byte(`{"name":"fixture","version":"1.0.0"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".npmrc"), []byte("//registry.example.invalid/:_authToken=DO_NOT_PRINT\n"), 0600); err != nil {
		t.Fatal(err)
	}
	i := audit.Item{ID: "fixture", Kind: "node-package", Name: "fixture", Identity: "fixture", Owner: "npm", Path: path, Version: "1.0.0"}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	b := manage.Binding{Path: path, RealPath: real, Scope: prefix, ManagerPath: filepath.Join(home, "first-prefix/bin/npm")}
	calls := []manage.Command{}
	r := Resolver{Options: manage.Options{Home: home}}
	r.Runner = queryRunner(func(c manage.Command) ([]byte, error) {
		calls = append(calls, c)
		if c.Dir != home {
			t.Fatalf("query cwd differs from apply: %s", c.Dir)
		}
		switch c.Args[0] {
		case "--version":
			return []byte("11.19.1"), nil
		case "root":
			return []byte(root), nil
		case "ls":
			return []byte(`{"dependencies":{"fixture":{"version":"1.0.0"}},"problems":["unrelated"]}`), errors.New("unrelated dependency")
		case "config", "view":
			joined := strings.Join(c.Args, "|")
			if !strings.Contains(joined, "--global|--prefix|"+prefix) {
				t.Fatalf("query does not use approved global prefix: %v", c.Args)
			}
			if c.Args[0] == "view" {
				return []byte(candidate), nil
			}
			if c.Args[2] == "tag" {
				return []byte("latest"), nil
			}
			if c.Args[2] == "registry" {
				return []byte("https://registry.example.invalid/"), nil
			}
			return []byte("undefined"), nil
		default:
			t.Fatalf("unexpected command: %v", c.Args)
		}
		return nil, nil
	})
	return r, i, b, &calls
}

func TestNodeQueriesUseSamePrefixAndKeepConfigurationPrivate(t *testing.T) {
	r, i, b, calls := nodeFixture(t, `{"name":"fixture","version":"2.0.0"}`)
	target, err := r.node(context.Background(), i, b)
	if err != nil || target.Version != "2.0.0" || !target.OriginVerified {
		t.Fatalf("%+v %v", target, err)
	}
	data, _ := json.Marshal(target)
	if strings.Contains(string(data), "DO_NOT_PRINT") || strings.Contains(string(data), "registry.example") {
		t.Fatal("configuration escaped into public plan")
	}
	for _, c := range *calls {
		if c.Args[0] == "install" || c.Args[0] == "update" {
			t.Fatal("preview changed installed packages")
		}
	}
	if err := os.WriteFile(filepath.Join(r.Options.Home, ".npmrc"), []byte("changed credential or config"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := r.node(context.Background(), i, b)
	if err != nil || changed.Source == target.Source {
		t.Fatal("configuration change did not invalidate source binding", err)
	}
}

func TestNodeDependencyScriptsNeedSpecialConfirmationAndNeverDowngrade(t *testing.T) {
	for _, tc := range []struct {
		candidate, want string
		scripts         bool
	}{
		{`{"name":"fixture","version":"2.0.0","dependencies":{"native-child":"^1"}}`, "2.0.0", true},
		{`{"name":"fixture","version":"2.0.0","optionalDependencies":{"optional-child":"^1"}}`, "2.0.0", true},
		{`{"name":"fixture","version":"2.0.0","scripts":{"postinstall":"build"}}`, "2.0.0", true},
		{`{"name":"fixture","version":"0.9.0"}`, "1.0.0", false},
	} {
		r, i, b, _ := nodeFixture(t, tc.candidate)
		target, err := r.node(context.Background(), i, b)
		if err != nil || target.Version != tc.want || target.HasLifecycleScripts != tc.scripts {
			t.Fatalf("%+v %v", target, err)
		}
	}
}

func TestNodeRecordedSourceMismatchAndWrongRegistryIdentityAreRejected(t *testing.T) {
	r, i, b, _ := nodeFixture(t, `{"name":"another-package","version":"2.0.0"}`)
	if _, err := r.node(context.Background(), i, b); err == nil {
		t.Fatal("accepted unrelated registry package")
	}
	r, i, b, _ = nodeFixture(t, `{"name":"fixture","version":"2.0.0"}`)
	if err := os.WriteFile(filepath.Join(i.Path, "package.json"), []byte(`{"name":"fixture","version":"1.0.0","_resolved":"https://different.invalid/fixture.tgz"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.node(context.Background(), i, b); err == nil {
		t.Fatal("changed recorded installation source")
	}
}

func TestBrewPinnedAndCandidateInstallationMismatchAreRejected(t *testing.T) {
	r := Resolver{Options: manage.Options{Home: t.TempDir()}, Runner: queryRunner(func(c manage.Command) ([]byte, error) {
		return []byte(`{"formulae":[{"name":"fixture","tap":"homebrew/core","versions":{"stable":"2.0"},"pinned":true,"installed":[{"version":"1.0"}]}]}`), nil
	})}
	i := audit.Item{Kind: "formula", Owner: "homebrew", Name: "fixture", Version: "1.0", Source: "homebrew/core"}
	if _, err := r.brew(context.Background(), i, manage.Binding{}); err == nil {
		t.Fatal("accepted pinned formula")
	}
}

func TestPythonOwnerQueryUsesExactEnvironmentAndKeepsConfigPrivate(t *testing.T) {
	for _, owner := range []string{"pipx", "uv"} {
		r, item, b := pythonResolverFixture(t, owner, "fixture", queryRunner(func(c manage.Command) ([]byte, error) {
			if owner == "pipx" && strings.Join(c.Args[:3], " ") != "runpip fixture list" {
				t.Fatal(c.Args)
			}
			if owner == "uv" && !strings.Contains(strings.Join(c.Args, "|"), "--python|") {
				t.Fatal(c.Args)
			}
			return []byte(`[{"name":"fixture","version":"1.0.0","latest_version":"2.0.0"}]`), nil
		}))
		target, err := r.python(context.Background(), item, b)
		if err != nil || target.Version != "2.0.0" || !target.OriginVerified || target.Python == nil {
			t.Fatal(target, err)
		}
	}
}

func TestBunQueriesWholeJSONObjectInOwningGlobalDirectory(t *testing.T) {
	r, i, b, calls := selectorFixture(t, "bun", "^1.0.0")
	target, err := r.bun(context.Background(), i, b)
	if err != nil || target.Version != "1.1.0" || target.Node.LatestVersion != "2.0.0" || !target.HasLifecycleScripts {
		t.Fatal(target, err)
	}
	for _, c := range *calls {
		if c.Dir != b.Scope || !strings.Contains(strings.Join(c.Env, "\n"), "BUN_INSTALL_GLOBAL_DIR="+b.Scope) {
			t.Fatal("unbound Bun global root", c)
		}
		if c.Args[0] == "info" && (len(c.Args) != 3 || c.Args[2] != "--json") {
			t.Fatal("query does not return full JSON", c.Args)
		}
	}
}

func TestBrewCandidateDependenciesAreBoundToThePreview(t *testing.T) {
	dependencies := "homebrew/core/new-shared-library\nnew-compiler\n"
	r := Resolver{Options: manage.Options{Home: t.TempDir()}, Runner: queryRunner(func(c manage.Command) ([]byte, error) {
		switch c.Args[0] {
		case "info":
			return []byte(`{"formulae":[{"name":"fixture","tap":"homebrew/core","versions":{"stable":"2.0"},"installed":[{"version":"1.0"}]}]}`), nil
		case "deps":
			for _, flag := range []string{"--include-build", "--include-optional", "--include-implicit", "--full-name", "--formula"} {
				if !strings.Contains(strings.Join(c.Args, " "), flag) {
					t.Fatalf("dependency scope missing %s", flag)
				}
			}
			return []byte(dependencies), nil
		default:
			t.Fatalf("unexpected brew query: %v", c.Args)
			return nil, nil
		}
	})}
	i := audit.Item{Kind: "formula", Owner: "homebrew", Name: "fixture", Version: "1.0", Source: "homebrew/core"}
	target, err := r.brew(context.Background(), i, manage.Binding{})
	if err != nil || strings.Join(target.AffectedPackages, ",") != "new-compiler,new-shared-library" {
		t.Fatal(target, err)
	}
	dependencies += "another-library\n"
	changed, err := r.brew(context.Background(), i, manage.Binding{})
	if err != nil || changed.Source == target.Source {
		t.Fatal("candidate dependency change did not invalidate source", err)
	}
	dependencies = "other/tap/private-library\n"
	if _, err := r.brew(context.Background(), i, manage.Binding{}); err == nil {
		t.Fatal("accepted unknown dependency source")
	}
}
