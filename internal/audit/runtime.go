package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var runtimeKeg = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*(?:_[0-9]+)?$`)
var pythonBinary = regexp.MustCompile(`^python(?:3(?:\.[0-9]+)?)?$`)

func (s *Scanner) executables() {
	names := []string{"node", "python3", "python", "java", "bun", "go", "npm", "pnpm", "pipx", "uv", "codex", "claude", "grok", "hermes", "openclaw", "gcloud"}
	dirs := filepath.SplitList(s.Config.Path)
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			s.diag("health", "", "relative_or_empty_PATH_entry")
			continue
		}
		if _, e := os.ReadDir(d); e != nil {
			s.diag("health", d, "PATH_directory_unavailable")
		}
	}
	for _, n := range names {
		selected := false
		for _, d := range dirs {
			if !filepath.IsAbs(d) {
				continue
			}
			p := filepath.Join(d, n)
			st, e := os.Lstat(p)
			if os.IsNotExist(e) {
				continue
			}
			i := item("executable", n, p, "PATH")
			if root := VoltaRoot(p, s.Config.Home, s.Config.ManagerRoots); root != "" {
				voltaManual(&i, root)
				i.Health = result("unknown", "volta_shim_unverified")
				if real(p) == "" {
					i.Health = result("error", "broken_or_inaccessible_link")
				}
				selected = true
				s.add(i)
				continue
			}
			if e != nil {
				i.Health = result("error", "entry_unreadable")
				s.add(i)
				continue
			}
			if st.IsDir() {
				continue
			}
			rp := real(p)
			if rp == "" {
				i.Health = result("error", "broken_or_inaccessible_link")
				s.add(i)
				continue
			}
			rs, e := os.Stat(p)
			if e != nil || rs.IsDir() || rs.Mode().Perm()&0111 == 0 || syscall.Access(p, 1) != nil {
				i.Health = result("error", "not_executable")
				s.add(i)
				continue
			}
			i.Active = !selected
			i.ActivityKnown = true
			selected = true
			i.Architecture = architectures(rp)
			i.Health = result("ok", "entry_resolved")
			i.Evidence = append(i.Evidence, "resolved:"+rp)
			for _, br := range s.Config.BrewRoots {
				if within(rp, filepath.Join(br, "Cellar")) {
					i.Source = "homebrew"
					rel, _ := filepath.Rel(filepath.Join(br, "Cellar"), rp)
					parts := strings.Split(rel, string(os.PathSeparator))
					if len(parts) > 1 {
						i.Version = parts[1]
						i.Evidence = append(i.Evidence, "homebrew-keg-version:"+parts[1])
					}
				}
			}
			if i.Source == "unknown" {
				i.Source = "standalone"
			}
			// Only known runtime names are probed; agent CLIs and package shims are never started.
			if n == "node" || n == "python3" || n == "python" || n == "bun" || n == "go" {
				if len(i.Architecture) > 0 && s.trustedRuntime(rp, n) {
					s.probeRuntime(&i, rp, n)
				}
			}
			if n == "codex" && strings.Contains(rp, "/standalone/releases/") {
				v := strings.SplitN(strings.SplitN(rp, "/standalone/releases/", 2)[1], "/", 2)[0]
				v = strings.SplitN(v, "-aarch64-", 2)[0]
				v = strings.SplitN(v, "-x86_64-", 2)[0]
				if numeric.MatchString(v) {
					i.Version = v
					i.Evidence = append(i.Evidence, "native release directory")
				}
			}
			if n == "claude" && strings.Contains(rp, "/claude/versions/") && numeric.MatchString(filepath.Base(rp)) {
				i.Version = filepath.Base(rp)
				i.Evidence = append(i.Evidence, "native release filename")
			}
			if n == "grok" {
				v := strings.TrimPrefix(filepath.Base(rp), "grok-")
				v = strings.SplitN(v, "-macos-", 2)[0]
				if numeric.MatchString(v) {
					i.Version = v
					i.Evidence = append(i.Evidence, "native release filename")
				}
			}
			if n == "gcloud" {
				if b, e := readSmall(filepath.Join(filepath.Dir(filepath.Dir(rp)), "VERSION")); e == nil {
					v := strings.TrimSpace(string(b))
					if numeric.MatchString(v) {
						i.Version = v
						i.Evidence = append(i.Evidence, "SDK VERSION file")
					}
				}
			}
			s.nativeCLI(&i)
			if i.Active {
				i.ActiveVersion = i.Version
			}
			i.Update = result("unsupported", "use_installation_owner")
			if i.Owner == "uv-standalone" {
				i.Update = result("unknown", "native_cli_preview_required")
			}
			s.add(i)
		}
	}
	for _, pattern := range []string{filepath.Join(s.Config.Home, ".nvm/versions/node/*/bin/node"), filepath.Join(s.Config.Home, ".local/share/uv/python/*/bin/python3"), "/Library/Java/JavaVirtualMachines/*/Contents/Home/bin/java", filepath.Join(s.Config.Home, "Library/Java/JavaVirtualMachines/*/Contents/Home/bin/java")} {
		for _, p := range glob(pattern) {
			i := item("runtime", filepath.Base(p), p, "version-manager")
			i.Architecture = architectures(real(p))
			i.Health = result("unknown", "retained_runtime_not_probed")
			i.Update = result("unsupported", "use_installation_owner")
			s.add(i)
		}
	}
}
func (s *Scanner) system() {
	i := item("os", "macOS", "/System/Library/CoreServices/SystemVersion.plist", "system")
	i.Source = "apple"
	i.Channel = "stable"
	i.Architecture = []string{s.Report.Architecture}
	if b, e := s.run("/usr/bin/sw_vers", "-productVersion"); e == nil {
		i.Version = strings.TrimSpace(string(b))
		s.osVersion = i.Version
		i.Active = true
		i.ActivityKnown = true
		i.ActiveVersion = i.Version
		i.Health = result("ok", "system_version_read")
	} else {
		i.Health = result("error", "system_query_failed")
	}
	i.Update = result("unknown", "system_update_query_not_requested")
	if s.Config.SystemUpdates {
		b, e := s.run("/usr/sbin/softwareupdate", "--list")
		if e != nil {
			i.Update = result("error", "system_query_failed")
		} else {
			i.Update = parseSystemUpdateList(string(b))
			if i.Update.Status == "available" {
				i.Evidence = append(i.Evidence, fmt.Sprintf("available-label-count:%d", strings.Count(string(b), "* Label:")))
			}
		}
	}
	s.add(i)
}

// Probe only exact core Homebrew runtime layouts; metadata alone never authorizes execution.
func (s *Scanner) trustedRuntime(p, name string) bool {
	family := name
	if name == "python3" {
		family = "python"
	}
	if family != "python" && family != "node" && family != "bun" && family != "go" {
		return false
	}
	for _, root := range s.Config.BrewRoots {
		cellar := real(filepath.Join(root, "Cellar"))
		if cellar == "" || !within(p, cellar) {
			continue
		}
		rel, _ := filepath.Rel(cellar, p)
		parts := strings.Split(rel, string(os.PathSeparator))
		if len(parts) < 4 || !runtimeKeg.MatchString(parts[1]) {
			continue
		}
		release, _, _ := strings.Cut(parts[1], "_")
		if parts[0] != family {
			formulaVersion, ok := strings.CutPrefix(parts[0], family+"@")
			if !ok || !numericVersion.MatchString(formulaVersion) || !(release == formulaVersion || strings.HasPrefix(release, formulaVersion+".")) {
				continue
			}
		}
		layout := len(parts) == 4 && parts[2] == "bin" && parts[3] == name
		if family == "python" {
			v := strings.Split(release, ".")
			if len(v) < 2 || v[0] != "3" {
				continue
			}
			minor := strings.Join(v[:2], ".")
			if parts[0] != family && parts[0] != "python@"+minor {
				continue
			}
			layout = len(parts) == 4 && parts[2] == "bin" && pythonBinary.MatchString(parts[3]) && (parts[3] == "python" || parts[3] == "python3" || parts[3] == "python"+minor)
			if len(parts) == 8 {
				layout = parts[2] == "Frameworks" && parts[3] == "Python.framework" && parts[4] == "Versions" && parts[5] == minor && parts[6] == "bin" && parts[7] == "python"+minor
			}
		}
		if !layout {
			continue
		}
		var receipt struct {
			Source struct {
				Tap string `json:"tap"`
			} `json:"source"`
		}
		b, e := readSmall(filepath.Join(cellar, parts[0], parts[1], "INSTALL_RECEIPT.json"))
		if e == nil && json.Unmarshal(b, &receipt) == nil && receipt.Source.Tap == "homebrew/core" {
			return true
		}
	}
	return false
}

func hasRuntimeProbe(i Item) bool {
	for _, evidence := range i.Evidence {
		if evidence == "runtime probe attempted" || evidence == "runtime version probe" {
			return true
		}
	}
	return false
}

// Reuse successes and failures by real target, including aliases and environment links.
func (s *Scanner) probeRuntime(i *Item, target, name string) {
	for _, previous := range s.Report.Items {
		if previous.Kind == "executable" && previous.RealPath == target && hasRuntimeProbe(previous) {
			i.Version, i.Health = previous.Version, previous.Health
			i.Evidence = append(i.Evidence, "runtime probe attempted", "runtime probe reused")
			if previous.Health.Status == "ok" && previous.Version != "" {
				i.Evidence = append(i.Evidence, "runtime version probe")
			}
			return
		}
	}
	i.Version = ""
	i.Evidence = append(i.Evidence, "runtime probe attempted")
	args := []string{"--version"}
	if name == "python" || name == "python3" {
		args = []string{"-I", "-S", "--version"}
	}
	if name == "go" {
		args = []string{"version"}
	}
	b, err := s.run(target, args...)
	if err != nil {
		i.Health = result("error", "runtime_probe_failed")
		return
	}
	i.Version = parseRuntimeVersion(name, string(b))
	if i.Version == "" {
		i.Health = result("unknown", "runtime_probe_unrecognized")
		return
	}
	for _, root := range s.Config.BrewRoots {
		cellar := real(filepath.Join(root, "Cellar"))
		if cellar == "" || !within(target, cellar) {
			continue
		}
		rel, _ := filepath.Rel(cellar, target)
		parts := strings.Split(rel, string(os.PathSeparator))
		if len(parts) < 2 {
			continue
		}
		release, _, _ := strings.Cut(parts[1], "_")
		if cmp, known := compare(i.Version, release); !known || cmp != 0 {
			i.Health = result("unknown", "runtime_version_metadata_mismatch")
			return
		}
	}
	i.Health = result("ok", "runtime_version_verified")
	i.Evidence = append(i.Evidence, "runtime version probe")
}

func parseRuntimeVersion(name, raw string) string {
	text := strings.TrimSpace(raw)
	switch name {
	case "python", "python3":
		var ok bool
		text, ok = strings.CutPrefix(text, "Python ")
		if !ok {
			return ""
		}
	case "node":
		var ok bool
		text, ok = strings.CutPrefix(text, "v")
		if !ok {
			return ""
		}
	case "go":
		var ok bool
		text, ok = strings.CutPrefix(text, "go version go")
		if !ok {
			return ""
		}
		text, _, _ = strings.Cut(text, " ")
	case "bun":
	default:
		return ""
	}
	if !numericVersion.MatchString(text) {
		return ""
	}
	if _, known := compare(text, text); !known {
		return ""
	}
	return text
}

// Only a trusted interpreter's isolated version switch runs; environment packages are never loaded.
func (s *Scanner) environmentRuntimes() {
	count := len(s.Report.Items)
	for n := 0; n < count; n++ {
		environment := s.Report.Items[n]
		if environment.Kind != "python-environment" {
			continue
		}
		if environment.Version != "" && numericVersion.MatchString(environment.Version) {
			environment.Evidence = append(environment.Evidence, "creation-version:"+environment.Version)
		}
		environment.Version = ""
		environment.ActiveVersion = ""
		target := real(filepath.Join(environment.Path, "bin/python"))
		if environment.Health.Status == "error" || target == "" {
			s.Report.Items[n] = environment
			continue
		}
		arch := architectures(target)
		st, err := os.Stat(target)
		if err != nil || !st.Mode().IsRegular() || syscall.Access(target, 1) != nil || len(arch) == 0 || !s.trustedRuntime(target, "python3") {
			environment.Health = result("unknown", "interpreter_version_unverified")
			s.Report.Items[n] = environment
			continue
		}
		runtime := item("executable", "python3", target, "environment-runtime")
		runtime.Architecture, runtime.Source = arch, "homebrew"
		runtime.Update = result("unsupported", "use_installation_owner")
		s.probeRuntime(&runtime, target, "python3")
		if runtime.Health.Status == "ok" {
			environment.Version = runtime.Version
		}
		environment.Health = runtime.Health
		if runtime.Version != "" {
			environment.Evidence = append(environment.Evidence, "runtime version probe")
		}
		found := -1
		for j, previous := range s.Report.Items {
			if previous.Kind == "executable" && previous.RealPath == target && hasRuntimeProbe(previous) {
				found = j
				break
			}
		}
		if found >= 0 {
			environment.Related = append(environment.Related, s.Report.Items[found].ID)
			s.Report.Items[found].Related = append(s.Report.Items[found].Related, environment.ID)
		} else {
			environment.Related = append(environment.Related, runtime.ID)
			runtime.Related = append(runtime.Related, environment.ID)
			s.add(runtime)
		}
		s.Report.Items[n] = environment
	}
}

func parseSystemUpdateList(output string) Result {
	if strings.Contains(output, "* Label:") {
		return result("available", "apple_catalog_updates")
	}
	if strings.Contains(output, "No new software available") {
		return result("current", "apple_catalog_no_updates")
	}
	return result("unknown", "unrecognized_system_response")
}
