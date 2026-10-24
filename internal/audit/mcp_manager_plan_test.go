package audit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
	"github.com/JasonHMY/KeepUpdated/internal/resolve"
)

func TestMCPCustomManagerPathsThroughApply(t *testing.T) {
	for _, scenario := range []string{"shared-entries", "missing-manager", "network", "permission", "bin-drift", "root-drift"} {
		t.Run(scenario, func(t *testing.T) {
			f := newMCPOwnerFixture(t)
			root, bin := filepath.Join(f.home, "custom/global"), filepath.Join(f.home, "custom/bin")
			pkg := filepath.Join(root, "node_modules/fixture-server")
			manager := filepath.Join(bin, "bun")
			marker := filepath.Join(f.home, "payload-started")
			install := func(version string) {
				f.write(filepath.Join(pkg, "package.json"), fmt.Sprintf(`{"name":"fixture-server","version":%q,"bin":{"fixture-server":"serve.js"}}`, version), 0644)
				f.write(filepath.Join(pkg, "serve.js"), "#!/bin/sh\ntouch '"+marker+"'\nexit 99\n", 0755)
			}
			install("1.0.0")
			f.write(filepath.Join(root, "package.json"), `{"dependencies":{"fixture-server":"^1.0.0"}}`, 0644)
			f.write(manager, "controlled manager; never executed", 0755)
			if err := os.Symlink(filepath.Join(pkg, "serve.js"), filepath.Join(bin, "fixture-server")); err != nil {
				t.Fatal(err)
			}
			// A different npm installation has the same name and a lower-priority PATH entry.
			f.config.Path = bin + string(os.PathListSeparator) + filepath.Join(f.prefix, "bin")
			f.config.ManagerRoots = map[string]string{"bun": root}
			f.config.ManagerBins = map[string]string{"bun": bin}
			data, _ := json.Marshal(map[string]any{"mcp": map[string]any{
				"bare":     map[string]any{"type": "local", "command": []string{"fixture-server"}},
				"absolute": map[string]any{"type": "local", "command": []string{filepath.Join(bin, "fixture-server")}},
			}})
			f.write(f.config.MCPConfigs[0], string(data), 0600)
			f.options.ManagerPaths = map[string]string{"bun": manager}
			f.options.ManagerRoots, f.options.ManagerBins = f.config.ManagerRoots, f.config.ManagerBins
			snapshot := func(context.Context) (audit.Report, error) { return audit.PackageMCPFixtureReport(f.config), nil }
			report, _ := snapshot(context.Background())
			selection := manage.Selection{Action: "update"}
			ownerID := ""
			for _, item := range report.Items {
				if item.Path == pkg {
					ownerID = item.ID
					selection.IDs = append(selection.IDs, item.ID)
				}
				if item.Kind == "mcp-server" {
					selection.IDs = append(selection.IDs, item.ID)
				}
			}
			if ownerID == "" || len(selection.IDs) != 3 || len(report.Queries) != 0 {
				t.Fatal("isolated discovery incomplete", selection, report.Queries)
			}
			for _, item := range report.Items {
				if item.Kind == "mcp-server" && (len(item.Related) != 1 || item.Related[0] != ownerID) {
					t.Fatal("borrowed or lost owner", item)
				}
			}
			if scenario == "missing-manager" {
				if err := os.Remove(manager); err != nil {
					t.Fatal(err)
				}
			}
			var mutations, queries atomic.Int32
			runner := nodeUpdateRunner(func(_ context.Context, c manage.Command) ([]byte, error) {
				if c.Path != manager {
					t.Errorf("wrong manager: %+v", c)
					return nil, fmt.Errorf("wrong manager")
				}
				if c.Args[0] == "add" {
					mutations.Add(1)
					if !strings.Contains(strings.Join(c.Args, " "), "fixture-server@1.1.0") {
						t.Errorf("exact target changed: %v", c.Args)
					}
					install("1.1.0")
					return nil, nil
				}
				queries.Add(1)
				if scenario == "network" {
					return nil, manage.CommandFailure{Code: "owner_network_unavailable"}
				}
				if scenario == "permission" {
					return nil, os.ErrPermission
				}
				switch c.Args[0] {
				case "--version":
					return []byte("1.4.3"), nil
				case "pm":
					return []byte(bin), nil
				case "info":
					return []byte(`{"name":"fixture-server","version":"1.1.0","dist":{"tarball":"https://registry.example.invalid/fixture-server.tgz"}}`), nil
				default:
					t.Errorf("unexpected query: %+v", c)
					return nil, fmt.Errorf("unexpected query")
				}
			})
			resolveTargets := func(ctx context.Context, report audit.Report, selection manage.Selection) (map[string]manage.ResolvedTarget, error) {
				return (resolve.Resolver{Options: f.options, Runner: runner, Timeout: time.Second}).Resolve(ctx, report, selection)
			}
			targets, err := resolveTargets(context.Background(), report, selection)
			if err != nil {
				t.Fatal(err)
			}
			options := f.options
			options.ResolvedTargets = targets
			plan, err := manage.NewPlan(report, selection, options)
			if err != nil {
				t.Fatal(err)
			}
			ready := 0
			for _, item := range plan.Items {
				if item.Status == "ready" {
					ready++
				}
			}
			failure := scenario == "missing-manager" || scenario == "network" || scenario == "permission"
			if failure {
				if ready != 0 {
					t.Fatal("failed query became executable", plan.Items)
				}
				for _, item := range plan.Items {
					if item.TargetVersion != "" || item.Status != "manual" && item.Status != "blocked" {
						t.Fatal("failure hidden", item)
					}
					if scenario != "missing-manager" && item.Reason != map[string]string{"network": "owner_network_unavailable", "permission": "owner_permission_denied"}[scenario] {
						t.Fatal("lost query reason", item)
					}
				}
				if scenario == "missing-manager" && queries.Load() != 0 {
					t.Fatal("missing manager invoked")
				}
			} else if ready != 1 {
				t.Fatal("shared owner not deduplicated", plan.Items)
			}
			if scenario == "bin-drift" {
				f.options.ManagerBins = map[string]string{"bun": filepath.Join(f.home, "changed-bin")}
			}
			if scenario == "root-drift" {
				f.options.ManagerRoots = map[string]string{"bun": filepath.Join(f.home, "changed-root")}
			}
			engine := manage.Engine{Options: f.options, StateDir: filepath.Join(f.home, "state"), Snapshot: snapshot, ResolveTargets: resolveTargets, Runner: runner, Now: func() time.Time { return f.options.Now.Add(time.Minute) }}
			result, err := manage.Apply(context.Background(), plan, plan.PlanID, engine)
			if strings.HasSuffix(scenario, "drift") {
				if err == nil || mutations.Load() != 0 {
					t.Fatal("drift did not stop before execution", result, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if scenario == "shared-entries" {
				updated := 0
				for _, item := range result.Results {
					if item.Status == "updated" {
						updated++
					}
				}
				manifest, readErr := os.ReadFile(filepath.Join(root, "package.json"))
				if readErr != nil || string(manifest) != `{"dependencies":{"fixture-server":"^1.0.0"}}` {
					t.Fatal("original selector changed", string(manifest), readErr)
				}
				if mutations.Load() != 1 || updated != 1 {
					t.Fatal("shared owner execution/readback failed", result, mutations.Load())
				}
			} else if mutations.Load() != 0 {
				t.Fatal("refused item mutated")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("MCP payload started", err)
			}
			if _, err := os.Stat(manager); scenario == "missing-manager" && !os.IsNotExist(err) {
				t.Fatal("missing manager was installed")
			}
		})
	}
}
