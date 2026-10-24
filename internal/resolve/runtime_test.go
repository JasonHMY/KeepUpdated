package resolve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func TestRuntimeResolutionBindsPrefixPinsAndDependents(t *testing.T) {
	for _, mode := range []string{"valid", "prefix", "pin", "source", "dependency-query", "custom-dependent"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, "brew")
			if err := os.MkdirAll(root, 0755); err != nil {
				t.Fatal(err)
			}
			i := audit.Item{Kind: "formula", Owner: "homebrew", Name: "node", Source: "homebrew/core", Version: "22.1.0"}
			binding := manage.Binding{ManagerPath: filepath.Join(root, "bin/brew"), Scope: root}
			resolver := Resolver{Options: manage.Options{Home: home}, Runner: queryRunner(func(c manage.Command) ([]byte, error) {
				switch c.Args[0] {
				case "info":
					pinned := "false"
					source := "homebrew/core"
					if mode == "pin" {
						pinned = "true"
					}
					if mode == "source" {
						source = "another/tap"
					}
					return []byte(`{"formulae":[{"name":"node","tap":"` + source + `","versions":{"stable":"22.2.0"},"pinned":` + pinned + `,"installed":[{"version":"22.1.0"}]}]}`), nil
				case "deps":
					return []byte("openssl@3\n"), nil
				case "--prefix":
					if mode == "prefix" {
						return []byte(home), nil
					}
					return []byte(root), nil
				case "uses":
					if mode == "dependency-query" {
						return nil, errors.New("query failure")
					}
					if mode == "custom-dependent" {
						return []byte("custom/tap/consumer\n"), nil
					}
					if strings.Join(c.Args, " ") != "uses --installed --recursive --include-build --include-optional node" {
						t.Fatal(c.Args)
					}
					return []byte("consumer\nhomebrew/core/consumer\nsecond\n"), nil
				}
				t.Fatalf("unexpected query: %v", c.Args)
				return nil, nil
			})}
			target, err := resolver.brew(context.Background(), i, binding)
			if mode == "valid" {
				if err != nil || !target.RuntimeScopeVerified || strings.Join(target.RuntimeDependents, ",") != "consumer,second" {
					t.Fatal(target, err)
				}
			} else if err == nil {
				t.Fatal("unsafe runtime resolution", mode, target)
			}
		})
	}
}

func TestProtectedDependencyResolutionDistinguishesUnchangedAndPossibleUpgrade(t *testing.T) {
	for _, mode := range []string{"current", "outdated", "missing-outdated", "missing-installation", "major", "source", "query-error"} {
		t.Run(mode, func(t *testing.T) {
			r := Resolver{Runner: queryRunner(func(c manage.Command) ([]byte, error) {
				if strings.Join(c.Args, " ") != "info --json=v2 --formula node" {
					t.Fatal(c.Args)
				}
				if mode == "query-error" {
					return nil, errors.New("unavailable")
				}
				outdated := `,"outdated":false`
				if mode == "outdated" || mode == "major" {
					outdated = `,"outdated":true`
				}
				if mode == "missing-outdated" {
					outdated = ""
				}
				candidate := "22.1.0"
				if mode == "major" {
					candidate = "23.0.0"
				}
				installed := `[{"version":"22.1.0"}]`
				if mode == "missing-installation" {
					installed = `[]`
				}
				source := "homebrew/core"
				if mode == "source" {
					source = "custom/tap"
				}
				return []byte(`{"formulae":[{"name":"node","tap":"` + source + `","versions":{"stable":"` + candidate + `"},"installed":` + installed + outdated + `}]}`), nil
			})}
			dependency := r.runtimeDependency(context.Background(), "node", manage.Binding{})
			if dependency.Unchanged != (mode == "current") {
				t.Fatal(mode, dependency)
			}
			if mode == "major" && (dependency.CurrentVersion != "22.1.0" || dependency.TargetVersion != "23.0.0") {
				t.Fatal("version impact lost", dependency)
			}
		})
	}
}
