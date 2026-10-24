package audit_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

type nodeUpdateRunner func(context.Context, manage.Command) ([]byte, error)

func (r nodeUpdateRunner) Run(ctx context.Context, c manage.Command) ([]byte, error) {
	return r(ctx, c)
}

func TestNodeApplyVerifiesEveryDeclaredCommand(t *testing.T) {
	for _, layout := range []struct{ owner, version, store string }{
		{"npm", "11.19.1", ""},
		{"pnpm", "10.18.3", ".pnpm"},
		{"pnpm", "10.18.3", "node_modules/.pnpm"},
		{"bun", "1.4.3", ""},
	} {
		scenarios := []string{"missing", "old-payload", "other-package", "wrong-command", "not-executable", "other-bin", "all-correct"}
		if layout.owner != "npm" {
			scenarios = append(scenarios, "custom-bin")
		}
		if layout.owner == "pnpm" {
			scenarios = append(scenarios, "wrapper-correct", "wrapper-old-payload", "wrapper-other-package", "wrapper-wrong-command", "wrapper-branches-disagree")
		}
		for _, scenario := range scenarios {
			t.Run(layout.owner+"/"+layout.store+"/"+scenario, func(t *testing.T) {
				home, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				write := func(path, text string, mode os.FileMode) {
					t.Helper()
					if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(text), mode); err != nil {
						t.Fatal(err)
					}
				}
				link := func(target, path string) {
					t.Helper()
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				}
				scope := filepath.Join(home, ".npm-global")
				modules := filepath.Join(scope, "lib/node_modules")
				bin := filepath.Join(scope, "bin")
				if layout.owner == "pnpm" {
					scope = filepath.Join(home, "Library/pnpm/global/5")
					modules = filepath.Join(scope, "node_modules")
					bin = filepath.Join(home, "Library/pnpm")
				} else if layout.owner == "bun" {
					scope = filepath.Join(home, ".bun/install/global")
					modules = filepath.Join(scope, "node_modules")
					bin = filepath.Join(home, ".bun/bin")
				}
				if scenario == "custom-bin" {
					bin = filepath.Join(home, "custom bin")
				}
				path := filepath.Join(modules, "example")
				marker := filepath.Join(home, "payload-was-run")
				payload := "#!/bin/sh\n: > '" + marker + "'\nexit 97\n"
				install := func(version string, extra bool) {
					t.Helper()
					realPath := path
					if layout.store != "" {
						realPath = filepath.Join(scope, layout.store, "example@"+version, "node_modules/example")
						if err := os.MkdirAll(modules, 0755); err != nil {
							t.Fatal(err)
						}
						if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
							t.Fatal(err)
						}
						link(realPath, path)
					}
					bins := `"example":"cli.js"`
					if extra {
						bins += `,"example-extra":"extra.js"`
					}
					write(filepath.Join(realPath, "package.json"), fmt.Sprintf(`{"name":"example","version":%q,"_resolved":"https://registry.npmjs.org/example/-/example-%s.tgz","bin":{%s}}`, version, version, bins), 0644)
					write(filepath.Join(realPath, "cli.js"), payload, 0755)
					write(filepath.Join(realPath, "extra.js"), payload, 0755)
				}
				install("1.0.0", false)
				if layout.owner != "npm" {
					write(filepath.Join(scope, "package.json"), `{"dependencies":{"example":"^1.0.0"}}`, 0644)
				}
				if layout.owner == "pnpm" {
					relative := "../.pnpm"
					if layout.store == "node_modules/.pnpm" {
						relative = ".pnpm"
					}
					write(filepath.Join(modules, ".modules.yaml"), "layoutVersion: 5\nnodeLinker: isolated\npackageManager: pnpm@10.18.3\nvirtualStoreDir: "+relative+"\n", 0644)
				}
				manager := filepath.Join(bin, layout.owner)
				const managerContent = "controlled manager\n"
				write(manager, managerContent, 0755)
				link(filepath.Join(path, "cli.js"), filepath.Join(bin, "example"))
				config := audit.Config{Home: home, Path: bin, Timeout: time.Second}
				snapshot := func(context.Context) (audit.Report, error) {
					return audit.ReadNodePackageForTest(config, path, layout.owner), nil
				}
				report, _ := snapshot(context.Background())
				if len(report.Items) != 1 || !report.Items[0].Active {
					t.Fatal("initial command not active", report)
				}
				item := report.Items[0]
				selector, err := audit.ReadNodeSelector(layout.owner, item.Name, path, scope)
				if err != nil {
					t.Fatal(err)
				}
				now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
				targets := map[string]manage.ResolvedTarget{item.ID: {
					Provider: layout.owner, Scope: scope, ManagerPath: manager, OriginVerified: true,
					Source: "controlled native query", Version: "1.1.0",
					Node: &manage.NodePolicy{ManagerVersion: layout.version, ManagerFingerprint: fmt.Sprintf("%x", sha256.Sum256([]byte(managerContent))), Layout: manage.NodeLayout(layout.owner, layout.version), Bin: bin, Selection: selector, VirtualStore: layout.store},
				}}
				options := manage.Options{Home: home, Now: now, ManagerPaths: map[string]string{layout.owner: manager}, ResolvedTargets: targets}
				if scenario == "custom-bin" {
					options.ManagerBins = map[string]string{layout.owner: bin}
				}
				plan, err := manage.NewPlan(report, manage.Selection{Action: "update", IDs: []string{item.ID}}, options)
				if err != nil || len(plan.Items) != 1 || plan.Items[0].Status != "ready" {
					t.Fatalf("plan: %+v, %v", plan.Items, err)
				}
				calls := 0
				engine := manage.Engine{Options: options, StateDir: filepath.Join(home, "state"), Snapshot: snapshot, Now: func() time.Time { return now.Add(time.Minute) },
					ResolveTargets: func(context.Context, audit.Report, manage.Selection) (map[string]manage.ResolvedTarget, error) {
						return targets, nil
					},
					Runner: nodeUpdateRunner(func(context.Context, manage.Command) ([]byte, error) {
						calls++
						install("1.1.0", true)
						extra := filepath.Join(path, "extra.js")
						switch scenario {
						case "missing":
							return nil, nil
						case "old-payload", "wrapper-old-payload":
							extra = filepath.Join(home, "old-installation/extra.js")
							if layout.owner == "pnpm" {
								extra = filepath.Join(scope, layout.store, "example@1.0.0/node_modules/example/extra.js")
							} else {
								write(extra, payload, 0755)
							}
						case "other-package", "wrapper-other-package":
							extra = filepath.Join(modules, "other/cli.js")
							write(extra, payload, 0755)
						case "wrong-command", "wrapper-wrong-command":
							extra = filepath.Join(path, "cli.js")
						case "not-executable":
							write(filepath.Join(bin, "example-extra"), payload, 0644)
							return nil, nil
						case "other-bin":
							otherBin := filepath.Join(home, "other-bin")
							if err := os.Mkdir(otherBin, 0755); err != nil {
								t.Fatal(err)
							}
							link(extra, filepath.Join(otherBin, "example-extra"))
							config.Path += string(os.PathListSeparator) + otherBin
							return nil, nil
						}
						entry := filepath.Join(bin, "example-extra")
						if strings.HasPrefix(scenario, "wrapper-") {
							// Generated cmd-shim 7.0.0 layout, pinned by pnpm 10.18.3.
							data, err := os.ReadFile("testdata/pnpm-node-shim.sh")
							if err != nil {
								t.Fatal(err)
							}
							relative, err := filepath.Rel(bin, extra)
							if err != nil {
								t.Fatal(err)
							}
							shim := strings.ReplaceAll(string(data), "TARGET", relative)
							shim = strings.ReplaceAll(shim, "NODE_PATH_VALUE", filepath.Join(scope, "node_modules"))
							if scenario == "wrapper-branches-disagree" {
								shim = strings.Replace(shim, relative, strings.Replace(relative, "extra.js", "cli.js", 1), 1)
							}
							write(entry, shim, 0755)
						} else {
							link(extra, entry)
						}
						return nil, nil
					}),
				}
				result, err := manage.Apply(context.Background(), plan, plan.PlanID, engine)
				if err != nil || calls != 1 || len(result.Results) != 1 {
					t.Fatalf("apply: %+v, %v, calls=%d", result, err, calls)
				}
				post, _ := snapshot(context.Background())
				if !post.Items[0].Active || post.Items[0].Version != "1.1.0" || post.Items[0].Health.Status != "ok" {
					t.Fatal("existing command must remain active in partial outcomes", post)
				}
				wantStatus, wantReason := "verification_unknown", "manager_succeeded_but_requested_state_unverified"
				if scenario == "all-correct" || scenario == "wrapper-correct" || scenario == "custom-bin" {
					wantStatus, wantReason = "updated", "target_version_verified"
				}
				r := result.Results[0]
				if r.Status != wantStatus || r.Reason != wantReason || !r.Changed || !r.Executed || !r.CommandSucceeded || r.AfterVersion != "1.1.0" {
					t.Fatalf("got %+v; want %s/%s", r, wantStatus, wantReason)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("payload was executed: %v", err)
				}
			})
		}
	}
}
