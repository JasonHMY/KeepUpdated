package resolve

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func TestVoltaLegacyInventoriesNeverQueryNativeManager(t *testing.T) {
	for _, scenario := range []string{"conventional", "custom-marker", "custom-config", "alias", "owner", "manager-only"} {
		for _, entry := range []string{"package", "mcp-owner"} {
			t.Run(scenario+"/"+entry, func(t *testing.T) {
				home, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				root := filepath.Join(home, ".volta")
				if scenario == "custom-marker" || scenario == "custom-config" || scenario == "alias" {
					root = filepath.Join(home, "custom-volta")
				}
				write := func(p, body string, mode os.FileMode) {
					t.Helper()
					if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte(body), mode); err != nil {
						t.Fatal(err)
					}
				}
				write(filepath.Join(root, "layout.v4"), "", 0644)
				pkgPath := filepath.Join(root, "tools/image/packages/fixture/lib/node_modules/fixture")
				if scenario == "manager-only" {
					pkgPath = filepath.Join(home, "ordinary/lib/node_modules/fixture")
				}
				write(filepath.Join(pkgPath, "package.json"), `{"name":"fixture","version":"1.0.0"}`, 0644)
				if scenario == "alias" {
					alias := filepath.Join(home, "ordinary/lib/node_modules/fixture")
					if err := os.MkdirAll(filepath.Dir(alias), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(pkgPath, alias); err != nil {
						t.Fatal(err)
					}
					pkgPath = alias
				}
				manager := filepath.Join(root, "bin/npm")
				write(manager, "controlled shim; must never be dispatched", 0755)
				pkg := audit.Item{ID: "pkg", Kind: "node-package", Name: "fixture", Identity: "fixture", Path: pkgPath, Owner: "npm", Source: "npm-public-candidate", Version: "1.0.0", Health: audit.Result{Status: "ok"}}
				if scenario == "owner" {
					pkg.Owner = "volta"
				}
				// A legacy report can mark the first PATH shim active and healthy. No manager override is needed.
				executable := audit.Item{ID: "npm", Kind: "executable", Name: "npm", Path: manager, Owner: "PATH", Active: true, ActivityKnown: true, Health: audit.Result{Status: "ok"}}
				config := filepath.Join(home, "opencode.json")
				write(config, `{"mcp":{"fixture":{"type":"local","command":["fixture"]}}}`, 0600)
				server := audit.Item{ID: "server", Kind: "mcp-server", Name: "fixture MCP", Owner: "configuration", Path: url.PathEscape(config) + "#mcp#fixture", Related: []string{pkg.ID}}
				report := audit.Report{Schema: audit.Schema, Items: []audit.Item{pkg, executable, server}}
				selection := manage.Selection{Action: "update", IDs: []string{pkg.ID}}
				if entry == "mcp-owner" {
					selection.IDs = []string{server.ID}
					expanded := manage.ResolutionSelection(report, selection)
					if scenario != "owner" && !slices.Contains(expanded.IDs, pkg.ID) {
						t.Fatal("fixture did not exercise owner expansion", expanded)
					}
				}
				options := manage.Options{Home: home, BrewRoots: []string{}}
				if scenario == "custom-config" {
					options.ManagerRoots = map[string]string{"volta": root}
					if err := os.Remove(filepath.Join(root, "layout.v4")); err != nil {
						t.Fatal(err)
					}
				}
				var calls atomic.Int32
				resolver := Resolver{Options: options, Runner: queryRunner(func(manage.Command) ([]byte, error) { calls.Add(1); return nil, errors.New("unexpected manager query") })}
				targets, err := resolver.Resolve(context.Background(), report, selection)
				if err != nil || calls.Load() != 0 {
					t.Fatalf("Volta manager dispatched %d times: %v", calls.Load(), err)
				}
				if targets[pkg.ID].ResolutionError != "volta_owner_manual" {
					t.Fatal("outer owner query reason lost", targets)
				}
				options.ResolvedTargets = targets
				plan, err := manage.NewPlan(report, selection, options)
				if err != nil || len(plan.Items) != 1 || plan.Items[0].Status != "manual" {
					t.Fatal(plan, err)
				}
			})
		}
	}
}

func TestOrdinaryNodeOwnersStillResolveThroughWholeResolver(t *testing.T) {
	for _, owner := range []string{"npm", "pnpm", "bun"} {
		t.Run(owner, func(t *testing.T) {
			r, i, _, calls := selectorFixture(t, owner, "^1.0.0")
			targets, err := r.Resolve(context.Background(), audit.Report{Items: []audit.Item{i}}, manage.Selection{Action: "update", IDs: []string{i.ID}})
			if err != nil || len(*calls) == 0 || targets[i.ID].ResolutionError != "" || !targets[i.ID].OriginVerified {
				t.Fatal("ordinary owner queries regressed", targets, err)
			}
		})
	}
}
