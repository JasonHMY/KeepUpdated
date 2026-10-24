package audit

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pythonMCPFixture(t *testing.T, s *Scanner, owner string) (string, string) {
	t.Helper()
	base := ".local/share/uv/tools"
	if owner == "pipx" {
		base = ".local/share/pipx/venvs"
	}
	env := filepath.Join(s.Config.Home, base, "example-server")
	dist := filepath.Join(env, "lib/python3.13/site-packages/example_server-1.0.0.dist-info")
	write(t, filepath.Join(dist, "METADATA"), "Name: example-server\nVersion: 1.0.0\n")
	write(t, filepath.Join(dist, "entry_points.txt"), "[console_scripts]\nexample-mcp = example:main\n")
	payload := "#!/bin/sh\necho NEVER_EXECUTE\n"
	write(t, filepath.Join(env, "bin/example-mcp"), payload)
	if err := os.Chmod(filepath.Join(env, "bin/example-mcp"), 0755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(payload))
	write(t, filepath.Join(dist, "RECORD"), fmt.Sprintf("../../../bin/example-mcp,sha256=%s,%d\n", base64.RawURLEncoding.EncodeToString(sum[:]), len(payload)))
	entry := filepath.Join(s.Config.Home, ".local/bin/example-mcp")
	link(t, filepath.Join(env, "bin/example-mcp"), entry)
	if owner == "pipx" {
		write(t, filepath.Join(env, "pipx_metadata.json"), fmt.Sprintf(`{"main_package":{"package":"example-server","package_version":"1.0.0","apps":["example-mcp"],"app_paths":[{"__Path__":%q}],"package_or_url":"example-server"}}`, filepath.Join(env, "bin/example-mcp")))
	} else {
		write(t, filepath.Join(env, "uv-receipt.toml"), fmt.Sprintf("[tool]\nrequirements = [{ name = \"example-server\" }]\nentrypoints = [\n{ name = \"example-mcp\", install-path = %q, from = \"example-server\" },\n]\n", entry))
	}
	return env, dist
}

func TestMCPPythonStableEntrypointOwner(t *testing.T) {
	for _, owner := range []string{"pipx", "uv"} {
		t.Run(owner, func(t *testing.T) {
			s := fixture(t)
			env, _ := pythonMCPFixture(t, s, owner)
			s.Config.Path = filepath.Join(s.Config.Home, ".local/bin")
			s.packages()
			for _, command := range []string{"example-mcp", filepath.Join(env, "bin/example-mcp")} {
				s.server("fixture.json", "mcp", command, map[string]any{"command": command, "args": []any{"SECRET"}}, "")
			}
			servers := 0
			for _, i := range s.Report.Items {
				if i.Kind == "mcp-server" {
					servers++
					if len(i.Related) != 1 || i.Update.Code != "use_installation_owner" {
						t.Fatal(i)
					}
				}
			}
			if servers != 2 || len(s.Report.Queries) != 0 {
				t.Fatal(s.Report)
			}
			data, _ := json.Marshal(s.Report)
			if strings.Contains(string(data), "SECRET") {
				t.Fatal("argument leak")
			}
		})
	}
}

func TestMCPPythonRejectsModifiedUndeclaredAndAmbiguousOwners(t *testing.T) {
	for _, scenario := range []string{"modified", "missing-record", "undeclared", "receipt-name", "receipt-path", "duplicate-bin", "versioned-entry", "duplicate-owner", "path-override", "package-edited"} {
		t.Run(scenario, func(t *testing.T) {
			s := fixture(t)
			env, dist := pythonMCPFixture(t, s, "uv")
			entry := filepath.Join(s.Config.Home, ".local/bin/example-mcp")
			switch scenario {
			case "package-edited":
				module := filepath.Join(filepath.Dir(dist), "example.py")
				write(t, module, "original")
				sum := sha256.Sum256([]byte("original"))
				record, _ := os.ReadFile(filepath.Join(dist, "RECORD"))
				write(t, filepath.Join(dist, "RECORD"), string(record)+fmt.Sprintf("example.py,sha256=%s,8\n", base64.RawURLEncoding.EncodeToString(sum[:])))
				write(t, module, "edited")
			case "modified":
				write(t, filepath.Join(env, "bin/example-mcp"), "locally edited")
			case "missing-record":
				os.Remove(filepath.Join(dist, "RECORD"))
			case "undeclared":
				write(t, filepath.Join(dist, "entry_points.txt"), "[console_scripts]\nother=example:main\n")
			case "receipt-name":
				write(t, filepath.Join(env, "uv-receipt.toml"), fmt.Sprintf("[tool]\nentrypoints=[{name=\"example-mcp\",install-path=%q,from=\"other\"}]\n", entry))
			case "receipt-path":
				write(t, filepath.Join(env, "uv-receipt.toml"), "[tool]\nentrypoints=[{name=\"example-mcp\",install-path=\"/other/bin/example-mcp\"}]\n")
			case "duplicate-bin":
				write(t, filepath.Join(dist, "entry_points.txt"), "[console_scripts]\nexample-mcp=example:main\nexample-mcp=other:main\n")
			case "versioned-entry":
				entry = filepath.Join(s.Config.Home, "versioned/1.0.0/example-mcp")
				link(t, filepath.Join(env, "bin/example-mcp"), entry)
			}
			s.packages()
			if scenario == "duplicate-owner" {
				for _, i := range s.Report.Items {
					if i.Kind == "python-package" {
						i.ID = "another-owner"
						s.Report.Items = append(s.Report.Items, i)
						break
					}
				}
			}
			definition := map[string]any{"command": entry}
			if scenario == "path-override" {
				definition["command"] = "example-mcp"
				definition["env"] = map[string]any{"PATH": "/somewhere/else"}
				s.Config.Path = filepath.Dir(entry)
			}
			s.server("fixture.json", "mcp", "server", definition, "")
			server := s.Report.Items[len(s.Report.Items)-1]
			want := 0
			if scenario == "duplicate-owner" {
				want = 2
			}
			if len(server.Related) != want {
				t.Fatal(server)
			}
		})
	}
}

func TestLauncherClassificationNeverClaimsInstalledUpdate(t *testing.T) {
	s := fixture(t)
	for _, tc := range []struct {
		args []any
		code string
	}{
		{[]any{"-y", "server@1.2.3", "SECRET"}, "launcher_reference_requires_explicit_migration"},
		{[]any{"server"}, "launcher_unpinned_not_persistently_managed"},
		{[]any{"--package", "server", "--package", "other", "exec"}, "server_source_not_verified"},
	} {
		s.server("fixture.json", "mcp", "server", map[string]any{"command": "npx", "args": tc.args}, "")
		server := s.Report.Items[len(s.Report.Items)-1]
		if server.Update.Code != tc.code || len(server.Related) != 0 || server.Version != "" {
			t.Fatal(server)
		}
		s.Report.Items = nil
		s.seen = map[string]bool{}
	}
}

func TestDuplicateTOMLLauncherFieldsStayAmbiguous(t *testing.T) {
	for _, text := range []string{
		"[mcp_servers.fixture]\ncommand=\"npx\"\ncommand=\"uvx\"\nargs=[\"server\"]\n",
		"[mcp_servers.fixture]\ncommand=\"npx\"\nargs=[\"server\"]\nargs=[\"other\"]\n",
	} {
		servers, partial := tomlServers(text)
		if !partial || servers["fixture"].(map[string]any)["_parse_error"] != true {
			t.Fatal("duplicate launcher field accepted", servers)
		}
	}
}
