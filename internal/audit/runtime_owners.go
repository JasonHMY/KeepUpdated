package audit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// VoltaRoot recognizes containment, not authenticity or permission to execute.
// The path check also protects older inventories that labelled inner tools npm.
func VoltaRoot(path, home string, roots map[string]string) string {
	candidates := []string{filepath.Join(home, ".volta")}
	if validManagerRoot(roots["volta"]) {
		candidates = append([]string{roots["volta"]}, candidates...)
	}
	for _, p := range []string{path, real(path)} {
		if !filepath.IsAbs(p) {
			continue
		}
		for _, root := range candidates {
			for _, r := range []string{root, real(root)} {
				if r != "" && within(p, r) {
					return root
				}
			}
		}
		for parent := filepath.Dir(p); parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
			if st, err := os.Lstat(filepath.Join(parent, "layout.v4")); err == nil && st.Mode().IsRegular() {
				return parent
			}
		}
	}
	return ""
}

func (s *Scanner) voltaRoots() []string {
	roots := []string{filepath.Join(s.Config.Home, ".volta")}
	if validManagerRoot(s.Config.ManagerRoots["volta"]) {
		roots = append(roots, s.Config.ManagerRoots["volta"])
	}
	for _, dir := range filepath.SplitList(s.Config.Path) {
		if filepath.IsAbs(dir) && filepath.Base(dir) == "bin" {
			if root := VoltaRoot(dir, s.Config.Home, s.Config.ManagerRoots); root != "" {
				roots = append(roots, root)
			}
		}
	}
	return uniq(roots)
}

// Only the currently verified Unix layout is interpreted. Unknown records still
// retain outer ownership, but cannot establish a selected platform or consumer.
func voltaLayout(root string) bool {
	st, err := os.Lstat(filepath.Join(root, "layout.v4"))
	return err == nil && st.Mode().IsRegular()
}
func voltaJSON(root, path string, out any) bool {
	rp, rr := real(path), real(root)
	if rp == "" || rr == "" || !within(rp, rr) {
		return false
	}
	b, err := readSmall(path)
	if err != nil || !voltaObject(b) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(out) == nil
}
func voltaObject(b []byte) bool {
	return voltaObjectDepth(b, 0)
}
func voltaObjectDepth(b []byte, depth int) bool {
	// Supported records have only a root and one platform object; bound copies before decoding.
	if depth > 1 {
		return false
	}
	fields, err := nodeObject(b)
	if err != nil {
		return false
	}
	for key, value := range fields {
		v := bytes.TrimSpace(value)
		if len(v) > 0 && v[0] == '"' && (key == "node" || key == "npm" || key == "pnpm" || key == "yarn" || key == "runtime") {
			var version string
			if json.Unmarshal(v, &version) != nil || !npmTarballVersion.MatchString(version) {
				return false
			}
		}
		if len(v) > 0 && v[0] == '{' && !voltaObjectDepth(v, depth+1) {
			return false
		}
	}
	return true
}

type voltaPlatform struct {
	Node string `json:"node"`
	Npm  string `json:"npm"`
	Pnpm string `json:"pnpm"`
	Yarn string `json:"yarn"`
}

func (p voltaPlatform) valid() bool {
	if !npmTarballVersion.MatchString(p.Node) {
		return false
	}
	return p.validOptional()
}

func (p voltaPlatform) validOptional() bool {
	for _, v := range []string{p.Node, p.Npm, p.Pnpm, p.Yarn} {
		if v != "" && !npmTarballVersion.MatchString(v) {
			return false
		}
	}
	return true
}
func (p voltaPlatform) versions() map[string]string {
	return map[string]string{"node": p.Node, "npm": p.Npm, "pnpm": p.Pnpm, "yarn": p.Yarn}
}

type voltaPackageRecord struct {
	Name     string        `json:"name"`
	Version  string        `json:"version"`
	Platform voltaPlatform `json:"platform"`
	Bins     []string      `json:"bins"`
	Manager  string        `json:"manager"`
}
type voltaBinRecord struct {
	Name     string        `json:"name"`
	Package  string        `json:"package"`
	Version  string        `json:"version"`
	Platform voltaPlatform `json:"platform"`
	Manager  string        `json:"manager"`
}

func voltaManual(i *Item, root string) {
	i.Owner, i.Source, i.Channel = "volta", "volta", "unknown"
	i.lookup, i.NodeSelector = "", nil
	i.Active, i.ActivityKnown, i.ActiveVersion = false, false, ""
	i.Update = result("unsupported", "volta_owner_manual")
	i.Evidence = uniq(append(i.Evidence, "volta-root:"+root))
}

func (s *Scanner) voltaInventory() {
	for _, root := range s.voltaRoots() {
		var defaults struct {
			Node *struct {
				Runtime string `json:"runtime"`
				Npm     string `json:"npm"`
			} `json:"node"`
			Pnpm string `json:"pnpm"`
			Yarn string `json:"yarn"`
		}
		platformFile := filepath.Join(root, "tools/user/platform.json")
		known := voltaLayout(root) && voltaJSON(root, platformFile, &defaults)
		selected := voltaPlatform{Pnpm: defaults.Pnpm, Yarn: defaults.Yarn}
		if defaults.Node != nil {
			selected.Node, selected.Npm = defaults.Node.Runtime, defaults.Node.Npm
		}
		known = known && selected.validOptional() && (defaults.Node == nil || selected.Node != "")
		for _, tool := range []string{"node", "npm", "pnpm", "yarn"} {
			base := filepath.Join(root, "tools/image", tool)
			versions := []string{}
			for _, entry := range s.entries("volta", base) {
				if entry.IsDir() && npmTarballVersion.MatchString(entry.Name()) {
					versions = append(versions, entry.Name())
				}
			}
			for _, v := range versions {
				p := filepath.Join(base, v)
				i := item("runtime", tool, p, "volta")
				voltaManual(&i, root)
				i.Version, i.Versions = v, versions
				i.Evidence = append(i.Evidence, "volta-image-version-directory:"+p)
				i.Health = result("unknown", "volta_metadata_unverified")
				if known && real(p) != "" && within(real(p), real(root)) {
					i.ActivityKnown, i.Active = true, selected.versions()[tool] == v
					i.Evidence = append(i.Evidence, "volta-default-platform:"+platformFile)
					i.Health = result("unknown", "volta_retained_image")
					if i.Active {
						i.ActiveVersion = v
						i.Health.Code = "volta_default_selected"
					}
				}
				s.add(i)
			}
		}
		for _, image := range glob(filepath.Join(root, "tools/image/node/*/lib/node_modules")) {
			s.nodeRoot(image, "volta")
		}
		// Package names may be scoped; never recursively search arbitrary projects.
		images := append(glob(filepath.Join(root, "tools/image/packages/*")), glob(filepath.Join(root, "tools/image/packages/@*/*"))...)
		for _, image := range images {
			if strings.HasPrefix(filepath.Base(image), "@") {
				continue
			}
			for _, suffix := range []string{"lib/node_modules", "5/node_modules"} {
				s.nodeRoot(filepath.Join(image, suffix), "volta")
			}
		}
	}
	s.voltaProjects()
}

func (s *Scanner) voltaPackage(i *Item, root string) {
	voltaManual(i, root)
	i.Health = result("unknown", "volta_metadata_unverified")
	if !voltaLayout(root) || !packageName.MatchString(i.Name) {
		return
	}
	recordPath := filepath.Join(root, "tools/user/packages", i.Name+".json")
	var record voltaPackageRecord
	if !voltaJSON(root, recordPath, &record) || record.Name != i.Name || record.Version != i.Version || !npmTarballVersion.MatchString(record.Version) || !record.Platform.valid() || record.Bins == nil {
		return
	}
	suffix := "lib/node_modules"
	switch record.Manager {
	case "Npm", "Yarn":
	case "Pnpm":
		suffix = "5/node_modules"
	default:
		return
	}
	expected := filepath.Join(root, "tools/image/packages", record.Name, suffix, record.Name)
	image := real(filepath.Join(root, "tools/image/packages", record.Name))
	if filepath.Clean(i.Path) != expected || image == "" || !within(image, real(root)) || real(i.Path) == "" || !within(real(i.Path), image) || real(filepath.Join(i.Path, "package.json")) == "" || !within(real(filepath.Join(i.Path, "package.json")), image) {
		return
	}
	i.Evidence = append(i.Evidence, "volta-package-record:"+recordPath, "volta-package-manager:"+record.Manager)
	s.voltaConsumers(i, root, record.Platform)
	i.Health = result("unknown", "volta_shim_unverified")
	bins := map[string]bool{}
	for _, name := range record.Bins {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") || bins[name] {
			return
		}
		bins[name] = true
		var bin voltaBinRecord
		path := filepath.Join(root, "tools/user/bins", name+".json")
		if !voltaJSON(root, path, &bin) || bin.Name != name || bin.Package != record.Name || bin.Version != record.Version || bin.Manager != record.Manager || bin.Platform != record.Platform {
			return
		}
		shim := filepath.Join(root, "bin", name)
		st, err := os.Stat(shim)
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
			return
		}
		// Presence is not proof that an arbitrary launcher implements Volta dispatch.
		i.Evidence = append(i.Evidence, "volta-bin-record:"+path)
	}
	i.Health = result("unknown", "volta_platform_recorded")
}

func (s *Scanner) voltaConsumers(i *Item, root string, platform voltaPlatform) {
	for _, tool := range []string{"node", "npm", "pnpm", "yarn"} {
		v := platform.versions()[tool]
		if v == "" {
			continue
		}
		i.Evidence = append(i.Evidence, "volta-platform:"+tool+"@"+v)
		for n := range s.Report.Items {
			runtime := &s.Report.Items[n]
			if runtime.Kind == "runtime" && runtime.Path == filepath.Join(root, "tools/image", tool, v) && runtime.RealPath != "" && within(runtime.RealPath, real(root)) {
				i.Related = uniq(append(i.Related, runtime.ID))
				runtime.Related = uniq(append(runtime.Related, i.ID))
			}
		}
	}
}

func (s *Scanner) voltaProjects() {
	for _, project := range s.Config.Projects {
		project, err := filepath.Abs(project)
		if err != nil {
			continue
		}
		path := filepath.Join(project, "package.json")
		if real(project) == "" || real(path) == "" || !within(real(path), real(project)) {
			continue
		}
		b, err := readSmall(path)
		if err != nil {
			continue
		}
		manifest, err := nodeObject(b)
		if err != nil {
			continue
		}
		raw, ok := manifest["volta"]
		if !ok {
			continue
		}
		i := item("runtime-pin", "Volta project pin", path, "volta")
		i.Source, i.Update = "volta", result("unsupported", "volta_owner_manual")
		i.Health = result("unknown", "volta_project_pin_unverified")
		i.Evidence = append(i.Evidence, "explicit-project-manifest:"+path)
		var pin voltaPlatform
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if voltaObject(raw) && d.Decode(&pin) == nil && pin.validOptional() && pin != (voltaPlatform{}) {
			i.Health.Code = "volta_project_pin_recorded"
			// Pins describe requested versions, not an installation or an active process.
			root := filepath.Join(s.Config.Home, ".volta")
			if validManagerRoot(s.Config.ManagerRoots["volta"]) {
				root = s.Config.ManagerRoots["volta"]
			}
			if voltaLayout(root) {
				s.voltaConsumers(&i, root, pin)
			}
		}
		s.add(i)
	}
}
