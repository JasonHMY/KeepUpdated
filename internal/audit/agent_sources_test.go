package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMultiAgentMCPStaticSourcesAndSecretIsolation(t *testing.T) {
	s := fixture(t)
	bin := filepath.Join(s.Config.Home, "bin", "synthetic-server")
	write(t, bin, "#!/bin/sh\ntouch SHOULD_NOT_RUN\n")
	if err := os.Chmod(bin, 0755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s.Config.Home, ".config/opencode/opencode.jsonc"), `{
 // synthetic OpenCode config
 "mcp": {
  "code-local": {"type":"local","command":["synthetic-server","SECRET_ARG"],"environment":{"TOKEN":"SECRET_ENV"}},
  "code-remote": {"type":"remote","url":"https://user:SECRET_URL@example.invalid","headers":{"Authorization":"SECRET_HEADER"}},
  "invalid-array": {"type":"local","command":["synthetic-server",42]},
 },
}`)
	write(t, filepath.Join(s.Config.Home, ".openclaw/openclaw.json"), `{"mcp":{"servers":{"claw-local":{"command":"synthetic-server","args":["SECRET_ARG"]}}},"nodeHost":{"mcp":{"servers":{"claw-local":{"command":"synthetic-server"}}}}}`)
	write(t, filepath.Join(s.Config.Home, ".hermes/config.yaml"), "mcp_servers:\n  hermes-local:\n    command: synthetic-server\n    args:\n      - SECRET_ARG\n    env:\n      TOKEN: SECRET_ENV\n  hermes-remote:\n    url: https://example.invalid/SECRET_URL\n")
	s.configs()
	servers, configs := 0, 0
	for _, i := range s.Report.Items {
		switch i.Kind {
		case "mcp-config":
			configs++
			if strings.HasSuffix(i.Path, ".yaml") && i.Health.Status != "unknown" {
				t.Fatal("YAML subset claimed complete", i)
			}
		case "mcp-server":
			servers++
			if i.Name == "invalid-array" && i.Health.Code != "command_syntax_unsupported" {
				t.Fatal("unsupported command array hidden", i)
			}
		}
	}
	if servers != 7 || configs != 3 {
		t.Fatalf("sources lost: servers=%d configs=%d, %+v", servers, configs, s.Report)
	}
	if len(s.Report.Queries) != 0 {
		t.Fatal("discovery executed a command", s.Report.Queries)
	}
	b, err := json.Marshal(s.Report)
	if err != nil || strings.Contains(string(b), "SECRET_") || strings.Contains(string(b), "SHOULD_NOT_RUN") {
		t.Fatal("configuration arguments or secrets escaped")
	}
}

func TestHermesMCPArgumentListsDoNotHideStableCommands(t *testing.T) {
	for _, indent := range []string{"    ", "      "} {
		t.Run(fmt.Sprint(len(indent)), func(t *testing.T) {
			s := fixture(t)
			command := filepath.Join(s.Config.Home, "bin", "synthetic-server")
			write(t, command, "#!/bin/sh\ntouch SHOULD_NOT_RUN\n")
			if err := os.Chmod(command, 0755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(s.Config.Home, ".hermes/config.yaml"), "mcp_servers:\n  example:\n    command: "+command+"\n    args:\n"+indent+"- SECRET_ARG\n    env:\n      TOKEN: SECRET_ENV\n    enabled: true\n")
			s.configs()
			servers := 0
			for _, i := range s.Report.Items {
				if i.Kind == "mcp-server" {
					servers++
					if i.Health.Code != "command_resolved_not_started" || i.RealPath != real(command) {
						t.Fatal("legal argument list hid a static command", i)
					}
				}
			}
			data, _ := json.Marshal(s.Report)
			if servers != 1 || len(s.Report.Queries) != 0 || strings.Contains(string(data), "SECRET_") || strings.Contains(string(data), "SHOULD_NOT_RUN") {
				t.Fatal("server discovery lost its no-execution or secret-isolation boundary")
			}
		})
	}
}

func TestHermesMCPOrphanArgumentListsRemainUnknown(t *testing.T) {
	for _, fields := range []string{
		"    command: synthetic-server\n    - SECRET_ARG\n",
		"    command: synthetic-server\n    args:\n    - SECRET_ARG\n    enabled: true\n    - SECRET_ORPHAN\n",
	} {
		servers, _, partial := yamlAgentSources("mcp_servers:\n  example:\n" + fields)
		entry, ok := servers["example"].(map[string]any)
		if !ok || !partial || entry["_parse_error"] != true {
			t.Fatal("orphan list was accepted as a server field")
		}
	}
}

func TestMCPNetworkLocationEvidencePreservesDisabledStateAndSecrets(t *testing.T) {
	cases := []struct {
		name, address, location string
	}{
		{"localhost", "http://localhost:12345/SECRET_PATH", "loopback"},
		{"localhost-case", "https://LOCALHOST/SECRET_PATH", "loopback"},
		{"ipv4-loopback", "http://127.10.20.30/SECRET_PATH", "loopback"},
		{"ipv6-loopback", "http://[::1]:12345/SECRET_PATH", "loopback"},
		{"mapped-loopback", "http://[::ffff:127.0.0.1]/SECRET_PATH", "loopback"},
		{"remote", "https://example.invalid/SECRET_PATH?token=SECRET_QUERY", "remote"},
		{"localhost-subdomain", "https://localhost.example.invalid/SECRET_PATH", "remote"},
		{"credentials", "https://user:SECRET_PASSWORD@example.invalid/SECRET_PATH", "unknown"},
		{"relative", "/SECRET_PATH", "unknown"},
		{"missing-host", "https:///SECRET_PATH", "unknown"},
		{"bad-host", "https://bad host/SECRET_PATH", "unknown"},
		{"bad-brackets", "https://[localhost]/SECRET_PATH", "unknown"},
		{"bad-ipv4", "https://127.0.0.999/SECRET_PATH", "unknown"},
		{"empty-label", "https://example..invalid/SECRET_PATH", "unknown"},
		{"bad-port", "https://example.invalid:99999/SECRET_PATH", "unknown"},
		{"expansion", "https://${SECRET_HOST}/SECRET_PATH", "unknown"},
		{"wrong-scheme", "file:///SECRET_PATH", "unknown"},
	}
	for _, tc := range cases {
		for _, enabled := range []bool{true, false} {
			for _, field := range []string{"url", "serverUrl"} {
				t.Run(fmt.Sprintf("%s/%t/%s", tc.name, enabled, field), func(t *testing.T) {
					s := fixture(t)
					s.server("synthetic.json", "mcp", "example", map[string]any{field: tc.address, "enabled": enabled, "headers": map[string]any{"Authorization": "SECRET_HEADER"}}, "")
					if len(s.Report.Items) != 1 {
						t.Fatal("network entry lost")
					}
					i := s.Report.Items[0]
					health := "remote_not_contacted"
					if !enabled {
						health = "disabled_not_started"
					}
					if i.Source != "remote" || i.Health.Code != health || i.Update.Code != "remote_not_locally_managed" || !slices.Contains(i.Evidence, "endpoint:"+tc.location) {
						t.Fatal("network type, disabled state or location evidence differs", i)
					}
					data, _ := json.Marshal(s.Report)
					if len(s.Report.Queries) != 0 || strings.Contains(string(data), "SECRET_") || strings.Contains(string(data), tc.address) {
						t.Fatal("network discovery contacted the server or exposed connection data")
					}
				})
			}
		}
	}
}

func TestExplicitAgentSourcesAndDefaultSkills(t *testing.T) {
	s := fixture(t)
	extraRoot := filepath.Join(s.Config.Home, "custom-agent", "skills")
	extraConfig := filepath.Join(s.Config.Home, "custom-agent", "servers.json")
	s.Config.SkillRoots = []string{extraRoot, "~/missing-synthetic-skills"}
	s.Config.MCPConfigs = []string{extraConfig}
	write(t, extraConfig, `{"mcpServers":{"custom":{"command":"missing-synthetic"}}}`)
	for _, root := range []string{".config/opencode/skills", ".openclaw/skills", ".hermes/skills", "custom-agent/skills"} {
		write(t, filepath.Join(s.Config.Home, root, "fixture", "SKILL.md"), "---\nname: fixture\ndescription: synthetic\n---\n")
	}
	project := filepath.Join(s.Config.Home, "project")
	s.Config.Projects = []string{project}
	write(t, filepath.Join(project, ".opencode/skills/project-skill/SKILL.md"), "---\nname: project-skill\n---\n")
	write(t, filepath.Join(project, "opencode.json"), `{"mcp":{"project-server":{"type":"local","command":["missing-synthetic"]}}}`)
	s.configs()
	s.skills()
	skills, servers, missing := 0, 0, 0
	for _, i := range s.Report.Items {
		if i.Kind == "skill" {
			skills++
		}
		if i.Kind == "mcp-server" {
			servers++
		}
	}
	for _, d := range s.Report.Diagnostics {
		if d.Code == "source_path_missing" {
			missing++
		}
	}
	if skills != 5 || servers != 2 || missing != 1 {
		t.Fatalf("sources lost or duplicate diagnostics: skills=%d servers=%d missing=%d", skills, servers, missing)
	}
}

func TestHermesExternalDirsAndComplexServerRemainUnknown(t *testing.T) {
	s := fixture(t)
	extra := filepath.Join(s.Config.Home, "shared", "skills")
	write(t, filepath.Join(extra, "linked", "SKILL.md"), "---\nname: linked\n---\n")
	write(t, filepath.Join(s.Config.Home, ".hermes/config.yaml"), `skills:
  external_dirs: # only a static path list
    - '~/shared/skills'
    - '${SYNTHETIC_SECRET_PATH}/skills'
mcp_servers:
  before:
    command: "synthetic-before" # supported scalar
  anchored:
    <<: *SECRET_ALIAS
    command: synthetic-anchored
  after:
    url: 'https://example.invalid/SECRET_URL'
  flow: {command: synthetic-flow}
`)
	s.configs()
	s.skills()
	count, unknown, roots := 0, 0, 0
	for _, i := range s.Report.Items {
		if i.Kind == "skill" {
			roots++
		}
		if i.Kind == "mcp-server" {
			count++
			if i.Name == "anchored" || i.Name == "flow" {
				if i.Health.Status != "unknown" || i.Health.Code != "server_syntax_unsupported" {
					t.Fatal("complex server claimed valid", i)
				}
				unknown++
			}
		}
	}
	if count != 4 || unknown != 2 || roots != 1 {
		t.Fatal("partial YAML discovery lost known entries", count, unknown, roots)
	}
	found := false
	for _, d := range s.Report.Diagnostics {
		if d.Code == "skill_root_requires_absolute_path" {
			found = true
		}
	}
	if !found {
		t.Fatal("unsupported path expansion hidden")
	}
	b, _ := json.Marshal(s.Report)
	if strings.Contains(string(b), "SECRET_") {
		t.Fatal("YAML secrets escaped")
	}
}

func TestOpenClawJSON5IsNotSilentlyAcceptedAsJSONC(t *testing.T) {
	s := fixture(t)
	write(t, filepath.Join(s.Config.Home, ".openclaw/openclaw.json"), "{mcp: {servers: {example: {command: 'node'}}}}")
	s.configs()
	if len(s.Report.Items) != 1 || s.Report.Items[0].Health.Status != "unknown" || s.Report.Items[0].Health.Code != "json5_syntax_unsupported_or_invalid" {
		t.Fatal("JSON5 parse limit hidden", s.Report)
	}
	if len(s.Report.Diagnostics) != 1 {
		t.Fatal("missing JSON5 diagnostic", s.Report.Diagnostics)
	}
}

func TestMalformedJSONCCommentsAndEmptyCommasStayUnknown(t *testing.T) {
	for _, text := range []string{`{"mcp":{}} /* unterminated`, `{"mcp":{,}}`, `{"mcp":{"bad":{"command":[,]}}}`} {
		s := fixture(t)
		write(t, filepath.Join(s.Config.Home, ".openclaw/openclaw.json"), text)
		s.configs()
		if len(s.Report.Items) != 1 || s.Report.Items[0].Health.Status == "ok" {
			t.Fatal("malformed JSONC reported parsed", s.Report)
		}
	}
}

func TestArrayCommandDoesNotExecuteExpansion(t *testing.T) {
	s := fixture(t)
	s.server("synthetic.json", "mcp", "unsafe", map[string]any{"command": []any{"$(touch SECRET_SENTINEL)", "SECRET_ARG"}}, "")
	if len(s.Report.Items) != 1 || s.Report.Items[0].Health.Code != "command_requires_expansion" || len(s.Report.Queries) != 0 {
		t.Fatal(s.Report)
	}
	b, _ := json.Marshal(s.Report)
	if strings.Contains(string(b), "SECRET_") {
		t.Fatal("untrusted command escaped")
	}
}

func TestSameNameMCPServersInDifferentSchemasRemainDistinct(t *testing.T) {
	s := fixture(t)
	p := filepath.Join(s.Config.Home, "custom.json")
	s.Config.MCPConfigs = []string{p}
	write(t, p, `{"mcpServers":{"same":{"command":"one"}},"servers":{"same":{"command":"two"}},"mcp":{"same":{"command":["three"]}},"nodeHost":{"mcp":{"servers":{"same":{"command":"four"}}}}}`)
	s.configs()
	count := 0
	ids := map[string]bool{}
	for _, i := range s.Report.Items {
		if i.Kind == "mcp-server" {
			count++
			ids[i.ID] = true
		}
	}
	if count != 4 || len(ids) != 4 {
		t.Fatal("same-name definition in another config schema was dropped", s.Report)
	}
}

func TestSkillRootAliasAndLoopDoNotLoseScope(t *testing.T) {
	s := fixture(t)
	source := filepath.Join(s.Config.Home, "original", "fixture")
	write(t, filepath.Join(source, "SKILL.md"), "---\nname: fixture\n---\n")
	alias := filepath.Join(s.Config.Home, ".openclaw/skills/fixture")
	link(t, source, alias)
	link(t, filepath.Join(s.Config.Home, ".openclaw/skills"), filepath.Join(s.Config.Home, ".openclaw/skills/loop"))
	s.Config.SkillRoots = []string{filepath.Dir(source)}
	s.skills()
	if len(s.Report.Items) != 2 {
		t.Fatal("discovery aliases lost or loop traversed", s.Report)
	}
	seenAlias := false
	for _, i := range s.Report.Items {
		if i.Path == filepath.Join(alias, "SKILL.md") && i.RealPath == real(filepath.Join(source, "SKILL.md")) {
			seenAlias = true
		}
	}
	if !seenAlias {
		t.Fatal("alias original path lost", s.Report.Items)
	}
}

func TestMCPClientsShareExactDeclaredPackageOwner(t *testing.T) {
	s := fixture(t)
	prefix := filepath.Join(s.Config.Home, ".npm-global")
	root := filepath.Join(prefix, "lib/node_modules/@synthetic/service")
	write(t, filepath.Join(root, "package.json"), `{"name":"@synthetic/service","version":"1.2.3","bin":{"synthetic-mcp":"bin/server.js"},"_resolved":"https://registry.npmjs.org/@synthetic/service/-/service-1.2.3.tgz"}`)
	write(t, filepath.Join(root, "bin/server.js"), "#!/bin/sh\ntouch SHOULD_NOT_RUN\n")
	if err := os.Chmod(filepath.Join(root, "bin/server.js"), 0755); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(prefix, "bin/synthetic-mcp")
	link(t, "../lib/node_modules/@synthetic/service/bin/server.js", command)
	s.Config.Path = filepath.Join(prefix, "bin")
	write(t, filepath.Join(s.Config.Home, ".cursor/mcp.json"), `{"mcpServers":{"customer-name":{"command":"synthetic-mcp","args":["SECRET_ARG"]}}}`)
	write(t, filepath.Join(s.Config.Home, ".config/opencode/opencode.json"), `{"mcp":{"another-name":{"type":"local","command":[`+quotedString(command)+`,"SECRET_ARG"],"enabled":false}}}`)
	s.packages()
	s.configs()
	var owner string
	for _, i := range s.Report.Items {
		if i.Kind == "node-package" && i.Name == "@synthetic/service" {
			owner = i.ID
		}
	}
	servers, disabled := 0, 0
	for _, i := range s.Report.Items {
		if i.Kind != "mcp-server" {
			continue
		}
		servers++
		if len(i.Related) != 1 || i.Related[0] != owner || owner == "" {
			t.Fatal("MCP package relation is not exact", i)
		}
		if i.Update.Code != "use_installation_owner" {
			t.Fatal("known package owner still reported as unverified", i.Update)
		}
		if i.Name == "another-name" && i.Health.Code == "disabled_not_started" {
			disabled++
		}
	}
	if servers != 2 || disabled != 1 || len(s.Report.Queries) != 0 {
		t.Fatal("client aliases, disabled reference or no-execution guarantee lost", s.Report)
	}
	data, _ := json.Marshal(s.Report)
	if strings.Contains(string(data), "SECRET_ARG") || strings.Contains(string(data), "SHOULD_NOT_RUN") {
		t.Fatal("server arguments or payload escaped")
	}
}

func quotedString(value string) string {
	b, _ := json.Marshal(value)
	return string(b)
}

func TestMCPRuntimeAndUndeclaredOrVersionPinnedFilesRemainUnrelated(t *testing.T) {
	s := fixture(t)
	prefix := filepath.Join(s.Config.Home, ".npm-global")
	root := filepath.Join(prefix, "lib/node_modules/synthetic")
	write(t, filepath.Join(root, "package.json"), `{"name":"synthetic","version":"1.0.0","bin":{"synthetic-mcp":"bin/server","node":"bin/node"}}`)
	for _, name := range []string{"server", "node", "not-declared"} {
		write(t, filepath.Join(root, "bin", name), "SYNTHETIC_DO_NOT_EXECUTE")
		if err := os.Chmod(filepath.Join(root, "bin", name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	link(t, "../lib/node_modules/synthetic/bin/server", filepath.Join(prefix, "bin/synthetic-mcp"))
	link(t, "../lib/node_modules/synthetic/bin/node", filepath.Join(prefix, "bin/node"))
	s.Config.Path = filepath.Join(prefix, "bin")
	s.packages()
	for _, tc := range []struct{ name, command string }{
		{"runtime", "node"},
		{"undeclared", filepath.Join(root, "bin/not-declared")},
	} {
		s.server("synthetic.json", "mcp", tc.name, map[string]any{"command": tc.command}, "")
	}
	// A pnpm store path embeds a version and does not move when a global alias updates.
	store := filepath.Join(s.Config.Home, "Library/pnpm/global/5/node_modules/.pnpm/synthetic@1.0.0/node_modules/synthetic")
	write(t, filepath.Join(store, "package.json"), `{"name":"synthetic","version":"1.0.0","bin":{"synthetic-mcp":"bin/server"}}`)
	write(t, filepath.Join(store, "bin/server"), "SYNTHETIC_DO_NOT_EXECUTE")
	if err := os.Chmod(filepath.Join(store, "bin/server"), 0755); err != nil {
		t.Fatal(err)
	}
	logical := filepath.Join(s.Config.Home, "Library/pnpm/global/5/node_modules/synthetic")
	link(t, store, logical)
	s.nodePackage(logical, "pnpm")
	s.server("synthetic.json", "mcp", "pinned-store", map[string]any{"command": filepath.Join(store, "bin/server")}, "")
	for _, i := range s.Report.Items {
		if i.Kind == "mcp-server" && len(i.Related) != 0 {
			t.Fatal("runtime, undeclared or pinned path treated as owner update", i)
		}
	}
	if len(s.Report.Queries) != 0 {
		t.Fatal("MCP ownership ran code")
	}
}

func TestMCPBrewOwnershipUsesStableEntryAndActiveKeg(t *testing.T) {
	s := fixture(t)
	root := s.Config.BrewRoots[0]
	for _, version := range []string{"1.0.0", "1.1.0"} {
		keg := filepath.Join(root, "Cellar/synthetic-mcp", version)
		write(t, filepath.Join(keg, "INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"},"arch":"arm64"}`)
		write(t, filepath.Join(keg, "bin/synthetic-mcp"), "SYNTHETIC_DO_NOT_EXECUTE")
		if err := os.Chmod(filepath.Join(keg, "bin/synthetic-mcp"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	link(t, "../Cellar/synthetic-mcp/1.1.0", filepath.Join(root, "opt/synthetic-mcp"))
	link(t, "../Cellar/synthetic-mcp/1.1.0/bin/synthetic-mcp", filepath.Join(root, "bin/synthetic-mcp"))
	s.brew()
	for _, tc := range []struct{ name, command string }{
		{"stable-bin", filepath.Join(root, "bin/synthetic-mcp")},
		{"stable-opt", filepath.Join(root, "opt/synthetic-mcp/bin/synthetic-mcp")},
		{"pinned-current", filepath.Join(root, "Cellar/synthetic-mcp/1.1.0/bin/synthetic-mcp")},
		{"retained-old", filepath.Join(root, "Cellar/synthetic-mcp/1.0.0/bin/synthetic-mcp")},
	} {
		s.server("synthetic.json", "mcp", tc.name, map[string]any{"command": tc.command}, "")
	}
	for _, i := range s.Report.Items {
		if i.Kind != "mcp-server" {
			continue
		}
		stable := strings.HasPrefix(i.Name, "stable-")
		if stable && len(i.Related) != 1 || !stable && len(i.Related) != 0 {
			t.Fatal("active stable/retained pinned entry ownership confused", i)
		}
	}
}
