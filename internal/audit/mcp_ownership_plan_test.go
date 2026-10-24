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

type mcpOwnerFixture struct {
	t                          *testing.T
	home, prefix, pkg, command string
	config                     audit.Config
	options                    manage.Options
	queries                    atomic.Int32
}

func newMCPOwnerFixture(t *testing.T) *mcpOwnerFixture {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &mcpOwnerFixture{t: t, home: home, prefix: filepath.Join(home, "prefix")}
	f.pkg = filepath.Join(f.prefix, "lib/node_modules/fixture-server")
	f.command = filepath.Join(f.prefix, "bin/fixture-server")
	f.write(filepath.Join(f.pkg, "package.json"), `{"name":"fixture-server","version":"1.0.0","bin":{"fixture-server":"serve.js"}}`, 0644)
	f.write(filepath.Join(f.pkg, "serve.js"), "#!/bin/sh\nexit 99\n", 0755)
	f.write(filepath.Join(f.prefix, "bin/npm"), "controlled manager; never executed", 0755)
	if err := os.Symlink(filepath.Join(f.pkg, "serve.js"), f.command); err != nil {
		t.Fatal(err)
	}
	f.config = audit.Config{Home: home, Path: filepath.Dir(f.command), MCPConfigs: []string{filepath.Join(home, "opencode.json")}, Timeout: time.Second}
	f.options = manage.Options{Home: home, BrewRoots: []string{}, ManagerPaths: map[string]string{"npm": filepath.Join(f.prefix, "bin/npm")}, Now: time.Now()}
	return f
}

func (f *mcpOwnerFixture) write(path, body string, mode os.FileMode) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		f.t.Fatal(err)
	}
}

func (f *mcpOwnerFixture) definition(definition map[string]any) {
	f.t.Helper()
	data, err := json.Marshal(map[string]any{"mcp": map[string]any{"fixture": definition}})
	if err != nil {
		f.t.Fatal(err)
	}
	f.write(f.config.MCPConfigs[0], string(data), 0600)
}

func (f *mcpOwnerFixture) report(check bool) (audit.Report, audit.Item) {
	f.t.Helper()
	report := audit.MCPFixtureReport(f.config, f.pkg, check)
	if len(report.Queries) != 0 {
		f.t.Fatal("scan/check executed commands", report.Queries)
	}
	data, err := json.Marshal(report)
	if err != nil || strings.Contains(string(data), "SECRET_") {
		f.t.Fatal("invalid report or configuration values leaked", err)
	}
	for _, item := range report.Items {
		if item.Kind == "mcp-server" {
			return report, item
		}
	}
	f.t.Fatal("MCP definition missing")
	return report, audit.Item{}
}

func (f *mcpOwnerFixture) Run(_ context.Context, c manage.Command) ([]byte, error) {
	f.queries.Add(1)
	switch c.Args[0] {
	case "--version":
		return []byte("11.19.1"), nil
	case "root":
		return []byte(filepath.Join(f.prefix, "lib/node_modules")), nil
	case "ls":
		return []byte(fmt.Sprintf(`{"dependencies":{%q:{"version":"1.0.0"}}}`, filepath.Base(f.pkg))), nil
	case "config":
		if c.Args[2] == "tag" {
			return []byte("latest"), nil
		}
		if c.Args[2] == "registry" {
			return []byte("https://registry.example.invalid/"), nil
		}
		return []byte("undefined"), nil
	case "view":
		return []byte(fmt.Sprintf(`{"name":%q,"version":"2.0.0"}`, filepath.Base(f.pkg))), nil
	}
	return nil, fmt.Errorf("unexpected controlled query: %v", c.Args)
}

func (f *mcpOwnerFixture) plan(report audit.Report, server audit.Item) manage.Plan {
	f.t.Helper()
	selection := manage.Selection{Action: "update", IDs: []string{server.ID}}
	targets, err := (resolve.Resolver{Options: f.options, Runner: f, Timeout: time.Second}).Resolve(context.Background(), report, selection)
	if err != nil {
		f.t.Fatal(err)
	}
	options := f.options
	options.ResolvedTargets = targets
	plan, err := manage.NewPlan(report, selection, options)
	if err != nil || len(plan.Items) != 1 {
		f.t.Fatal(plan, err)
	}
	return plan
}

func TestMCPCommandOwnershipThroughPlan(t *testing.T) {
	for _, scenario := range []string{
		"bare", "absolute", "ordinary-env", "ordinary-environment", "absolute-path-override", "disabled-bare", "disabled-env",
		"relative-dot", "relative-subdirectory", "relative-parent", "env-path", "environment-path",
		"empty-path", "null-path", "invalid-environment", "null-environment", "conflicting-paths",
		"relative-search-path", "empty-search-path", "shadowed-executable", "array-and-args", "invalid-args", "nonstring-arg",
		"npx", "ambiguous-npx", "npm-exec", "node-wrapper",
	} {
		for _, check := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/check=%t", scenario, check), func(t *testing.T) {
				f := newMCPOwnerFixture(t)
				definition := map[string]any{"type": "local", "command": []any{"fixture-server", "SECRET_ARG"}}
				wantOwner := false
				switch scenario {
				case "bare":
					wantOwner = true
				case "disabled-bare", "disabled-env":
					definition["enabled"] = false
					wantOwner = scenario == "disabled-bare"
					if !wantOwner {
						definition["environment"] = map[string]any{"PATH": "/SECRET_CLIENT_PATH"}
					}
				case "absolute", "absolute-path-override":
					definition["command"] = []any{f.command, "SECRET_ARG"}
					if scenario == "absolute-path-override" {
						definition["environment"] = map[string]any{"PATH": "/SECRET_CLIENT_PATH"}
					}
					wantOwner = true
				case "ordinary-env", "ordinary-environment":
					definition[strings.TrimPrefix(scenario, "ordinary-")] = map[string]any{"TOKEN": "SECRET_TOKEN"}
					wantOwner = true
				case "relative-dot", "relative-subdirectory", "relative-parent":
					command := "./bin/fixture-server"
					f.config.Path = f.prefix
					if scenario == "relative-subdirectory" {
						command = "bin/fixture-server"
					} else if scenario == "relative-parent" {
						command = "../bin/fixture-server"
						f.config.Path = filepath.Join(f.prefix, "other")
					}
					definition["command"] = []any{command}
					definition["cwd"] = filepath.Join(f.home, "client-project")
				case "env-path", "environment-path", "empty-path", "null-path", "conflicting-paths":
					var value any = "/SECRET_CLIENT_PATH"
					key := "environment"
					if scenario == "env-path" {
						key = "env"
					} else if scenario == "empty-path" {
						value = ""
					} else if scenario == "null-path" {
						value = nil
					} else if scenario == "conflicting-paths" {
						definition["env"] = map[string]any{"PATH": filepath.Dir(f.command)}
					}
					definition[key] = map[string]any{"PATH": value}
				case "invalid-environment":
					definition["environment"] = []any{"PATH=/SECRET_CLIENT_PATH"}
				case "null-environment":
					definition["environment"] = nil
				case "relative-search-path":
					f.config.Path = "." + string(os.PathListSeparator) + f.config.Path
				case "empty-search-path":
					f.config.Path = string(os.PathListSeparator) + f.config.Path
				case "array-and-args":
					definition["args"] = []any{}
				case "invalid-args":
					definition["command"], definition["args"] = "fixture-server", "SECRET_ARG"
				case "nonstring-arg":
					definition["command"], definition["args"] = "fixture-server", []any{42}
				case "shadowed-executable":
					shadow := filepath.Join(f.home, "client-bin")
					f.write(filepath.Join(shadow, "fixture-server"), "unrelated executable", 0755)
					f.config.Path = shadow + string(os.PathListSeparator) + f.config.Path
				case "npx", "ambiguous-npx", "npm-exec", "node-wrapper":
					command := "npx"
					args := []any{"-y", "fixture-server@1.0.0", "SECRET_ARG"}
					if scenario == "ambiguous-npx" {
						args = []any{"--package", "fixture-server", "--package", "other-server", "fixture-server"}
					} else if scenario == "npm-exec" {
						command, args = "npm", []any{"exec", "--", "fixture-server"}
					} else if scenario == "node-wrapper" {
						command, args = "node", []any{filepath.Join(f.pkg, "serve.js")}
					}
					f.write(filepath.Join(f.prefix, "bin", command), "unrelated launcher; never executed", 0755)
					definition["command"] = append([]any{command}, args...)
				}
				f.definition(definition)
				report, server := f.report(check)
				if (len(server.Related) == 1) != wantOwner || !wantOwner && server.Update.Code == "use_installation_owner" {
					t.Fatal("incorrect installed owner", server)
				}
				plan := f.plan(report, server)
				item := plan.Items[0]
				if wantOwner {
					if item.Status != "ready" || item.Provider != "npm" || item.Binding.PackageName != "fixture-server" || item.TargetVersion != "2.0.0" || f.queries.Load() == 0 {
						t.Fatal("verified owner update regressed", item)
					}
				} else if item.Status != "manual" || item.Provider == "npm" || f.queries.Load() != 0 {
					t.Fatal("unrelated package acquired an executable preview or owner query", item, f.queries.Load())
				}
			})
		}
	}
}

func TestMCPSubsetEnvironmentDoesNotBorrowScannerPATH(t *testing.T) {
	for _, tc := range []struct {
		name, ext, body string
		unknownEnv      bool
	}{
		{"toml-plain", ".toml", "[mcp_servers.fixture]\ncommand=%q\n", false},
		{"toml-env-table", ".toml", "[mcp_servers.fixture]\ncommand=%q\n[mcp_servers.fixture.env]\nPATH='/SECRET_CLIENT_PATH'\n", true},
		{"toml-env-comment", ".toml", "[mcp_servers.fixture]\ncommand=%q\n[mcp_servers.fixture.env] # environment\nPATH='/SECRET_CLIENT_PATH'\n", true},
		{"toml-env-before-command", ".toml", "[mcp_servers.fixture.env]\nPATH='/SECRET_CLIENT_PATH'\n[mcp_servers.fixture]\ncommand=%q\n", true},
		{"toml-inline-env", ".toml", "[mcp_servers.fixture]\ncommand=%q\nenv={PATH='/SECRET_CLIENT_PATH'}\n", true},
		{"toml-dotted-env", ".toml", "[mcp_servers.fixture]\ncommand=%q\nenv . PATH='/SECRET_CLIENT_PATH'\n", true},
		{"toml-dotted-quoted-path", ".toml", "[mcp_servers.fixture]\ncommand=%q\nenv . \"PATH\"='/SECRET_CLIENT_PATH'\n", true},
		{"toml-dotted-quoted-env", ".toml", "[mcp_servers.fixture]\ncommand=%q\n\"env\" . \"PATH\"='/SECRET_CLIENT_PATH'\n", true},
		{"toml-escaped-env", ".toml", "[mcp_servers.fixture]\ncommand=%q\n\"\\u0065nv\"={PATH='/SECRET_CLIENT_PATH'}\n", true},
		{"toml-env-vars", ".toml", "[mcp_servers.fixture]\ncommand=%q\nenv_vars=['PATH']\n", true},
		{"yaml-env", ".yaml", "mcp_servers:\n  fixture:\n    command: %q\n    env:\n      PATH: /SECRET_CLIENT_PATH\n", true},
	} {
		for _, absolute := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/absolute=%t", tc.name, absolute), func(t *testing.T) {
				f := newMCPOwnerFixture(t)
				command := "fixture-server"
				if absolute {
					command = f.command
				}
				f.config.MCPConfigs[0] = filepath.Join(f.home, "fixture"+tc.ext)
				f.write(f.config.MCPConfigs[0], fmt.Sprintf(tc.body, command), 0600)
				report, server := f.report(false)
				wantOwner := absolute || !tc.unknownEnv
				if (len(server.Related) == 1) != wantOwner {
					t.Fatal("subset environment lost its uncertainty", server)
				}
				plan := f.plan(report, server)
				wantReady := wantOwner && tc.ext == ".toml"
				if (plan.Items[0].Status == "ready") != wantReady {
					t.Fatal("subset configuration changed owner support", plan.Items)
				}
			})
		}
	}
}

func TestMCPManagerLauncherScriptsNeverBecomeServerOwners(t *testing.T) {
	for _, tc := range []struct{ owner, command, script string }{
		{"npm", "npx", "npx-cli.js"}, {"npm", "npm", "npm-cli.js"}, {"pnpm", "pnpm", "pnpm.cjs"},
	} {
		t.Run(tc.script, func(t *testing.T) {
			f := newMCPOwnerFixture(t)
			f.pkg = filepath.Join(f.prefix, "lib/node_modules", tc.owner)
			f.write(filepath.Join(f.pkg, "package.json"), fmt.Sprintf(`{"name":%q,"version":"1.0.0","bin":{%q:%q,"fixture-server":%q}}`, tc.owner, tc.command, "bin/"+tc.script, "bin/"+tc.script), 0644)
			command := filepath.Join(f.pkg, "bin", tc.script)
			f.write(command, "#!/usr/bin/env node\n// controlled launcher; never executed\n", 0755)
			args := []any{"-y", "fixture-server@1.0.0"}
			if tc.command == "npm" {
				args = []any{"exec", "--", "fixture-server@1.0.0"}
			} else if tc.command == "pnpm" {
				args = []any{"dlx", "fixture-server@1.0.0"}
			}
			f.definition(map[string]any{"command": command, "args": args})
			report, server := f.report(false)
			if len(server.Related) != 0 {
				t.Fatal("launcher confused with the server package", server)
			}
			plan := f.plan(report, server)
			if plan.Items[0].Status != "manual" || f.queries.Load() != 0 {
				t.Fatal("launcher package acquired an owner update", plan.Items, f.queries.Load())
			}
		})
	}
}

func TestMCPTOMLCommandAndArgumentKeys(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{"quoted-keys", "\"command\"=%q\n\"args\"=[\"--option\",\"SECRET_ARG\"]\n", true},
		{"literal-keys-empty-args", "'command'=%q\n'args'=[]\n", true},
		{"quoted-key-with-dot", "command=%q\n\"other.key\"=\"ignored\"\n", true},
		{"unterminated-literal-key", "'command=%q\n", false},
		{"repeated-literal-quotes", "''command''=%q\n", false},
		{"invalid-basic-key", "\"comm\\qand\"=%q\n", false},
		{"quoted-nonarray-args", "command=%q\n\"args\"=42\n", false},
		{"null-args", "command=%q\nargs=null\n", false},
		{"mixed-args", "command=%q\nargs=[\"SECRET_ARG\",42]\n", false},
		{"conflicting-command", "command=%q\n\"command\"=\"other\"\n", false},
		{"conflicting-quoted-command", "\"command\"=%q\ncommand=\"other\"\n", false},
		{"duplicate-args", "command=%q\n\"args\"=[]\nargs=[]\n", false},
		{"invalid-command-before-quoted", "command=42\n\"command\"=%q\n", false},
	} {
		for _, absolute := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/absolute=%t", tc.name, absolute), func(t *testing.T) {
				f := newMCPOwnerFixture(t)
				command := "fixture-server"
				if absolute {
					command = f.command
				}
				f.config.MCPConfigs[0] = filepath.Join(f.home, "fixture.toml")
				f.write(f.config.MCPConfigs[0], "[mcp_servers.fixture]\n"+fmt.Sprintf(tc.fields, command), 0600)
				report, server := f.report(false)
				if (len(server.Related) == 1) != tc.valid {
					t.Fatal("TOML keys or argument types changed the owner proof", server)
				}
				plan := f.plan(report, server)
				if (plan.Items[0].Status == "ready") != tc.valid || !tc.valid && f.queries.Load() != 0 {
					t.Fatal("invalid TOML acquired an executable owner preview", plan.Items, f.queries.Load())
				}
			})
		}
	}
}

func TestMCPDeclaredLauncherDoesNotFallBackToHomebrew(t *testing.T) {
	f := newMCPOwnerFixture(t)
	brew := filepath.Join(f.home, "brew")
	keg := filepath.Join(brew, "Cellar/node/24.0.0")
	f.write(filepath.Join(keg, "INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"}}`, 0644)
	if err := os.MkdirAll(filepath.Join(brew, "opt"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(keg, filepath.Join(brew, "opt/node")); err != nil {
		t.Fatal(err)
	}
	f.pkg = filepath.Join(keg, "lib/node_modules/npm")
	f.write(filepath.Join(f.pkg, "package.json"), `{"name":"npm","version":"1.0.0","bin":{"npx":"bin/launcher.js","server-alias":"bin/launcher.js"}}`, 0644)
	f.write(filepath.Join(f.pkg, "bin/launcher.js"), "controlled launcher; never executed", 0755)
	f.config.BrewRoots = []string{brew}
	f.definition(map[string]any{"command": filepath.Join(brew, "opt/node/lib/node_modules/npm/bin/launcher.js"), "args": []any{"fixture-server"}})
	report, server := f.report(false)
	formulaFound := false
	for _, item := range report.Items {
		formulaFound = formulaFound || item.Kind == "formula" && item.Name == "node" && item.ActiveVersion == "24.0.0"
	}
	if !formulaFound || len(server.Related) != 0 {
		t.Fatal("declared launcher fell back to its enclosing formula", server)
	}
	plan := f.plan(report, server)
	if plan.Items[0].Status != "manual" || f.queries.Load() != 0 {
		t.Fatal("runtime owner became an executable server update", plan.Items)
	}
}

func TestMCPOwnerDriftRejectsPlanAndApply(t *testing.T) {
	for _, scenario := range []string{"config", "entry-target", "entry-mode", "earlier-path-entry", "package-bin", "package-version", "manager-content"} {
		for _, phase := range []string{"plan", "apply"} {
			t.Run(scenario+"/"+phase, func(t *testing.T) {
				f := newMCPOwnerFixture(t)
				shadow := filepath.Join(f.home, "earlier-bin")
				f.config.Path = shadow + string(os.PathListSeparator) + f.config.Path
				f.definition(map[string]any{"command": []any{"fixture-server"}})
				report, server := f.report(false)
				plan := f.plan(report, server)
				if plan.Items[0].Status != "ready" {
					t.Fatal("baseline owner preview failed", plan.Items)
				}
				switch scenario {
				case "config":
					f.definition(map[string]any{"command": []any{"fixture-server"}, "environment": map[string]any{"PATH": "/SECRET_CLIENT_PATH"}})
				case "entry-target":
					target := filepath.Join(f.home, "unrelated")
					f.write(target, "unrelated command", 0755)
					if err := os.Remove(f.command); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, f.command); err != nil {
						t.Fatal(err)
					}
				case "entry-mode":
					if err := os.Chmod(filepath.Join(f.pkg, "serve.js"), 0644); err != nil {
						t.Fatal(err)
					}
				case "earlier-path-entry":
					f.write(filepath.Join(shadow, "fixture-server"), "unrelated command", 0755)
				case "package-bin":
					f.write(filepath.Join(f.pkg, "package.json"), `{"name":"fixture-server","version":"1.0.0","bin":{"fixture-server":"other.js"}}`, 0644)
				case "package-version":
					f.write(filepath.Join(f.pkg, "package.json"), `{"name":"fixture-server","version":"3.0.0","bin":{"fixture-server":"serve.js"}}`, 0644)
				case "manager-content":
					f.write(f.options.ManagerPaths["npm"], "changed manager", 0755)
				}
				if phase == "plan" {
					// Retain the previously resolved target, as a change may occur after its query.
					options := f.options
					options.ResolvedTargets = map[string]manage.ResolvedTarget{plan.Items[0].Binding.TargetID: *plan.Items[0].Binding.ResolvedTarget}
					changed, err := manage.NewPlan(report, plan.Selection, options)
					if err != nil || changed.Items[0].Status == "ready" {
						t.Fatal("stale owner evidence produced an executable preview", changed.Items, err)
					}
					return
				}
				var mutations atomic.Int32
				engine := manage.Engine{
					Options: f.options, StateDir: filepath.Join(f.home, "state"), Now: func() time.Time { return f.options.Now.Add(time.Second) },
					Snapshot:       func(context.Context) (audit.Report, error) { fresh, _ := f.report(false); return fresh, nil },
					ResolveTargets: (resolve.Resolver{Options: f.options, Runner: f, Timeout: time.Second}).Resolve,
					Runner:         mcpMutationFunc(func() { mutations.Add(1) }),
				}
				if _, err := manage.Apply(context.Background(), plan, plan.PlanID, engine); err == nil || mutations.Load() != 0 {
					t.Fatal("changed binding was not rejected before execution", err, mutations.Load())
				}
			})
		}
	}
}

type mcpMutationFunc func()

func (f mcpMutationFunc) Run(context.Context, manage.Command) ([]byte, error) {
	f()
	return nil, fmt.Errorf("unexpected mutation")
}

func TestMCPPlanRequiresUniqueCurrentBinding(t *testing.T) {
	for _, scenario := range []string{"missing-hash", "duplicate-hash", "invalid-hash", "duplicate-entry", "relative-entry", "missing-lookup", "duplicate-owner", "decoded-path-report"} {
		t.Run(scenario, func(t *testing.T) {
			f := newMCPOwnerFixture(t)
			f.definition(map[string]any{"command": []any{"fixture-server"}})
			report, server := f.report(false)
			switch scenario {
			case "duplicate-owner":
				for _, item := range report.Items {
					if item.Kind == "node-package" {
						item.ID = "duplicate-owner"
						report.Items = append(report.Items, item)
						break
					}
				}
			case "decoded-path-report":
				data, err := json.Marshal(report)
				var decoded audit.Report
				if err != nil || json.Unmarshal(data, &decoded) != nil {
					t.Fatal("report JSON round trip failed", err)
				}
				report = decoded
			default:
				var evidence []string
				for _, e := range server.Evidence {
					switch {
					case scenario == "missing-hash" && strings.HasPrefix(e, "config-sha256:"), scenario == "missing-lookup" && strings.HasPrefix(e, "command-lookup:"):
						continue
					case scenario == "invalid-hash" && strings.HasPrefix(e, "config-sha256:"):
						e = "config-sha256:invalid"
					case scenario == "relative-entry" && strings.HasPrefix(e, "command-entry:"):
						e = "command-entry:./fixture-server"
					case scenario == "duplicate-hash" && strings.HasPrefix(e, "config-sha256:"), scenario == "duplicate-entry" && strings.HasPrefix(e, "command-entry:"):
						evidence = append(evidence, e)
					}
					evidence = append(evidence, e)
				}
				server.Evidence = evidence
				for n := range report.Items {
					if report.Items[n].ID == server.ID {
						report.Items[n] = server
					}
				}
			}
			plan := f.plan(report, server)
			if plan.Items[0].Status != "manual" {
				t.Fatal("unverifiable MCP binding accepted", plan.Items)
			}
		})
	}
}
