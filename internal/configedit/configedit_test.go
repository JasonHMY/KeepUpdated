package configedit

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func fixtureFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func fixtureItem(path, scope, key string) audit.Item {
	return audit.Item{ID: "synthetic-item", Kind: "mcp-server", Name: key,
		Path: url.PathEscape(path) + "#" + url.PathEscape(scope) + "#" + url.PathEscape(key)}
}

func TestJSONMemberRemovalPreservesOtherBytes(t *testing.T) {
	for _, tc := range []struct{ name, input, expected string }{
		{"first", `{"mcpServers":{"remove":{"command":"node"},"keep":{"command":"node"}},"token":"SECRET_KEEP"}`, `{"mcpServers":{"keep":{"command":"node"}},"token":"SECRET_KEEP"}`},
		{"last", `{"mcpServers":{"keep":{},"remove":{}},"token":"SECRET_KEEP"}`, `{"mcpServers":{"keep":{}},"token":"SECRET_KEEP"}`},
		{"only", `{"mcpServers":{"remove":{}},"token":"SECRET_KEEP"}`, `{"mcpServers":{},"token":"SECRET_KEEP"}`},
		{"trailing", `{"mcpServers":{"keep":{},"remove":{},},"token":"SECRET_KEEP"}`, `{"mcpServers":{"keep":{},},"token":"SECRET_KEEP"}`},
		{"comments", "{\n// KEEP_ROOT\n\"mcpServers\":{/*KEEP_A*/\"remove\":{\"args\":[\"\\\"}SECRET_REMOVE\"]} /*KEEP_B*/,/*KEEP_C*/\"keep\":{}},\n\"token\":\"SECRET_KEEP\"\n}", "{\n// KEEP_ROOT\n\"mcpServers\":{/*KEEP_A*/ /*KEEP_B*//*KEEP_C*/\"keep\":{}},\n\"token\":\"SECRET_KEEP\"\n}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "servers.jsonc")
			fixtureFile(t, path, tc.input)
			target, err := Plan(fixtureItem(path, "user-or-explicit-project", "remove"))
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(target)
			if strings.Contains(string(encoded), "SECRET_") || strings.Contains(string(encoded), "command") {
				t.Fatal("binding contains server values")
			}
			result, err := Apply(target)
			if err != nil || !result.Changed || result.Status != "applied" {
				t.Fatal(result, err)
			}
			actual, err := os.ReadFile(path)
			if err != nil || string(actual) != tc.expected {
				t.Fatalf("unrelated bytes changed: %q, %v", actual, err)
			}
			backup, err := os.ReadFile(result.BackupPath)
			if err != nil || string(backup) != tc.input {
				t.Fatal("rollback original missing", err)
			}
			for _, file := range []string{path, result.BackupPath} {
				st, err := os.Stat(file)
				if err != nil || st.Mode().Perm() != 0600 {
					t.Fatal("credential file permissions changed", err)
				}
			}
		})
	}
}

func TestMCPRemovalDoesNotRemoveSharedPackageOrOtherClient(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "installed", "server.js")
	fixtureFile(t, shared, "SYNTHETIC_PACKAGE_DO_NOT_EXECUTE_OR_REMOVE")
	marker := filepath.Join(root, "SHOULD_NOT_EXECUTE")
	original := `{"mcpServers":{"shared":{"command":"sh","args":["-c","touch ` + marker + `"],"env":{"TOKEN":"SECRET_ENV"}}},"settings":{"token":"SECRET_RETAIN"}}`
	selected := filepath.Join(root, "client-one.json")
	other := filepath.Join(root, "client-two.json")
	fixtureFile(t, selected, original)
	fixtureFile(t, other, original)
	target, err := Plan(fixtureItem(selected, "user-or-explicit-project", "shared"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Apply(target)
	if err != nil || !result.Changed {
		t.Fatal(result, err)
	}
	for path, text := range map[string]string{other: original, shared: "SYNTHETIC_PACKAGE_DO_NOT_EXECUTE_OR_REMOVE"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != text {
			t.Fatal("unselected consumer or installation changed", path, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("MCP command was executed")
	}
	data, _ := os.ReadFile(selected)
	if string(data) != `{"mcpServers":{},"settings":{"token":"SECRET_RETAIN"}}` {
		t.Fatal("selected config scope not removed exactly")
	}
}

func TestJSONSchemasAndScopes(t *testing.T) {
	for _, tc := range []struct{ name, scope, input, expected string }{
		{"opencode", "user-or-explicit-project", `{"mcp":{"remove":{"type":"local","command":["node","server.js"]},"keep":{}}}`, `{"mcp":{"keep":{}}}`},
		{"specificOpenCode", "mcp", `{"mcpServers":{"remove":{}},"mcp":{"remove":{},"keep":{}}}`, `{"mcpServers":{"remove":{}},"mcp":{"keep":{}}}`},
		{"specificOpenClaw", "mcp.servers", `{"mcpServers":{"remove":{}},"mcp":{"servers":{"remove":{},"keep":{}}}}`, `{"mcpServers":{"remove":{}},"mcp":{"servers":{"keep":{}}}}`},
		{"specificVSCode", "servers", `{"mcpServers":{"remove":{}},"servers":{"remove":{},"keep":{}}}`, `{"mcpServers":{"remove":{}},"servers":{"keep":{}}}`},
		{"openclaw", "user-or-explicit-project", `{"mcp":{"servers":{"remove":{},"keep":{}}},"nodeHost":{"mcp":{"servers":{"remove":{}}}}}`, `{"mcp":{"servers":{"keep":{}}},"nodeHost":{"mcp":{"servers":{"remove":{}}}}}`},
		{"nodeHost", "nodeHost.mcp.servers", `{"mcp":{"servers":{"remove":{}}},"nodeHost":{"mcp":{"servers":{"remove":{},"keep":{}}}}}`, `{"mcp":{"servers":{"remove":{}}},"nodeHost":{"mcp":{"servers":{"keep":{}}}}}`},
		{"vscode", "user-or-explicit-project", `{"servers":{"remove":{},"keep":{}},"inputs":[{"id":"token","type":"promptString"}]}`, `{"servers":{"keep":{}},"inputs":[{"id":"token","type":"promptString"}]}`},
		{"claudeProject", "/synthetic/project#scope", `{"projects":{"/synthetic/project#scope":{"mcpServers":{"remove":{},"keep":{}}}},"mcpServers":{"remove":{}}}`, `{"projects":{"/synthetic/project#scope":{"mcpServers":{"keep":{}}}},"mcpServers":{"remove":{}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "synthetic#config.json")
			fixtureFile(t, path, tc.input)
			target, err := Plan(fixtureItem(path, tc.scope, "remove"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(target); err != nil {
				t.Fatal(err)
			}
			actual, _ := os.ReadFile(path)
			if string(actual) != tc.expected {
				t.Fatal("another scope changed", string(actual))
			}
		})
	}
}

func TestStaleOrTamperedBindingDoesNotWrite(t *testing.T) {
	for _, change := range []string{"file", "mode", "schema", "jsonPath", "symlink"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "servers.json")
			original := `{"mcpServers":{"remove":{}},"settings":{"keep":true}}`
			fixtureFile(t, path, original)
			target, err := Plan(fixtureItem(path, "user-or-explicit-project", "remove"))
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "file":
				original += "\n"
				fixtureFile(t, path, original)
			case "mode":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "schema":
				target.Schema = "arbitrary"
			case "jsonPath":
				target.JSONPath = []string{"settings", "keep"}
			case "symlink":
				other := filepath.Join(root, "other.json")
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			}
			result, err := Apply(target)
			if err == nil || result.Changed || result.BackupPath != "" {
				t.Fatal("invalid binding mutated a config", result, err)
			}
			actual, _ := os.ReadFile(path)
			if string(actual) != original {
				t.Fatal("stale config clobbered")
			}
			files, _ := os.ReadDir(root)
			for _, file := range files {
				if strings.Contains(file.Name(), "keepupdated") {
					t.Fatal("stale binding created files", file.Name())
				}
			}
		})
	}
}

func TestUnsupportedSyntaxAndAmbiguousDefinitionsStayUntouched(t *testing.T) {
	for _, tc := range []struct{ name, extension, input string }{
		{"yaml", ".yaml", "mcp_servers:\n  remove:\n    command: node\n"},
		{"json5", ".json5", "{mcp: {remove: {command: 'node'}}}"},
		{"duplicates", ".json", `{"mcpServers":{"remove":{"token":"SECRET_DUPLICATE"},"remove":{}}}`},
		{"ambiguous", ".json", `{"mcpServers":{"remove":{}},"servers":{"remove":{}}}`},
		{"unclosedComment", ".jsonc", `{"mcpServers":{"remove":{}}} /* SECRET_UNCLOSED`},
		{"tooDeep", ".json", `{"mcpServers":{"remove":{}} ,"deep":` + strings.Repeat("[", 130) + `0` + strings.Repeat("]", 130) + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config"+tc.extension)
			fixtureFile(t, path, tc.input)
			_, err := Plan(fixtureItem(path, "user-or-explicit-project", "remove"))
			if err == nil || strings.Contains(err.Error(), "SECRET_") {
				t.Fatal("unsupported input accepted or echoed", err)
			}
			data, _ := os.ReadFile(path)
			if !bytes.Equal(data, []byte(tc.input)) {
				t.Fatal("planning changed the config")
			}
			files, _ := os.ReadDir(root)
			if len(files) != 1 {
				t.Fatal("planning created backup files")
			}
		})
	}
}

func TestCodexTOMLExactTableAndChildrenRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	before := "model = \"synthetic\"\n\n[mcp_servers.\"remove.with.dot\"]\ncommand = \"node\"\nargs = [\"SECRET_REMOVE\"]\n[mcp_servers.\"remove.with.dot\".env]\nTOKEN = \"SECRET_REMOVE\"\n[mcp_servers.keep]\ncommand = \"other\"\n[mcp_servers.\"remove.with.dot\".http_headers]\nAuthorization = \"SECRET_REMOVE\"\n[other]\nSECRET_KEEP = true\n"
	expected := "model = \"synthetic\"\n\n[mcp_servers.keep]\ncommand = \"other\"\n[other]\nSECRET_KEEP = true\n"
	fixtureFile(t, path, before)
	target, err := Plan(fixtureItem(path, "user-or-explicit-project", "remove.with.dot"))
	if err != nil || target.Schema != "toml" {
		t.Fatal(target, err)
	}
	result, err := Apply(target)
	if err != nil || !result.Changed {
		t.Fatal(result, err)
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != expected {
		t.Fatal("TOML unrelated content changed", string(actual))
	}
}

func TestComplexTOMLDoesNotGuessSpans(t *testing.T) {
	for _, text := range []string{
		"[mcp_servers.remove]\ncommand = \"node\"\nnotes = \"\"\"\n[mcp_servers.keep]\n\"\"\"\n",
		"[mcp_servers]\nremove = { command = \"node\" }\n",
		"[[mcp_servers.remove]]\ncommand = \"node\"\n",
		"[mcp_servers.remove]\ncommand = \"node\"\n[mcp_servers.remove]\ncommand = \"other\"\n",
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		fixtureFile(t, path, text)
		if _, err := Plan(fixtureItem(path, "user-or-explicit-project", "remove")); err == nil {
			t.Fatal("complex TOML accepted")
		}
		actual, _ := os.ReadFile(path)
		if string(actual) != text {
			t.Fatal("unsupported TOML changed")
		}
	}
}

func TestNonRegularAndOversizedConfigDoNotBlock(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{root, filepath.Join(root, "missing.json")} {
		if _, err := Plan(fixtureItem(path, "user-or-explicit-project", "remove")); err == nil {
			t.Fatal("non-file accepted")
		}
	}
	path := filepath.Join(root, "large.json")
	fixtureFile(t, path, strings.Repeat(" ", maxConfigBytes+1))
	if _, err := Plan(fixtureItem(path, "user-or-explicit-project", "remove")); err == nil {
		t.Fatal("oversized config accepted")
	}
}

func TestBackupCheckpointFailurePreventsConfigurationReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.json")
	original := `{"mcpServers":{"selected":{},"retained":{}}}`
	fixtureFile(t, path, original)
	target, err := Plan(fixtureItem(path, "user-or-explicit-project", "selected"))
	if err != nil {
		t.Fatal(err)
	}
	saved := ""
	result, err := ApplyWithBackup(target, func(backup string) error {
		saved = backup
		content, e := os.ReadFile(backup)
		if e != nil || string(content) != original {
			t.Fatal("backup unavailable", e)
		}
		return os.ErrPermission
	})
	if err == nil || result.Changed || saved == "" {
		t.Fatal(result, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != original {
		t.Fatal("configuration changed after checkpoint failure", err)
	}
}
