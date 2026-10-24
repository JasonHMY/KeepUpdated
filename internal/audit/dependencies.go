package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

type Dependency struct {
	Name         string `json:"name"`
	Relationship string `json:"relationship,omitempty"`
	Constraint   string `json:"constraint,omitempty"`
	Installed    string `json:"installed_version,omitempty"`
	Status       string `json:"status"`
	Code         string `json:"code"`
}

func depResult(name, spec, version string, matched, known bool) Dependency {
	d := Dependency{Name: name, Constraint: spec, Installed: version, Status: "unknown", Code: "constraint_not_supported"}
	if known {
		d.Status = "ok"
		d.Code = "declared_constraint_satisfied"
		if !matched {
			d.Status = "error"
			d.Code = "declared_constraint_unsatisfied"
		}
	}
	return d
}
func (s *Scanner) dependencies() { s.dependenciesWhere(nil) }

func (s *Scanner) dependenciesWhere(include func(Item) bool) {
	total := 0
	for _, i := range s.Report.Items {
		if dependencyKind(i.Kind) && (include == nil || include(i)) {
			total++
		}
	}
	progress := s.progress("dependencies", total)
	defer progress.finish()
	py := map[string]map[string][]Item{}
	envVersion := map[string]string{}
	for _, i := range s.Report.Items {
		if s.cancelled() {
			return
		}
		if i.Kind == "python-package" && i.Identity != "" {
			root := real(filepath.Dir(i.Path))
			if py[root] == nil {
				py[root] = map[string][]Item{}
			}
			py[root][normalizePy(i.Identity)] = append(py[root][normalizePy(i.Identity)], i)
		}
		if i.Kind == "python-environment" {
			// pyvenv.cfg records the creator's version; a replaced interpreter may differ.
			interpreter := real(filepath.Join(i.Path, "bin/python"))
			for _, runtime := range s.Report.Items {
				if interpreter == "" || runtime.Kind != "executable" || runtime.Health.Status != "ok" || runtime.Version == "" || real(runtime.Path) != interpreter {
					continue
				}
				for _, evidence := range runtime.Evidence {
					if evidence == "runtime version probe" && (runtime.Name == "python" || runtime.Name == "python3") {
						envVersion[real(i.Path)] = runtime.Version
					}
				}
			}
		}
	}
	for n := range s.Report.Items {
		if s.cancelled() {
			return
		}
		i := &s.Report.Items[n]
		if !dependencyKind(i.Kind) || include != nil && !include(*i) {
			continue
		}
		progress.begin(i.Name)
		s.inspectDependencies(i, py, envVersion)
		progress.done(i.Health.Status != "ok")
	}
}
func dependencyKind(kind string) bool {
	return kind == "formula" || kind == "cask" || kind == "node-package" || kind == "python-package"
}
func (s *Scanner) inspectDependencies(i *Item, py map[string]map[string][]Item, envVersion map[string]string) {
	switch i.Kind {
	case "formula", "cask":
		s.brewDependencies(i)
	case "node-package":
		s.nodeDependencies(i)
	case "python-package":
		s.pythonDependencies(i, py[real(filepath.Dir(i.Path))], envVersion)
	}
	if i.Dependencies == nil {
		return
	}
	sort.SliceStable(i.Dependencies, func(a, b int) bool { return i.Dependencies[a].Name < i.Dependencies[b].Name })
	if i.Health.Status == "error" {
		return
	}
	status, code := "ok", "declared_dependencies_checked"
	for _, d := range i.Dependencies {
		if d.Status == "error" {
			status, code = "error", "dependency_issue"
			break
		}
		if d.Status == "unknown" {
			status, code = "unknown", "dependencies_partially_checked"
		}
	}
	i.Health = result(status, code)
}

type brewRuntimeDependency struct {
	FullName string `json:"full_name"`
	Version  string `json:"version"`
}

func (s *Scanner) brewDependencies(i *Item) {
	var dependencies []brewRuntimeDependency
	prefix := filepath.Dir(filepath.Dir(i.Path))
	cellar := filepath.Join(prefix, "Cellar")
	if i.Kind == "formula" {
		if i.ActiveVersion == "" {
			return
		}
		var receipt struct {
			Runtime []brewRuntimeDependency `json:"runtime_dependencies"`
		}
		b, e := readSmall(filepath.Join(i.Path, i.ActiveVersion, "INSTALL_RECEIPT.json"))
		if e != nil || json.Unmarshal(b, &receipt) != nil {
			i.Dependencies = []Dependency{{Name: "declarations", Status: "unknown", Code: "dependency_receipt_unreadable"}}
			return
		}
		dependencies = receipt.Runtime
	} else {
		var receipt struct {
			Runtime struct {
				Formula []brewRuntimeDependency `json:"formula"`
			} `json:"runtime_dependencies"`
		}
		b, e := readSmall(filepath.Join(i.Path, ".metadata/INSTALL_RECEIPT.json"))
		if e != nil || json.Unmarshal(b, &receipt) != nil {
			i.Dependencies = []Dependency{{Name: "declarations", Status: "unknown", Code: "dependency_receipt_unreadable"}}
			return
		}
		dependencies = receipt.Runtime.Formula
	}
	if dependencies == nil {
		i.Dependencies = []Dependency{{Name: "declarations", Status: "unknown", Code: "dependency_declarations_unavailable"}}
		return
	}
	i.Dependencies = []Dependency{}
	for _, req := range dependencies {
		name := filepath.Base(req.FullName)
		d := Dependency{Name: req.FullName, Relationship: "dependency", Status: "unknown", Code: "dependency_identity_unresolved"}
		parts := strings.Split(req.FullName, "/")
		valid := len(parts) == 1 || len(parts) == 3
		for _, part := range parts {
			valid = valid && brewName.MatchString(part)
		}
		if !valid {
			i.Dependencies = append(i.Dependencies, d)
			continue
		}
		rack := filepath.Join(cellar, name)
		st, err := os.Stat(rack)
		if os.IsNotExist(err) {
			d.Status = "error"
			d.Code = "dependency_missing"
		} else if err != nil || !st.IsDir() {
			d.Code = "dependency_unreadable"
		} else {
			target := real(filepath.Join(prefix, "opt", name))
			if target == "" || real(rack) == "" || filepath.Dir(target) != real(rack) {
				d.Code = "dependency_active_link_unresolved"
			} else if st, err := os.Stat(target); err != nil || !st.IsDir() || syscall.Access(target, 5) != nil {
				d.Code = "dependency_unreadable"
			} else {
				d.Installed = filepath.Base(target)
				d.Status = "ok"
				d.Code = "dependency_active_keg_present"
				var receipt struct {
					Arch   string `json:"arch"`
					Source struct {
						Tap string `json:"tap"`
					} `json:"source"`
				}
				rb, re := readSmall(filepath.Join(target, "INSTALL_RECEIPT.json"))
				if re != nil || json.Unmarshal(rb, &receipt) != nil {
					d.Status = "unknown"
					d.Code = "dependency_receipt_unreadable"
				} else {
					wantTap := "homebrew/core"
					if len(parts) == 3 {
						wantTap = strings.Join(parts[:2], "/")
					}
					if receipt.Source.Tap != wantTap {
						d.Status = "unknown"
						d.Code = "dependency_source_mismatch"
					}
					if len(i.Architecture) == 1 && receipt.Arch != "" && i.Architecture[0] != receipt.Arch {
						d.Status = "error"
						d.Code = "dependency_architecture_mismatch"
					}
				}
			}
		}
		// Receipt versions record installation history, not a minimum-version requirement.
		i.Dependencies = append(i.Dependencies, d)
	}
}

type nodeMetadata struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Dependencies map[string]string `json:"dependencies"`
	Optional     map[string]string `json:"optionalDependencies"`
	Peers        map[string]string `json:"peerDependencies"`
	PeerMeta     map[string]struct {
		Optional bool `json:"optional"`
	} `json:"peerDependenciesMeta"`
	Engines map[string]string `json:"engines"`
	Bin     json.RawMessage   `json:"bin"`
}

func (s *Scanner) nodeDependencies(i *Item) {
	b, e := readSmall(filepath.Join(i.Path, "package.json"))
	var m nodeMetadata
	if e != nil || json.Unmarshal(b, &m) != nil {
		i.Dependencies = []Dependency{{Name: "declarations", Status: "unknown", Code: "dependency_metadata_unreadable"}}
		return
	}
	i.Dependencies = []Dependency{}
	type request struct {
		name, spec, relationship string
		optional, peer           bool
	}
	reqs := []request{}
	for k, v := range m.Dependencies {
		if _, overridden := m.Optional[k]; !overridden {
			reqs = append(reqs, request{k, v, "dependency", false, false})
		}
	}
	for k, v := range m.Optional {
		reqs = append(reqs, request{k, v, "optional", true, false})
	}
	for k, v := range m.Peers {
		relationship := "peer"
		if m.PeerMeta[k].Optional {
			relationship = "optional-peer"
		}
		reqs = append(reqs, request{k, v, relationship, m.PeerMeta[k].Optional, true})
	}
	sort.Slice(reqs, func(a, b int) bool {
		if reqs[a].name == reqs[b].name {
			return reqs[a].relationship < reqs[b].relationship
		}
		return reqs[a].name < reqs[b].name
	})
	for _, req := range reqs {
		name, spec := req.name, req.spec
		d := Dependency{Name: name, Relationship: req.relationship, Status: "unknown", Code: "dependency_identity_unresolved"}
		if !packageName.MatchString(name) {
			i.Dependencies = append(i.Dependencies, d)
			continue
		}
		root := real(i.Path)
		if root == "" {
			d.Code = "dependency_search_path_unresolved"
			i.Dependencies = append(i.Dependencies, d)
			continue
		}
		if req.peer {
			root = filepath.Dir(root)
			if strings.HasPrefix(filepath.Base(root), "@") {
				root = filepath.Dir(root)
			}
		}
		p, code := resolveNodePackage(root, name)
		if p == "" {
			d.Code = code
			if code == "dependency_missing" {
				d.Status = "error"
				if req.optional {
					d.Status = "skipped"
					d.Code = "optional_dependency_absent"
				}
			}
		} else {
			var installed nodeMetadata
			b, e := readSmall(filepath.Join(p, "package.json"))
			if e != nil || json.Unmarshal(b, &installed) != nil {
				d.Code = "dependency_metadata_unreadable"
			} else if installed.Name != name {
				d.Code = "dependency_identity_mismatch"
			} else {
				matched, known := nodeConstraint(installed.Version, spec)
				d = depResult(name, "", installed.Version, matched, known)
				if known {
					d.Constraint = spec
				}
				d.Relationship = req.relationship
			}
		}
		i.Dependencies = append(i.Dependencies, d)
	}
	if i.Owner == "volta" {
		d := Dependency{Name: "runtime:node", Relationship: "runtime", Status: "unknown", Code: "volta_runtime_not_probed"}
		for _, related := range i.Related {
			for _, runtime := range s.Report.Items {
				if runtime.ID == related && runtime.Kind == "runtime" && runtime.Name == "node" {
					d.Installed = runtime.Version
				}
			}
		}
		i.Dependencies = append(i.Dependencies, d)
		return
	}
	if spec := m.Engines["node"]; spec != "" {
		version := ""
		bound := false
		for _, x := range s.Report.Items {
			if x.Kind == "executable" && x.Name == "node" && x.Active {
				version = x.ActiveVersion
				bound = s.nodeRuntimeBound(i, m, x)
				break
			}
		}
		matched, known := nodeConstraint(version, spec)
		d := depResult("runtime:node", "", version, matched, known)
		d.Relationship = "runtime"
		if !bound {
			d.Status, d.Code = "unknown", "runtime_binding_unverified"
		} else if known {
			d.Constraint = spec
			d.Code = "PATH_runtime_constraint_satisfied"
			if !matched {
				d.Code = "PATH_runtime_constraint_unsatisfied"
			}
		}
		i.Dependencies = append(i.Dependencies, d)
	}
}

// A PATH Node version applies only when the active package entry explicitly uses it.
func (s *Scanner) nodeRuntimeBound(i *Item, m nodeMetadata, node Item) bool {
	if !i.Active || i.Owner == "bun" || i.Owner == "volta" {
		return false
	}
	bins := map[string]string{}
	var single string
	if json.Unmarshal(m.Bin, &single) == nil {
		bins[filepath.Base(m.Name)] = single
	} else {
		_ = json.Unmarshal(m.Bin, &bins)
	}
	matched := false
	for name, rel := range bins {
		if filepath.Base(name) != name || filepath.IsAbs(rel) || !within(filepath.Join(i.Path, rel), i.Path) {
			return false
		}
		target := real(filepath.Join(i.Path, rel))
		for _, dir := range filepath.SplitList(s.Config.Path) {
			if !filepath.IsAbs(dir) {
				continue
			}
			entry := filepath.Join(dir, name)
			if !exists(entry) {
				continue
			}
			if target != "" && real(entry) == target {
				st, err := os.Stat(target)
				if err != nil || !st.Mode().IsRegular() || syscall.Access(target, 1) != nil {
					return false
				}
				b, err := readSmall(target)
				if err != nil {
					return false
				}
				line, _, _ := strings.Cut(string(b), "\n")
				words := strings.Fields(strings.TrimPrefix(strings.TrimSuffix(line, "\r"), "#!"))
				if !strings.HasPrefix(line, "#!") || len(words) != 2 || words[0] != "/usr/bin/env" || words[1] != "node" {
					return false
				}
				matched = true
			}
			break
		}
	}
	return matched && node.ActiveVersion != "" && node.Health.Status == "ok"
}
func resolveNodePackage(start, name string) (string, string) {
	for p := start; p != ""; p = filepath.Dir(p) {
		if filepath.Base(p) != "node_modules" {
			q := filepath.Join(p, "node_modules", name)
			st, e := os.Stat(q)
			if e == nil && st.IsDir() {
				if syscall.Access(q, 5) != nil {
					return "", "dependency_unreadable"
				}
				return q, ""
			}
			if e == nil {
				return "", "dependency_not_package_directory"
			}
			if e != nil && !os.IsNotExist(e) {
				return "", "dependency_unreadable"
			}
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return "", "dependency_missing"
}

var pythonRequirement = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9_.-]*)(\[[A-Za-z0-9_., -]+\])?\s*(.*)$`)

func (s *Scanner) pythonDependencies(i *Item, installed map[string][]Item, envVersions map[string]string) {
	if i.pythonSite == "" {
		return
	}
	i.Dependencies = []Dependency{}
	version := envVersions[real(i.pythonEnv)]
	searchClosed := pythonSearchClosed(i.pythonEnv, i.pythonSite)
	if version == "" {
		searchClosed = false
	} else {
		parts := strings.Split(version, ".")
		if len(parts) < 2 || filepath.Base(filepath.Dir(i.pythonSite)) != "python"+strings.Join(parts[:2], ".") {
			searchClosed = false
		}
	}
	env := map[string]string{}
	switch s.Report.OS {
	case "darwin":
		env = map[string]string{"os_name": "posix", "sys_platform": "darwin", "platform_system": "Darwin"}
	case "linux":
		env = map[string]string{"os_name": "posix", "sys_platform": "linux", "platform_system": "Linux"}
	case "windows":
		env = map[string]string{"os_name": "nt", "sys_platform": "win32", "platform_system": "Windows"}
	}
	if version != "" {
		env["python_full_version"] = version
		parts := strings.Split(version, ".")
		if len(parts) >= 2 {
			env["python_version"] = strings.Join(parts[:2], ".")
		}
	}
	if i.requiresPython != "" {
		matched, known := pythonConstraint(version, i.requiresPython)
		d := depResult("runtime:python", "", version, matched, known)
		d.Relationship = "runtime"
		if version == "" {
			d.Code = "runtime_version_unverified"
		}
		if known {
			d.Constraint = i.requiresPython
		}
		i.Dependencies = append(i.Dependencies, d)
	}
	for _, raw := range i.pythonRequires {
		req, marker, hasMarker := strings.Cut(raw, ";")
		m := pythonRequirement.FindStringSubmatch(req)
		if m == nil {
			i.Dependencies = append(i.Dependencies, Dependency{Name: "unparsed-requirement", Status: "unknown", Code: "requirement_not_supported"})
			continue
		}
		name, spec := m[1], strings.TrimSpace(m[3])
		d := Dependency{Name: name, Relationship: "dependency", Status: "unknown", Code: "constraint_not_supported"}
		if hasMarker {
			applicable, known := evaluateMarker(marker, env)
			if !known {
				d.Code = "marker_not_supported"
				i.Dependencies = append(i.Dependencies, d)
				continue
			}
			if !applicable {
				d.Status = "skipped"
				d.Code = "marker_not_applicable"
				i.Dependencies = append(i.Dependencies, d)
				continue
			}
		}
		candidates := installed[normalizePy(name)]
		if len(candidates) == 0 {
			if searchClosed {
				d.Status = "error"
				d.Code = "dependency_missing"
			} else {
				d.Code = "dependency_not_found_in_scanned_paths"
			}
		} else if len(candidates) > 1 {
			d.Code = "multiple_installed_distributions"
		} else if !searchClosed {
			d.Code = "python_search_path_not_verified"
		} else {
			matched, known := pythonConstraint(candidates[0].Version, spec)
			d = depResult(name, "", candidates[0].Version, matched, known)
			d.Relationship = "dependency"
			if known {
				d.Constraint = spec
			}
			if m[2] != "" && d.Status == "ok" {
				d.Status = "unknown"
				d.Code = "dependency_extras_not_evaluated"
			}
		}
		i.Dependencies = append(i.Dependencies, d)
	}
}

// Only an explicitly isolated venv without path hooks supports a definitive missing result.
func pythonSearchClosed(env, site string) bool {
	root, resolvedSite := real(env), real(site)
	if root == "" || resolvedSite == "" || !within(resolvedSite, root) || os.Getenv("PYTHONPATH") != "" || os.Getenv("PYTHONHOME") != "" {
		return false
	}
	b, err := readSmall(filepath.Join(env, "pyvenv.cfg"))
	if err != nil {
		return false
	}
	isolated := false
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == "include-system-site-packages" {
			isolated = strings.EqualFold(strings.TrimSpace(v), "false")
		}
	}
	entries, err := os.ReadDir(site)
	if !isolated || err != nil || exists(filepath.Join(site, "sitecustomize.py")) || exists(filepath.Join(site, "usercustomize.py")) {
		return false
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".pth") || strings.HasPrefix(entry.Name(), "sitecustomize.") || strings.HasPrefix(entry.Name(), "usercustomize.") || entry.Name() == "sitecustomize" || entry.Name() == "usercustomize" {
			return false
		}
	}
	for _, path := range glob(filepath.Join(env, "lib/python*")) {
		for _, name := range []string{"sitecustomize.py", "sitecustomize.pyc", "sitecustomize", "usercustomize.py", "usercustomize.pyc", "usercustomize"} {
			if exists(filepath.Join(path, name)) {
				return false
			}
		}
	}
	return true
}
