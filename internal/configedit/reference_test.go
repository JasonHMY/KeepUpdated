package configedit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReferenceMigrationPreservesBytesAndDefinition(t *testing.T) {
	for _, tc := range []struct{ name, scope, input, before, after string }{
		{"jsonc", "user-or-explicit-project", `{// retained
 "mcpServers":{"server":{"command":"npx","args":["-y", /* KEEP */ "@scope/server@1.2.3", "SECRET_ARG"],"env":{"TOKEN":"SECRET_ENV"}},"keep":{"command":"other"}},"untouched":true}`, "@scope/server@1.2.3", "@scope/server@2.0.0"},
		{"json", "mcp", `{"mcp":{"server":{"command":["uvx","--from=server==1.2.3","serve","SECRET_ARG"],"enabled":false},"keep":{}}}`, "server==1.2.3", "server==2.0.0"},
		{"toml", "user-or-explicit-project", "# KEEP\n[mcp_servers.server]\ncommand = 'uvx'\nargs = ['--from', 'server==1.2.3', 'serve', 'SECRET_ARG'] # KEEP\n[mcp_servers.server.env]\nTOKEN='SECRET_ENV'\n[mcp_servers.keep]\ncommand='other'\n", "server==1.2.3", "server==2.0.0"},
		{"toml", "user-or-explicit-project", "[mcp_servers.server]\ncommand = \"uv\"\nargs = [\"tool\", \"run\", \"server\"] # KEEP\n", "server", "server==2.0.0"},
		{"json", "user-or-explicit-project", `{"mcpServers":{"server":{"command":"bunx","args":["pkg", "SECRET_ARG"],"env":{"TOKEN":"SECRET_ENV"}},"keep":{"command":"other"}}}`, "pkg", "pkg@2.0.0"},
		{"jsonc", "user-or-explicit-project", `{// retained
 "mcpServers":{"server":{"command":"/tools/bunx","args":[/* KEEP */ "@scope/server@latest", "--bun", "--package", "other", "-p", "other", "--install", "--no-install", "SECRET_ARG"],"env":{"TOKEN":"SECRET_ENV"}},"keep":{"command":"other"}},"untouched":true}`, "@scope/server@latest", "@scope/server@2.0.0"},
		{"jsonc", "mcp", `{// retained
 "mcp":{"server":{"command":["bunx",/* KEEP */ "pkg@1.2.3", "--bun", "--package=other", "-p", "other", "--install", "SECRET_ARG"],"enabled":false,"env":{"TOKEN":"SECRET_ENV"}},"keep":{}}}`, "pkg@1.2.3", "pkg@2.0.0"},
		{"toml", "user-or-explicit-project", "# KEEP\n[mcp_servers.server]\ncommand = '/tools/bunx'\nargs = ['pkg@1.2.3', '--bun', '--package', 'other', '-p', 'other', '--install', 'SECRET_ARG'] # KEEP\n[mcp_servers.server.env]\nTOKEN='SECRET_ENV'\n[mcp_servers.keep]\ncommand='other'\n", "pkg@1.2.3", "pkg@2.0.0"},
	} {
		t.Run(tc.name+tc.before, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config."+tc.name)
			fixtureFile(t, path, tc.input)
			if err := os.Chmod(path, 0640); err != nil {
				t.Fatal(err)
			}
			item := fixtureItem(path, tc.scope, "server")
			target, err := PlanReference(item, tc.after)
			if err != nil {
				t.Fatal(err)
			}
			if target.Operation != "replace-package-reference" || target.PackageReference.Before != tc.before || target.PackageReference.After != tc.after {
				t.Fatal(target)
			}
			encoded, _ := json.Marshal(target)
			if strings.Contains(string(encoded), "SECRET") {
				t.Fatal("argument leaked")
			}
			result, err := Apply(target)
			if err != nil || result.Reason != "mcp_package_reference_changed" || !result.Changed {
				t.Fatal(result, err)
			}
			got, _ := os.ReadFile(path)
			expected := strings.Replace(tc.input, tc.before, tc.after, 1)
			if tc.before == "server" {
				expected = strings.Replace(tc.input, `"server"]`, `"server==2.0.0"]`, 1)
			}
			if string(got) != expected {
				t.Fatalf("unrelated bytes changed\n%s\n%s", got, expected)
			}
			st, err := os.Stat(path)
			if err != nil || st.Mode().Perm() != 0640 {
				t.Fatal("file mode changed", st, err)
			}
			backupInfo, err := os.Stat(result.BackupPath)
			if err != nil || backupInfo.Mode().Perm() != 0600 {
				t.Fatal("backup is not private", backupInfo, err)
			}
			backup, _ := os.ReadFile(result.BackupPath)
			if string(backup) != tc.input {
				t.Fatal("backup mismatch")
			}
			if _, err := Plan(item); err != nil {
				t.Fatal("server definition was removed", err)
			}
			if _, err := Apply(target); err == nil {
				t.Fatal("old configuration accepted")
			}
		})
	}
}

func TestReferenceMigrationRejectsAmbiguityAndChangedBinding(t *testing.T) {
	for _, launcher := range []string{"npx", "bunx"} {
		t.Run(launcher, func(t *testing.T) {
			for _, input := range []string{
				`{"mcpServers":{"server":{"command":"uvx","args":["--from","pkg","--from","other","run"]}}}`,
				`{"mcpServers":{"server":{"command":"npx","args":["pkg@1.0.0"],"args":["other@1.0.0"]}}}`,
				`{"mcpServers":{"server":{"command":"npx","command":"uvx","args":["pkg@1.0.0"]}}}`,
				`{"mcpServers":{"server":{"command":["npx","pkg@1.0.0"],"args":[]}}}`,
				`{"mcpServers":{"server":{"command":"npx","args":["pkg@1.0.0"]}},"servers":{"server":{"command":"npx","args":["pkg@1.0.0"]}}}`,
				`{"mcpServers":{"server":{"url":"https://example.test","command":"npx","args":["pkg@1.0.0"]}}}`,
				`{"mcpServers":{"server":{"command":"npx","args":["pkg@1.0.0",123]}}}`,
			} {
				path := filepath.Join(t.TempDir(), "config.json")
				input = strings.ReplaceAll(input, "npx", launcher)
				fixtureFile(t, path, input)
				if _, err := PlanReference(fixtureItem(path, "user-or-explicit-project", "server"), "pkg@2.0.0"); err == nil {
					t.Fatal("ambiguous config accepted", input)
				}
			}
			for _, input := range []string{
				"[mcp_servers.server]\ncommand='npx'\nargs=['pkg@1.0.0']\nargs=['other@1.0.0']\n",
				"[mcp_servers.server]\ncommand='npx'\nargs=['pkg@1.0.0']\n[mcp_servers.server]\nargs=['pkg@1.0.0']\n",
				"[mcp_servers.server]\ncommand='npx'\nargs=[\n'pkg@1.0.0'\n]\n",
				"[mcp_servers.server]\ncommand='npx'\nargs=['pkg@1.0.0'] junk\n",
			} {
				path := filepath.Join(t.TempDir(), "config.toml")
				input = strings.ReplaceAll(input, "npx", launcher)
				fixtureFile(t, path, input)
				if _, err := PlanReference(fixtureItem(path, "user-or-explicit-project", "server"), "pkg@2.0.0"); err == nil {
					t.Fatal("ambiguous TOML accepted")
				}
			}
			path := filepath.Join(t.TempDir(), "config.json")
			input := `{"mcpServers":{"server":{"command":"npx","args":["pkg@1.0.0"]}}}`
			input = strings.ReplaceAll(input, "npx", launcher)
			fixtureFile(t, path, input)
			target, err := PlanReference(fixtureItem(path, "user-or-explicit-project", "server"), "pkg@2.0.0")
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"operation", "before", "after", "launcher", "fingerprint"} {
				copyTarget := target
				preview := *target.PackageReference
				copyTarget.PackageReference = &preview
				switch field {
				case "operation":
					copyTarget.Operation = ""
				case "before":
					preview.Before = "pkg@0.9.0"
				case "after":
					preview.After = "other@2.0.0"
				case "launcher":
					preview.Launcher = "uvx"
				case "fingerprint":
					copyTarget.SHA256 = "wrong"
				}
				if _, err := Apply(copyTarget); err == nil {
					t.Fatal("edited binding accepted", field)
				}
				got, _ := os.ReadFile(path)
				if string(got) != input {
					t.Fatal("failed binding changed file")
				}
			}
		})
	}
}

func TestReferenceMigrationChangesBoundTokenNotRepeatedText(t *testing.T) {
	for _, launcher := range []string{"npx", "bunx"} {
		for _, tc := range []struct{ format, scope, input, before, after string }{
			{"json", "user-or-explicit-project", `{"keep":"pkg@1.0.0","mcpServers":{"server":{"env":{"COPY":"pkg@1.0.0"},"command":"npx","args":["pkg@1.0.0","pkg@1.0.0"]}}}`, `"args":["pkg@1.0.0"`, `"args":["pkg@2.0.0"`},
			{"jsonc", "mcp", `{// pkg@1.0.0
 "mcp":{"server":{"env":{"COPY":"pkg@1.0.0"},"command":["npx", "pkg@1.0.0", "pkg@1.0.0"]},"keep":{"command":["npx", "pkg@1.0.0"]}}}`, `"command":["npx", "pkg@1.0.0"`, `"command":["npx", "pkg@2.0.0"`},
			{"toml", "user-or-explicit-project", "# pkg@1.0.0\n[mcp_servers.server]\ncommand='npx'\nargs=['pkg@1.0.0', 'pkg@1.0.0']\n[mcp_servers.server.env]\nCOPY='pkg@1.0.0'\n[mcp_servers.keep]\ncommand='npx'\nargs=['pkg@1.0.0']\n", "args=['pkg@1.0.0'", "args=['pkg@2.0.0'"},
		} {
			t.Run(launcher+"/"+tc.format, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config."+tc.format)
				input := strings.ReplaceAll(tc.input, "npx", launcher)
				expected := strings.ReplaceAll(strings.Replace(tc.input, tc.before, tc.after, 1), "npx", launcher)
				fixtureFile(t, path, input)
				target, err := PlanReference(fixtureItem(path, tc.scope, "server"), "pkg@2.0.0")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := Apply(target); err != nil {
					t.Fatal(err)
				}
				got, _ := os.ReadFile(path)
				if string(got) != expected {
					t.Fatalf("unbound text replaced: %s", got)
				}
			})
		}
	}
}
