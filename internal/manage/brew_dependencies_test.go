package manage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func TestBrewUninstallDependencyScope(t *testing.T) {
	for _, representation := range []string{"dependencies", "evidence", "qualified-evidence"} {
		for _, scenario := range []string{"independent", "same-prefix", "cask-independent", "cask-same-prefix", "unconfigured", "unknown-owner", "unknown-kind", "missing-path", "missing-dependency", "missing-opt", "invalid-keg", "shared-prefix", "shared-rack", "nested-rack", "ancestor-rack", "cross-prefix-opt", "aliased-consumer", "batch-independent", "batch-same-prefix"} {
			t.Run(representation+"/"+scenario, func(t *testing.T) {
				f := setup(t)
				a, b := f.o.BrewRoots[0], filepath.Join(f.home, "brew-b")
				f.o.BrewRoots = append(f.o.BrewRoots, b)
				formula := func(root, id, name string) audit.Item {
					path := filepath.Join(root, "Cellar", name)
					f.write(filepath.Join(root, "bin/brew"), "#!/bin/sh\nexit 99\n", 0755)
					f.write(filepath.Join(path, "1.0.0/INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"},"runtime_dependencies":[]}`, 0644)
					return audit.Item{ID: id, Name: name, Kind: "formula", Owner: "homebrew", Path: path, Source: "homebrew/core", Version: "1.0.0", ActiveVersion: "1.0.0", Health: audit.Result{Status: "ok"}}
				}
				link := func(target, path string) {
					t.Helper()
					if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				}
				selected := formula(a, "a-zlib", "zlib")
				consumerRoot := b
				if scenario == "same-prefix" || scenario == "cask-same-prefix" || scenario == "batch-same-prefix" {
					consumerRoot = a
				}
				if scenario == "shared-prefix" {
					link(a, b)
				}
				consumer := formula(consumerRoot, "consumer", "consumer")
				if scenario == "cask-independent" || scenario == "cask-same-prefix" {
					consumer.Kind = "cask"
					consumer.Path = filepath.Join(consumerRoot, "Caskroom/consumer")
					f.write(filepath.Join(consumer.Path, ".metadata/INSTALL_RECEIPT.json"), `{}`, 0644)
				}
				dependency := filepath.Join(consumerRoot, "Cellar/zlib")
				if scenario == "shared-rack" {
					link(selected.Path, dependency)
				} else if scenario == "nested-rack" {
					link(filepath.Join(selected.Path, "1.0.0"), dependency)
					f.write(filepath.Join(dependency, "lib/content"), "shared payload", 0644)
				} else if scenario == "ancestor-rack" {
					link(filepath.Dir(selected.Path), dependency)
				} else if scenario != "missing-dependency" {
					formula(consumerRoot, "consumer-zlib", "zlib")
				}
				active := filepath.Join(dependency, "1.0.0")
				if scenario == "nested-rack" {
					active = filepath.Join(dependency, "lib")
				} else if scenario == "ancestor-rack" {
					active = selected.Path
				}
				if scenario == "cross-prefix-opt" {
					active = filepath.Join(selected.Path, "1.0.0")
				}
				if scenario == "invalid-keg" {
					active = filepath.Join(dependency, "not-a-directory")
					f.write(active, "not a keg", 0644)
				}
				if scenario != "missing-opt" {
					link(active, filepath.Join(consumerRoot, "opt/zlib"))
				}
				switch scenario {
				case "unconfigured":
					f.o.BrewRoots = []string{a}
				case "unknown-owner":
					consumer.Owner = "unknown"
				case "unknown-kind":
					consumer.Kind = "unknown"
				case "missing-path":
					consumer.Path = ""
				case "aliased-consumer":
					other := formula(a, "other-consumer", "other-consumer")
					consumer.Name = "linked-consumer"
					consumer.Path = filepath.Join(b, "Cellar/linked-consumer")
					link(other.Path, consumer.Path)
				}
				switch representation {
				case "dependencies":
					consumer.Dependencies = []audit.Dependency{{Name: "zlib", Status: "unknown"}}
				case "evidence":
					consumer.Evidence = []string{"dependency:zlib"}
				case "qualified-evidence":
					consumer.Evidence = []string{"dependency:homebrew/core/zlib"}
				}
				f.r.Items = []audit.Item{selected, consumer}
				selection := Selection{Action: "uninstall", IDs: []string{selected.ID}}
				if scenario == "batch-independent" || scenario == "batch-same-prefix" {
					selection.IDs = append(selection.IDs, consumer.ID)
				}
				plan := f.plan(selection)
				want := "blocked"
				if scenario == "independent" || scenario == "cask-independent" || scenario == "batch-independent" {
					want = "ready"
				}
				for _, item := range plan.Items {
					if item.ID == selected.ID && (item.Status != want || want == "blocked" && (item.Reason != "required_by_other_installations" || !contains(item.AffectedIDs, consumer.ID))) {
						t.Fatalf("target: %+v; want %s", item, want)
					}
					if item.ID == consumer.ID && item.Status != "ready" {
						t.Fatalf("batch consumer: %+v", item)
					}
				}
			})
		}
	}
}
