package audit

import (
	"bufio"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var packageName = regexp.MustCompile(`^(?:@[a-z0-9._-]+/)?[a-z0-9][a-z0-9._-]*$`)

func (s *Scanner) packages() {
	s.voltaInventory()
	h := s.Config.Home
	roots := map[string]string{}
	for _, b := range s.Config.BrewRoots {
		roots[filepath.Join(b, "lib/node_modules")] = "npm"
	}
	for _, d := range filepath.SplitList(s.Config.Path) {
		if filepath.IsAbs(d) {
			roots[filepath.Join(filepath.Dir(d), "lib/node_modules")] = "npm"
		}
	}
	for _, p := range []string{filepath.Join(h, ".npm-global/lib/node_modules"), filepath.Join(h, ".local/lib/node_modules")} {
		roots[p] = "npm"
	}
	for _, pattern := range []string{filepath.Join(h, ".nvm/versions/node/*/lib/node_modules"), filepath.Join(h, ".local/share/fnm/node-versions/*/installation/lib/node_modules")} {
		for _, p := range glob(pattern) {
			roots[p] = "npm"
		}
	}
	for _, pattern := range []string{filepath.Join(h, "Library/pnpm/global/*/node_modules"), filepath.Join(h, ".local/share/pnpm/global/*/node_modules")} {
		for _, p := range glob(pattern) {
			roots[p] = "pnpm"
		}
	}
	roots[filepath.Join(h, ".bun/install/global/node_modules")] = "bun"
	for owner, root := range s.Config.ManagerRoots {
		if !validManagerRoot(root) {
			continue
		}
		switch owner {
		case "bun":
			roots[filepath.Join(root, "node_modules")] = owner
		case "pnpm":
			for _, p := range glob(filepath.Join(root, "*/node_modules")) {
				roots[p] = owner
			}
		case "pipx", "uv":
			for _, d := range s.entries("python-tools", root) {
				if d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
					s.pythonEnv(filepath.Join(root, d.Name()), owner)
				}
			}
		}
	}
	for p, owner := range roots {
		s.nodeRoot(p, owner)
	}
	for _, base := range []struct{ p, o string }{{filepath.Join(h, ".local/share/pipx/venvs"), "pipx"}, {filepath.Join(h, ".local/pipx/venvs"), "pipx"}, {filepath.Join(h, ".local/share/uv/tools"), "uv"}} {
		for _, d := range s.entries("python-tools", base.p) {
			if d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
				s.pythonEnv(filepath.Join(base.p, d.Name()), base.o)
			}
		}
	}
	s.pythonEnv(filepath.Join(h, ".config/gcloud/virtenv"), "gcloud")
	for _, p := range s.Config.Projects {
		q, e := filepath.Abs(p)
		if e == nil {
			s.pythonEnv(filepath.Join(q, ".venv"), "project")
		}
	}
	pythonPatterns := []string{filepath.Join(h, "Library/Python/*/lib/python/site-packages")}
	escapeRoot := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`)
	for _, root := range s.Config.BrewRoots {
		pythonPatterns = append(pythonPatterns, filepath.Join(escapeRoot.Replace(root), "lib/python*/site-packages"))
	}
	for _, pattern := range pythonPatterns {
		for _, p := range glob(pattern) {
			s.pythonDists(p, "global-python", filepath.Dir(p))
		}
	}
}
func (s *Scanner) nodeRoot(p, owner string) {
	for _, d := range s.entries("node-packages", p) {
		if strings.HasPrefix(d.Name(), ".") {
			continue
		}
		q := filepath.Join(p, d.Name())
		if strings.HasPrefix(d.Name(), "@") {
			for _, sub := range s.entries("node-packages", q) {
				s.nodePackage(filepath.Join(q, sub.Name()), owner)
			}
		} else {
			s.nodePackage(q, owner)
		}
	}
}
func (s *Scanner) nodePackage(p, owner string) {
	if s.seen[item("node-package", "", p, owner).ID] {
		return
	}
	volta := VoltaRoot(p, s.Config.Home, s.Config.ManagerRoots)
	if volta != "" {
		owner = "volta"
	}
	b, e := readSmall(filepath.Join(p, "package.json"))
	if os.IsNotExist(e) {
		if st, err := os.Lstat(p); err == nil && (st.Mode()&os.ModeSymlink != 0 || volta != "") {
			i := item("node-package", filepath.Base(p), p, owner)
			i.Health = result("error", "package_link_or_metadata_missing")
			if volta != "" {
				voltaManual(&i, volta)
			}
			s.add(i)
		}
		return
	}
	i := item("node-package", filepath.Base(p), p, owner)
	if volta != "" {
		voltaManual(&i, volta)
	}
	var m struct {
		Name     string            `json:"name"`
		Version  string            `json:"version"`
		Engines  map[string]string `json:"engines"`
		Bin      any               `json:"bin"`
		Resolved json.RawMessage   `json:"_resolved"`
	}
	if e != nil || json.Unmarshal(b, &m) != nil {
		i.Health = result("error", "package_metadata_unreadable")
		s.add(i)
		return
	}
	i.Name = m.Name
	i.Identity = m.Name
	i.Version = m.Version
	i.Evidence = append(i.Evidence, "package.json")
	// An installation record establishes declared origin, never current registry settings or content integrity.
	i.Source = "npm-public-candidate"
	if evidence := npmPublicProvenance(p, owner, m.Name, m.Version, m.Resolved); evidence != "" {
		i.Source = "npm-public"
		i.Evidence = append(i.Evidence, evidence)
	}
	// Channel describes the public candidate query, not the original request.
	i.Channel = "latest"
	root := filepath.Dir(p)
	if strings.HasPrefix(i.Name, "@") {
		root = filepath.Dir(root)
	}
	scope := filepath.Dir(root)
	if owner == "npm" {
		scope = filepath.Dir(scope)
	}
	if selector, err := ReadNodeSelector(owner, i.Name, p, scope); err == nil {
		i.NodeSelector = &selector
	}
	if packageName.MatchString(m.Name) {
		i.lookup = m.Name
	}
	i.Health = result("ok", "package_metadata_readable")
	if v := m.Engines["node"]; v != "" {
		i.Evidence = append(i.Evidence, "requires-node:"+v)
	}
	bins := map[string]string{}
	switch v := m.Bin.(type) {
	case string:
		bins[filepath.Base(m.Name)] = v
	case map[string]any:
		for n, p := range v {
			bins[n] = str(p)
		}
	}
	for n, pth := range bins {
		if filepath.Base(n) != n || filepath.IsAbs(pth) || !within(filepath.Join(p, pth), p) {
			continue
		}
		target := real(filepath.Join(p, pth))
		if target == "" {
			continue
		}
		for _, dir := range filepath.SplitList(s.Config.Path) {
			if !filepath.IsAbs(dir) {
				continue
			}
			candidate := filepath.Join(dir, n)
			st, err := os.Stat(candidate)
			if err != nil || !st.Mode().IsRegular() || syscall.Access(candidate, 1) != nil {
				continue
			}
			if real(candidate) == target {
				i.Active = true
				i.ActivityKnown = true
				i.ActiveVersion = i.Version
				i.Evidence = append(i.Evidence, "PATH entry resolves to package bin")
				break
			}
			if exists(candidate) {
				break
			}
		}
	}
	if volta != "" {
		s.voltaPackage(&i, volta)
	}
	s.add(i)
}

var npmTarballVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[A-Za-z0-9.-]+)?(?:\+[A-Za-z0-9.-]+)?$`)

func npmPublicTarball(raw, name, version string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "registry.npmjs.org" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" {
		return false
	}
	return u.Path == "/"+name+"/-/"+filepath.Base(name)+"-"+version+".tgz"
}

// Read only npm's installed metadata, never npmrc, auth, cache or manager configuration.
func npmPublicProvenance(path, owner, name, version string, resolved json.RawMessage) string {
	if owner != "npm" || !packageName.MatchString(name) || !npmTarballVersion.MatchString(version) {
		return ""
	}
	root := filepath.Dir(path)
	if strings.HasPrefix(name, "@") {
		root = filepath.Dir(root)
	}
	if filepath.Base(root) != "node_modules" || filepath.Clean(path) != filepath.Join(root, filepath.FromSlash(name)) {
		return ""
	}
	resolvedRoot, resolvedPackage := real(root), real(path)
	if resolvedRoot == "" || resolvedPackage != filepath.Join(resolvedRoot, filepath.FromSlash(name)) {
		return ""
	}
	metadata, err := os.Lstat(filepath.Join(path, "package.json"))
	if err != nil || !metadata.Mode().IsRegular() {
		return ""
	}
	if len(resolved) > 0 {
		var recorded string
		if json.Unmarshal(resolved, &recorded) != nil {
			return ""
		}
		if recorded != "" {
			if npmPublicTarball(recorded, name, version) {
				return "npm public origin recorded in package.json _resolved"
			}
			return ""
		}
	}
	lockPath := filepath.Join(root, ".package-lock.json")
	lock, err := os.Lstat(lockPath)
	if err != nil || !lock.Mode().IsRegular() || lock.ModTime().Before(metadata.ModTime()) {
		return ""
	}
	installed, err := os.Stat(path)
	if err != nil || !installed.IsDir() || lock.ModTime().Before(installed.ModTime()) {
		return ""
	}
	var hidden struct {
		Version  int `json:"lockfileVersion"`
		Packages map[string]struct {
			Name     string `json:"name"`
			Version  string `json:"version"`
			Resolved string `json:"resolved"`
			Link     bool   `json:"link"`
		} `json:"packages"`
	}
	b, err := readSmall(lockPath)
	if err != nil || json.Unmarshal(b, &hidden) != nil || hidden.Version != 3 {
		return ""
	}
	entry, ok := hidden.Packages["node_modules/"+name]
	if !ok || entry.Link || entry.Version != version || entry.Name != "" && entry.Name != name || !npmPublicTarball(entry.Resolved, name, version) {
		return ""
	}
	return "npm public origin recorded in global hidden lockfile"
}

func (s *Scanner) pythonEnv(p, owner string) {
	if !exists(p) {
		return
	}
	i := item("python-environment", filepath.Base(p), p, owner)
	i.Source = owner
	py := filepath.Join(p, "bin/python")
	resolved := real(py)
	if resolved == "" {
		i.Health = result("error", "interpreter_missing_or_broken")
	} else {
		st, err := os.Stat(py)
		if err != nil || !st.Mode().IsRegular() || syscall.Access(py, 1) != nil {
			i.Health = result("error", "interpreter_not_executable")
		} else {
			i.Health = result("ok", "interpreter_link_resolved")
		}
		i.Evidence = append(i.Evidence, "interpreter:"+resolved)
	}
	if b, e := readSmall(filepath.Join(p, "pyvenv.cfg")); e == nil {
		for _, l := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(l, "=")
			if ok && strings.TrimSpace(k) == "version" {
				i.Version = strings.TrimSpace(v)
			}
		}
	}
	i.Update = result("unsupported", "environment_not_update_target")
	s.add(i)
	for _, d := range glob(filepath.Join(p, "lib/python*/site-packages")) {
		s.pythonDists(d, owner, p)
	}
}
func (s *Scanner) pythonDists(p, owner, env string) {
	for _, d := range s.entries("python-tools", p) {
		if !strings.HasSuffix(d.Name(), ".dist-info") {
			continue
		}
		q := filepath.Join(p, d.Name())
		i := item("python-package", strings.TrimSuffix(d.Name(), ".dist-info"), q, owner)
		i.pythonSite = p
		i.pythonEnv = env
		b, e := readSmall(filepath.Join(q, "METADATA"))
		if e != nil {
			i.Health = result("error", "metadata_unreadable")
			s.add(i)
			continue
		}
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			l := sc.Text()
			if l == "" {
				break
			}
			k, v, ok := strings.Cut(l, ":")
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			switch k {
			case "Name":
				i.Name = v
				i.Identity = v
			case "Version":
				i.Version = v
			case "Requires-Dist":
				i.pythonRequires = append(i.pythonRequires, v)
			case "Requires-Python":
				i.requiresPython = v
				i.Evidence = append(i.Evidence, "requires-python:"+v)
			}
		}
		if sc.Err() != nil {
			i.pythonSite = ""
			i.Health = result("error", "metadata_header_too_large")
			s.add(i)
			continue
		}
		i.Source = "pypi-public-candidate"
		i.Channel = "stable"
		i.lookup = i.Name
		i.Health = result("unknown", "dependencies_not_evaluated")
		i.Evidence = append(i.Evidence, "environment:"+env)
		if exists(filepath.Join(filepath.Dir(p), "EXTERNALLY-MANAGED")) {
			i.Evidence = append(i.Evidence, "PEP-668 externally managed")
		}
		if exists(filepath.Join(q, "direct_url.json")) {
			i.Source = "direct-url"
			i.lookup = ""
			i.Update = result("unsupported", "direct_install_not_registry")
		}
		s.add(i)
	}
}
