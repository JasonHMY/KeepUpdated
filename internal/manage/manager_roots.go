package manage

import (
	"path/filepath"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func configuredManagerRoot(o Options, owner, root string) bool {
	return root != "" && root != "/" && filepath.IsAbs(root) && filepath.Clean(root) == root && o.ManagerRoots[owner] == root
}

// PnpmGlobalDir maps the supported version-5 layout back to pnpm's config root.
// pnpm appends the layout version itself, including for read-only root queries.
func PnpmGlobalDir(scope string) string { return filepath.Dir(scope) }

func managerBin(provider string, b Binding, o Options) string {
	if p := o.ManagerBins[provider]; filepath.IsAbs(p) && filepath.Clean(p) == p && p != "/" {
		return p
	}
	switch provider {
	case "pnpm":
		return filepath.Dir(PnpmGlobalDir(b.Scope))
	case "bun":
		return filepath.Join(o.Home, ".bun/bin")
	default:
		return filepath.Join(o.Home, ".local/bin")
	}
}

// pnpm 10 uses a sibling .pnpm store for globals; older layouts put it inside
// node_modules. Only the exact package/version record in either layout is valid.
func pnpmPackagePath(i audit.Item, scope string) bool {
	realScope, se := filepath.EvalSymlinks(scope)
	realPackage, pe := filepath.EvalSymlinks(i.Path)
	if se != nil || pe != nil {
		return false
	}
	name := filepath.FromSlash(i.Name)
	if realPackage == filepath.Join(realScope, "node_modules", name) {
		return true
	}
	entry := strings.ReplaceAll(i.Name, "/", "+") + "@" + i.Version
	for _, store := range []string{".pnpm", "node_modules/.pnpm"} {
		if realPackage == filepath.Join(realScope, store, entry, "node_modules", name) {
			return true
		}
	}
	return false
}
