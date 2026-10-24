package manage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

// NodePolicy binds the capability and declaration used by the native query.
// LatestVersion is informational; ResolvedTarget.Version is the permitted target.
type NodePolicy struct {
	VirtualStore       string             `json:"virtual_store,omitempty"`
	ManagerVersion     string             `json:"manager_version"`
	ManagerFingerprint string             `json:"manager_fingerprint"`
	Layout             string             `json:"layout"`
	Bin                string             `json:"bin"`
	Selection          audit.NodeSelector `json:"selection"`
	LatestVersion      string             `json:"latest_version"`
}

// Exact versions are intentional: expand only with owner/layout/no-save fixtures.
func NodeLayout(owner, version string) string {
	switch {
	case owner == "npm" && version == "11.19.1":
		return "npm-global-lib"
	case owner == "pnpm" && version == "10.18.3":
		return "pnpm-global-5"
	case owner == "bun" && version == "1.4.3":
		return "bun-global-node-modules"
	}
	return ""
}
func NodeBin(owner string, b Binding, o Options) string {
	if owner == "npm" {
		return filepath.Join(b.Scope, "bin")
	}
	return managerBin(owner, b, o)
}
func validNodePolicy(i audit.Item, b Binding, t ResolvedTarget, o Options) bool {
	p := t.Node
	if p == nil || p.Layout == "" || p.Layout != NodeLayout(i.Owner, p.ManagerVersion) || p.Bin != NodeBin(i.Owner, b, o) || p.ManagerFingerprint == "" {
		return false
	}
	if i.Owner == "pnpm" {
		store, err := PnpmVirtualStore(i, b.Scope)
		if err != nil || store != p.VirtualStore {
			return false
		}
	}
	digest, err := fileHash(b.ManagerPath)
	if err != nil || digest != p.ManagerFingerprint {
		return false
	}
	selector, err := audit.ReadNodeSelector(i.Owner, i.Name, i.Path, b.Scope)
	if err != nil || selector != p.Selection || !audit.NodeSelectorAllows(selector.Selector, i.Version) || !audit.NodeSelectorAllows(selector.Selector, t.Version) {
		return false
	}
	cmp, known := audit.CompareVersions(i.Version, t.Version)
	return known && cmp <= 0
}

// PnpmVirtualStore recognizes only the existing isolated global layout; this
// does not create a store, migrate a layout or reinterpret a hoisted installation.
func PnpmVirtualStore(i audit.Item, scope string) (string, error) {
	data, err := readMetadata(filepath.Join(scope, "node_modules/.modules.yaml"))
	if err != nil {
		return "", err
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		for _, key := range []string{"layoutVersion", "nodeLinker", "packageManager", "virtualStoreDir"} {
			if strings.HasPrefix(line, key+":") {
				if _, ok := fields[key]; ok {
					return "", errors.New("ambiguous pnpm layout")
				}
				fields[key] = strings.TrimSpace(strings.TrimPrefix(line, key+":"))
			}
		}
	}
	if fields["layoutVersion"] != "5" || fields["nodeLinker"] != "isolated" || fields["packageManager"] != "pnpm@10.18.3" {
		return "", errors.New("unsupported pnpm layout")
	}
	store := ""
	switch fields["virtualStoreDir"] {
	case "../.pnpm":
		store = ".pnpm"
	case ".pnpm":
		store = "node_modules/.pnpm"
	default:
		return "", errors.New("unsupported pnpm store")
	}
	realScope, e1 := filepath.EvalSymlinks(scope)
	realPackage, e2 := filepath.EvalSymlinks(i.Path)
	entry := strings.ReplaceAll(i.Name, "/", "+") + "@" + i.Version
	if e1 != nil || e2 != nil || realPackage != filepath.Join(realScope, store, entry, "node_modules", filepath.FromSlash(i.Name)) {
		return "", errors.New("pnpm store differs from installation")
	}
	return store, nil
}

// PnpmDisableRuntime uses typed null without replacing nodeVersion with
// an empty string, so native engine checks still use the running Node version.
const PnpmDisableRuntime = "--config.use-node-version=null"

// PnpmDirectoryArgs addresses the existing global directory without global's
// forced save=true. Explicit bin/store paths retain its activation and layout.
func PnpmDirectoryArgs(b Binding, o Options, store string) []string {
	return []string{"--global=false", "--dir", b.Scope, "--ignore-workspace", "--config.ignore-pnpmfile=true", "--config.manage-package-manager-versions=false", PnpmDisableRuntime, "--config.bin=" + NodeBin("pnpm", b, o), "--config.virtual-store-dir=" + store, "--config.node-linker=isolated"}
}

// Global root/list queries can create pnpm's bin directory. Require it to exist
// before querying, so preview cannot create an installation destination.
func NodeBinExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir() && ownedPath(path, false) == nil
}
