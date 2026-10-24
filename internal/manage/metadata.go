package manage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func readMetadata(path string) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 4<<20 {
		return nil, errors.New("metadata unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 4<<20))
}

func nodeOrigin(i audit.Item, root string) bool {
	b, err := readMetadata(filepath.Join(i.Path, "package.json"))
	if err != nil {
		return false
	}
	var m struct {
		Name, Version string
		Resolved      string `json:"_resolved"`
	}
	if json.Unmarshal(b, &m) != nil || m.Name != i.Name || m.Version != i.Version {
		return false
	}
	u, err := url.Parse(m.Resolved)
	return err == nil && u.Scheme == "https" && u.Host == "registry.npmjs.org" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.RawPath == "" && u.Path == "/"+i.Name+"/-/"+filepath.Base(i.Name)+"-"+i.Version+".tgz"
}

func nodeScripts(i audit.Item) bool {
	b, err := readMetadata(filepath.Join(i.Path, "package.json"))
	if err != nil {
		return true
	}
	var m struct{ Scripts map[string]string }
	if json.Unmarshal(b, &m) != nil {
		return true
	}
	for _, name := range []string{"preinstall", "install", "postinstall", "prepare"} {
		if m.Scripts[name] != "" {
			return true
		}
	}
	_, err = os.Stat(filepath.Join(i.Path, "binding.gyp"))
	return err == nil
}

func nodeDirect(i audit.Item, scope, action string) bool {
	// Keep the uninstall source filter; update selectors are verified separately.
	b, err := readMetadata(filepath.Join(scope, "package.json"))
	if err != nil {
		return false
	}
	var m struct{ Dependencies, DevDependencies, OptionalDependencies map[string]string }
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	count := 0
	for _, deps := range []map[string]string{m.Dependencies, m.DevDependencies, m.OptionalDependencies} {
		if spec, ok := deps[i.Name]; ok {
			if action == "uninstall" && (!regexp.MustCompile(`^[A-Za-z0-9*^~<>=|.+ _-]+$`).MatchString(spec) || strings.Contains(spec, " ")) {
				return false
			}
			count++
		}
	}
	return count == 1
}

func installationVerified(i audit.Item, p PlanItem, report audit.Report) bool {
	if p.Provider == "homebrew" && protectedDependencyMayChange(p, report) {
		return false
	}
	switch i.Kind {
	case "formula":
		if p.RuntimeReview != nil {
			return runtimeVersionVerified(i, p, report)
		}
	case "node-package":
		if p.Binding.ResolvedTarget == nil || p.Binding.ResolvedTarget.Node == nil {
			return false
		}
		if i.Owner == "pnpm" {
			store, err := PnpmVirtualStore(i, p.Binding.Scope)
			if err != nil || store != p.Binding.ResolvedTarget.Node.VirtualStore {
				return false
			}
		}
		selector, err := audit.ReadNodeSelector(i.Owner, i.Name, i.Path, p.Binding.Scope)
		if err != nil || selector != p.Binding.ResolvedTarget.Node.Selection {
			return false
		}
		b, err := readMetadata(filepath.Join(i.Path, "package.json"))
		if err != nil {
			return false
		}
		var m struct {
			Name, Version string
			Bin           json.RawMessage
			Directories   struct{ Bin string }
		}
		if json.Unmarshal(b, &m) != nil || m.Name != i.Name || m.Version != i.Version {
			return false
		}
		if len(m.Bin) == 0 || string(m.Bin) == "null" {
			return m.Directories.Bin == ""
		}
		var single string
		bins := map[string]string{}
		if json.Unmarshal(m.Bin, &single) == nil {
			bins[filepath.Base(i.Name)] = single
		} else if json.Unmarshal(m.Bin, &bins) != nil {
			return false
		}
		return nodeBinsVerified(i, p, bins)
	case "python-package":
		digest, err := pythonRequirementHash(i, p.Binding.Scope)
		return pythonMetadataMatches(i) && err == nil && digest == p.Binding.PythonRequirementFingerprint && pythonPolicyAfter(i, p)
	case "cask":
		for _, expected := range p.Applications {
			matched := false
			for _, app := range report.Items {
				if app.ID == expected.ID && app.Path == expected.Path && app.Identity == expected.Identity && app.Kind == "application" && contains(app.Related, i.ID) && app.Version == strings.Split(p.TargetVersion, ",")[0] && app.Health.Status != "error" {
					matched = true
				}
			}
			if !matched {
				return false
			}
		}
	}
	return true
}

func caskSafe(i audit.Item) bool {
	paths, err := filepath.Glob(filepath.Join(i.Path, ".metadata", "*", "*", "Casks", "*.json"))
	if err != nil || len(paths) > 100 {
		return false
	}
	matched := false
	for _, path := range paths {
		b, err := readMetadata(path)
		if err != nil {
			return false
		}
		var m struct {
			Token, Version string
			Artifacts      []map[string]json.RawMessage
		}
		if json.Unmarshal(b, &m) != nil {
			return false
		}
		if m.Token != i.Name || m.Version != i.Version {
			continue
		}
		matched = true
		if len(m.Artifacts) == 0 {
			return false
		}
		for _, artifact := range m.Artifacts {
			for key := range artifact {
				if key != "app" && key != "zap" {
					return false
				}
			}
		}
	}
	return matched
}

func cleanupPaths(i audit.Item, report audit.Report, o Options) ([]string, string) {
	if i.Kind != "cask" {
		return nil, "cleanup_scope_not_supported"
	}
	apps := []audit.Item{}
	for _, a := range report.Items {
		if a.Kind == "application" && contains(a.Related, i.ID) {
			apps = append(apps, a)
		}
	}
	if len(apps) != 1 {
		return nil, "cleanup_requires_unique_application_identity"
	}
	a := apps[0]
	return applicationCleanupPaths(a, report, o)
}

func applicationCleanupPaths(a audit.Item, report audit.Report, o Options) ([]string, string) {
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]+\.[A-Za-z0-9.-]+$`).MatchString(a.Identity) {
		return nil, "cleanup_bundle_identity_unverified"
	}
	for _, other := range report.Items {
		if other.Kind == "application" && other.ID != a.ID && other.Identity == a.Identity {
			return nil, "cleanup_shared_application_identity"
		}
	}
	paths := []string{filepath.Join(o.Home, "Library/Preferences", a.Identity+".plist"), filepath.Join(o.Home, "Library/Caches", a.Identity), filepath.Join(o.Home, "Library/Saved Application State", a.Identity+".savedState")}
	existing := []string{}
	for _, p := range paths {
		st, err := os.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return nil, "cleanup_path_untrusted"
		}
		if err = ownedPath(p, false); err != nil {
			return nil, "cleanup_path_not_owned"
		}
		if err = checkTree(p); err != nil {
			return nil, "cleanup_tree_untrusted"
		}
		existing = append(existing, p)
	}
	sort.Strings(existing)
	return existing, ""
}

func checkTree(root string) error {
	count := 0
	return filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		count++
		if count > 100000 {
			return errors.New("tree too large")
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in cleanup tree")
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		s, ok := st.Sys().(*syscall.Stat_t)
		if !ok || s.Uid != uint32(os.Getuid()) {
			return errors.New("not owned")
		}
		return nil
	})
}

func cleanupHash(paths []string) (string, error) {
	h := sha256.New()
	for _, p := range paths {
		if err := filepath.WalkDir(p, func(path string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return errors.New("cleanup symlink")
			}
			st, e := d.Info()
			if e != nil {
				return e
			}
			_, _ = io.WriteString(h, path+"\x00"+st.ModTime().String()+"\x00"+st.Mode().String()+"\x00")
			if st.Mode().IsRegular() {
				hash, e := fileHash(path)
				if e != nil {
					return e
				}
				_, _ = io.WriteString(h, hash)
			}
			return nil
		}); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Move only enumerated customer data; package managers never receive --zap.
func moveCleanup(paths []string, home, planID string, checkpoints ...func(string) error) error {
	trash := filepath.Join(home, ".Trash")
	if err := os.MkdirAll(trash, 0700); err != nil {
		return err
	}
	st, err := os.Lstat(trash)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("trash unavailable")
	}
	if err = ownedPath(trash, false); err != nil {
		return errors.New("trash not owned")
	}
	for _, p := range paths {
		if err := ownedPath(p, false); err != nil {
			return err
		}
		if err := checkTree(p); err != nil {
			return err
		}
		container, err := os.MkdirTemp(trash, "KeepUpdated-"+planID[:12]+"-")
		if err != nil {
			return err
		}
		destination := filepath.Join(container, filepath.Base(p))
		for _, checkpoint := range checkpoints {
			if err := checkpoint(destination); err != nil {
				return err
			}
		}
		if err = os.Rename(p, destination); err != nil {
			return err
		}
	}
	return nil
}
