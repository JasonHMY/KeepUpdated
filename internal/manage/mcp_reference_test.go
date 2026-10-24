package manage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func mcpReferenceFixture(t *testing.T, launcher string) (*fixture, audit.Item, string) {
	f := setup(t)
	path := filepath.Join(f.home, ".cursor/mcp.json")
	args := []string{"fixture@1.0.0", "SECRET_ARG"}
	if launcher == "npx" {
		args = append([]string{"-y"}, args...)
	} else {
		args = append(args, "--bun", "--package", "other", "-p", "other", "--install", "fixture@1.0.0")
	}
	definition := map[string]any{"mcpServers": map[string]any{"fixture": map[string]any{"command": launcher, "args": args, "env": map[string]string{"TOKEN": "SECRET_ENV"}}}, "keep": 42}
	data, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	f.write(path, string(data), 0600)
	item := audit.Item{ID: "server", Name: "fixture", Kind: "mcp-server", Owner: "configuration", Source: "local-command", Path: url.PathEscape(path) + "#user-or-explicit-project#fixture"}
	f.r.Items = []audit.Item{item}
	return f, item, path
}

func TestMCPReferencePlanAndApplyKeepServerWithoutRunningManager(t *testing.T) {
	for _, launcher := range []string{"npx", "bunx", "/tools/bunx"} {
		t.Run(launcher, func(t *testing.T) {
			f, item, path := mcpReferenceFixture(t, launcher)
			original, _ := os.ReadFile(path)
			ordinary := f.plan(Selection{Action: "update", IDs: []string{item.ID}})
			if ordinary.Items[0].Status == "ready" {
				t.Fatal("pin migrated without request")
			}
			all := f.plan(Selection{Action: "update", All: true})
			if all.Items[0].Status == "ready" {
				t.Fatal("all update migrated reference")
			}
			p := f.plan(Selection{Action: "update", IDs: []string{item.ID}, Special: true, MCPReference: "fixture@2.0.0"})
			pi := p.Items[0]
			if pi.Status != "ready" || pi.Provider != "mcp-config" || pi.MCP.PackageReference.Before != "fixture@1.0.0" || pi.TargetVersion != "" {
				t.Fatal(pi)
			}
			encoded, _ := json.Marshal(p)
			if strings.Contains(string(encoded), "SECRET") {
				t.Fatal("configuration values leaked")
			}
			e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) { t.Fatal("launcher or manager ran"); return nil, nil }))
			if _, err := Apply(context.Background(), p, "wrong-plan-id", e); err == nil {
				t.Fatal("missing exact confirmation accepted")
			}
			out, err := Apply(context.Background(), p, p.PlanID, e)
			if err != nil || len(out.Results) != 1 || out.Results[0].Status != "configured" || out.Results[0].AfterVersion != "" || !out.Results[0].Changed || out.Results[0].Reason != "package_reference_changed_server_not_started" {
				t.Fatal(out, err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != strings.Replace(string(original), "fixture@1.0.0", "fixture@2.0.0", 1) {
				t.Fatal(string(got))
			}
			resultJSON, _ := json.Marshal(out)
			if strings.Contains(string(resultJSON), "SECRET") {
				t.Fatal("result leaked arguments")
			}
			backup, _ := os.ReadFile(out.Results[0].BackupPath)
			if string(backup) != string(original) {
				t.Fatal("missing original backup")
			}
			if _, err := Apply(context.Background(), p, p.PlanID, e); err == nil {
				t.Fatal("replay accepted")
			}
		})
	}
}

func TestMCPReferenceRequiresNarrowExplicitSelection(t *testing.T) {
	for _, launcher := range []string{"npx", "bunx", "/tools/bunx"} {
		t.Run(launcher, func(t *testing.T) {
			f, item, _ := mcpReferenceFixture(t, launcher)
			for _, s := range []Selection{
				{Action: "update", All: true, Special: true, MCPReference: "fixture@2.0.0"},
				{Action: "update", IDs: []string{item.ID}, MCPReference: "fixture@2.0.0"},
				{Action: "uninstall", IDs: []string{item.ID}, Special: true, MCPReference: "fixture@2.0.0"},
				{Action: "update", IDs: []string{item.ID, "other"}, Special: true, MCPReference: "fixture@2.0.0"},
			} {
				if _, err := NewPlan(f.r, s, f.o); err == nil {
					t.Fatal("broad migration accepted", s)
				}
			}
			for _, ref := range []string{"other@2.0.0", "fixture@latest", "https://example.test/pkg"} {
				p := f.plan(Selection{Action: "update", IDs: []string{item.ID}, Special: true, MCPReference: ref})
				if p.Items[0].Status == "ready" {
					t.Fatal("source change accepted", ref)
				}
			}
		})
	}
}

func TestMCPReferenceRejectsChangedConfigAndPostcheck(t *testing.T) {
	for _, launcher := range []string{"npx", "bunx", "/tools/bunx"} {
		t.Run(launcher, func(t *testing.T) {
			for _, phase := range []string{"before", "preflight", "post", "post-unreadable"} {
				t.Run(phase, func(t *testing.T) {
					f, item, path := mcpReferenceFixture(t, launcher)
					p := f.plan(Selection{Action: "update", IDs: []string{item.ID}, Special: true, MCPReference: "fixture@2.0.0"})
					e := f.engine(runFunc(func(context.Context, Command) ([]byte, error) { t.Fatal("manager ran"); return nil, nil }))
					calls := 0
					e.Snapshot = func(context.Context) (audit.Report, error) {
						calls++
						if phase == "post-unreadable" && calls == 3 {
							return audit.Report{}, errors.New("snapshot unavailable")
						}
						if phase == "before" && calls == 1 || phase == "preflight" && calls == 2 || phase == "post" && calls == 3 {
							f.write(path, `{"mcpServers":{"fixture":{"command":"npx","args":["other@1.0.0"]}}}`, 0600)
						}
						return f.r, nil
					}
					result, err := Apply(context.Background(), p, p.PlanID, e)
					if phase == "before" {
						if err == nil {
							t.Fatal("stale plan accepted")
						}
						return
					}
					if err != nil || len(result.Results) != 1 {
						t.Fatal(result, err)
					}
					want := "blocked"
					if phase == "post" || phase == "post-unreadable" {
						want = "verification_unknown"
					}
					if result.Results[0].Status != want {
						t.Fatal(result)
					}
				})
			}
		})
	}
}

func TestMCPPythonOwnerPlanUsesManagerAndRetainsPinGuards(t *testing.T) {
	for _, provider := range []string{"pipx", "uv"} {
		t.Run(provider, func(t *testing.T) {
			f := setup(t)
			owner, scope, command := f.mcpPythonOwner(provider)
			path := filepath.Join(f.home, "mcp.json")
			f.write(path, fmt.Sprintf(`{"mcpServers":{"fixture":{"command":%q}}}`, command), 0600)
			server := audit.Item{ID: "server", Name: "fixture", Kind: "mcp-server", Owner: "configuration", Path: url.PathEscape(path) + "#user-or-explicit-project#fixture", Related: []string{owner.ID}}
			f.bindMCPServer(&server, path, command)
			f.r.Items = append(f.r.Items, server)
			p := f.plan(Selection{Action: "update", IDs: []string{server.ID}})
			if p.Items[0].Provider != provider || p.Items[0].Status != "ready" || p.Items[0].Binding.TargetID != owner.ID || p.Items[0].MCP == nil {
				t.Fatal(p.Items)
			}
			if provider == "pipx" {
				f.editPipx(scope, func(_, main map[string]any) { main["package_or_url"] = "example-tool==1.0.0" })
			} else {
				receipt := filepath.Join(scope, "uv-receipt.toml")
				data, err := os.ReadFile(receipt)
				if err != nil {
					t.Fatal(err)
				}
				f.write(receipt, strings.Replace(string(data), `extras = ["feature"]`, `specifier = "==1.0.0"`, 1), 0644)
			}
			pinned := f.plan(Selection{Action: "update", IDs: []string{server.ID}})
			if pinned.Items[0].Status == "ready" || pinned.Items[0].Reason == "mcp_update_requires_verified_installation_owner" {
				t.Fatal("pinned owner accepted")
			}
			duplicate := owner
			duplicate.ID = "duplicate"
			server.Related = append(server.Related, duplicate.ID)
			f.r.Items = []audit.Item{owner, duplicate, server}
			ambiguous := f.plan(Selection{Action: "update", IDs: []string{server.ID}})
			if ambiguous.Items[0].Reason != "mcp_installation_owner_ambiguous" {
				t.Fatal(ambiguous.Items)
			}
		})
	}
}

func TestMCPPipxPinnedFlagRemainsManual(t *testing.T) {
	f := setup(t)
	owner, scope, command := f.mcpPythonOwner("pipx")
	receipt := filepath.Join(scope, "pipx_metadata.json")
	data, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	f.write(receipt, strings.Replace(string(data), `"suffix":""`, `"suffix":"","pinned":true`, 1), 0644)
	path := filepath.Join(f.home, "mcp.json")
	f.write(path, fmt.Sprintf(`{"mcpServers":{"fixture":{"command":%q}}}`, command), 0600)
	server := audit.Item{ID: "server", Name: "fixture", Kind: "mcp-server", Owner: "configuration", Path: url.PathEscape(path) + "#user-or-explicit-project#fixture", Related: []string{owner.ID}}
	f.bindMCPServer(&server, path, command)
	f.r.Items = append(f.r.Items, server)
	p := f.plan(Selection{Action: "update", IDs: []string{server.ID}})
	if p.Items[0].Status != "manual" || p.Items[0].Reason != "owner_version_pinned" {
		t.Fatal(p.Items)
	}
}

func (f *fixture) bindMCPServer(server *audit.Item, config, command string) {
	f.t.Helper()
	data, err := os.ReadFile(config)
	if err != nil {
		f.t.Fatal(err)
	}
	server.RealPath, err = filepath.EvalSymlinks(command)
	if err != nil {
		f.t.Fatal(err)
	}
	server.Evidence = []string{"config-sha256:" + hashText(string(data)), "command-entry:" + command, "command-lookup:absolute"}
}

func (f *fixture) mcpPythonOwner(provider string) (audit.Item, string, string) {
	f.t.Helper()
	owner, scope := f.pythonTool(provider, "example-tool")
	owner.RealPath = owner.Path
	f.r.Items[0] = owner
	command := f.pythonEntry(owner, scope)
	f.bindPythonPolicy(owner, scope, "update")
	return owner, scope, command
}
