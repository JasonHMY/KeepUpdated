package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBunxLauncherClassificationNeverClaimsInstalledUpdate(t *testing.T) {
	for _, tc := range []struct {
		command any
		args    []any
		code    string
	}{
		{"bunx", []any{"server@1.2.3", "SECRET"}, "launcher_reference_requires_explicit_migration"},
		{"/tools/bunx", []any{"@scope/server@1.2.3", "--bun", "--package", "other", "-p", "other", "--install", "SECRET"}, "launcher_reference_requires_explicit_migration"},
		{"bunx", []any{"server"}, "launcher_unpinned_not_persistently_managed"},
		{"bunx", []any{"server@latest"}, "launcher_unpinned_not_persistently_managed"},
		{[]any{"bunx", "@scope/server@latest", "SECRET"}, nil, "launcher_unpinned_not_persistently_managed"},
		{[]any{"bunx", "server@1.2.3"}, []any{}, "server_source_not_verified"},
		{"bunx", []any{"--package", "server", "--package", "other", "exec"}, "server_source_not_verified"},
		{"bunx", []any{"--bun", "server"}, "server_source_not_verified"},
		{"bunx", []any{"tsc@1.2.3"}, "server_source_not_verified"},
		{"bunx", []any{"claude@latest"}, "server_source_not_verified"},
		{"bunx", []any{"add"}, "server_source_not_verified"},
		{"bunx", []any{"exec"}, "server_source_not_verified"},
		{"./bunx", []any{"server@1.2.3"}, "server_source_not_verified"},
		{"/tools/../bunx", []any{"server@1.2.3"}, "server_source_not_verified"},
		{"env", []any{"bunx", "server@1.2.3"}, "server_source_not_verified"},
		{"sh", []any{"-c", "bunx server@1.2.3"}, "server_source_not_verified"},
		{"bun", []any{"x", "server@1.2.3"}, "server_source_not_verified"},
	} {
		for _, disabled := range []bool{false, true} {
			s := fixture(t)
			definition := map[string]any{"command": tc.command, "disabled": disabled, "env": map[string]any{"TOKEN": "SECRET_ENV"}}
			if tc.args != nil {
				definition["args"] = tc.args
			}
			s.server("fixture.json", "mcp", "server", definition, "")
			server := s.Report.Items[len(s.Report.Items)-1]
			if server.Update.Code != tc.code || len(server.Related) != 0 || server.Version != "" {
				t.Fatal(server)
			}
			data, _ := json.Marshal(server)
			if strings.Contains(string(data), "SECRET") {
				t.Fatal("arguments leaked")
			}
		}
	}
}
