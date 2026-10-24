package manage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func TestUninstallVerificationScopesDiagnosticsToActualInstallation(t *testing.T) {
	for _, provider := range []string{"npm", "pipx", "mcp-alias"} {
		for _, scenario := range []string{"unrelated-global", "relevant-global", "ancestor-failed", "unrelated-path", "cancelled"} {
			t.Run(provider+"/"+scenario, func(t *testing.T) {
				f := setup(t)
				item := f.npm("target", "example")
				adapter := "node-packages"
				removedPath := item.Path
				p := PlanItem{ID: item.ID, Kind: item.Kind, Provider: "npm", Binding: Binding{TargetID: item.ID, Path: item.Path, Owner: "npm", InstallationKind: "node-package"}}
				if provider == "pipx" {
					item, removedPath = f.pythonTool("pipx", "example-tool")
					p = f.plan(Selection{Action: "uninstall", All: true}).Items[0]
					adapter = "python-tools"
				}
				if provider == "mcp-alias" {
					p.ID = "server-alias"
					p.Kind = "mcp-server"
				} // The execution owner remains node-package.
				if err := os.RemoveAll(removedPath); err != nil {
					t.Fatal(err)
				}
				diagnostics := []audit.Diagnostic{{Adapter: "apps", Code: "process_query_failed"}, {Adapter: "launchd", Code: "user_service_query_failed"}}
				expect := "uninstalled"
				switch scenario {
				case "relevant-global":
					diagnostics = append(diagnostics, audit.Diagnostic{Adapter: adapter, Code: "inventory_unavailable"})
					expect = "verification_unknown"
				case "ancestor-failed":
					diagnostics = append(diagnostics, audit.Diagnostic{Adapter: adapter, Path: filepath.Dir(removedPath), Code: "unreadable_directory"})
					expect = "verification_unknown"
				case "unrelated-path":
					diagnostics = append(diagnostics, audit.Diagnostic{Adapter: adapter, Path: filepath.Join(f.home, "unrelated-installations"), Code: "unreadable_directory"})
				case "cancelled":
					diagnostics = append(diagnostics, audit.Diagnostic{Adapter: "scan", Code: "scan_cancelled"})
					expect = "verification_unknown"
				}
				r := ItemResult{}
				verifyResult(&r, p, "uninstall", audit.Report{Diagnostics: diagnostics}, nil)
				if r.Status != expect {
					t.Fatalf("uninstall with %s diagnostics: got %+v, want %s", scenario, r, expect)
				}
			})
		}
	}
}
