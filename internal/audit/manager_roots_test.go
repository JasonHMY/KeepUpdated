package audit

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestExplicitManagerRootsDiscoverOnlyConfiguredInstallations(t *testing.T) {
	s := fixture(t)
	s.Config.ManagerRoots = map[string]string{}
	for _, owner := range []string{"pipx", "uv", "pnpm", "bun"} {
		root := filepath.Join(s.Config.Home, "custom", owner)
		s.Config.ManagerRoots[owner] = root
		if owner == "pipx" || owner == "uv" {
			write(t, filepath.Join(root, "example/lib/python3.13/site-packages/example-1.0.dist-info/METADATA"), "Name: example\nVersion: 1.0\n")
		} else {
			if owner == "pnpm" {
				root = filepath.Join(root, "5")
			}
			write(t, filepath.Join(root, "node_modules/example/package.json"), `{"name":"example","version":"1.0.0"}`)
		}
	}
	write(t, filepath.Join(s.Config.Home, "unconfigured/tools/unknown/lib/python3.13/site-packages/unknown-1.0.dist-info/METADATA"), "Name: unknown\nVersion: 1.0\n")
	s.packages()
	counts := map[string]int{}
	for _, i := range s.Report.Items {
		if (i.Kind == "node-package" || i.Kind == "python-package") && (i.Owner == "pipx" || i.Owner == "uv" || i.Owner == "pnpm" || i.Owner == "bun") {
			counts[i.Owner]++
			if i.Name != "example" {
				t.Fatal(i)
			}
		}
	}
	for _, owner := range []string{"pipx", "uv", "pnpm", "bun"} {
		if counts[owner] != 1 {
			t.Fatal(counts)
		}
	}
	data, _ := json.Marshal(s.Report)
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["ManagerRoots"] != nil || wire["manager_roots"] != nil || s.Report.Schema != "keepupdated/v1" {
		t.Fatal("scan wire schema changed")
	}
}

func TestExplicitManagerPathsUseOnlyCleanAbsoluteOverrides(t *testing.T) {
	for _, key := range []string{"PIPX_HOME", "UV_TOOL_DIR", "BUN_INSTALL_GLOBAL_DIR", "BUN_INSTALL", "PNPM_HOME", "PIPX_BIN_DIR", "UV_TOOL_BIN_DIR", "BUN_INSTALL_BIN"} {
		t.Setenv(key, "")
	}
	home := t.TempDir()
	t.Setenv("PIPX_HOME", filepath.Join(home, "pipx"))
	t.Setenv("UV_TOOL_DIR", filepath.Join(home, "tools"))
	t.Setenv("PNPM_HOME", filepath.Join(home, "pnpm"))
	t.Setenv("BUN_INSTALL", filepath.Join(home, "bun"))
	roots, bins := ExplicitManagerPaths()
	if roots["pipx"] != filepath.Join(home, "pipx/venvs") || roots["uv"] != filepath.Join(home, "tools") || roots["pnpm"] != filepath.Join(home, "pnpm/global") || bins["pnpm"] != filepath.Join(home, "pnpm") || roots["bun"] != filepath.Join(home, "bun/install/global") {
		t.Fatal(roots, bins)
	}
	t.Setenv("UV_TOOL_DIR", "relative/secret")
	t.Setenv("PIPX_HOME", "/")
	roots, _ = ExplicitManagerPaths()
	if roots["uv"] != "" || roots["pipx"] != "" {
		t.Fatal(roots)
	}
}
