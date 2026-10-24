package audit

import (
	"os"
	"path/filepath"
)

// ExplicitManagerPaths reads only documented path overrides, never credentials
// or shell configuration. Callers using synthetic homes supply their own maps.
func ExplicitManagerPaths() (roots, bins map[string]string) {
	roots, bins = map[string]string{}, map[string]string{}
	for _, entry := range []struct{ owner, variable, suffix string }{
		{"volta", "VOLTA_HOME", ""}, {"pipx", "PIPX_HOME", "venvs"}, {"uv", "UV_TOOL_DIR", ""},
		{"bun", "BUN_INSTALL_GLOBAL_DIR", ""}, {"pnpm", "PNPM_HOME", "global"},
	} {
		if path := os.Getenv(entry.variable); validManagerRoot(path) {
			roots[entry.owner] = filepath.Join(path, entry.suffix)
		}
	}
	for owner, variable := range map[string]string{"pipx": "PIPX_BIN_DIR", "uv": "UV_TOOL_BIN_DIR", "bun": "BUN_INSTALL_BIN", "pnpm": "PNPM_HOME"} {
		if path := os.Getenv(variable); validManagerRoot(path) {
			bins[owner] = path
		}
	}
	if path := os.Getenv("BUN_INSTALL"); validManagerRoot(path) {
		if roots["bun"] == "" {
			roots["bun"] = filepath.Join(path, "install/global")
		}
		if bins["bun"] == "" {
			bins["bun"] = filepath.Join(path, "bin")
		}
	}
	return roots, bins
}

func validManagerRoot(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/"
}
