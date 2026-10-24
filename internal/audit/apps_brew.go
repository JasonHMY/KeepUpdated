package audit

import (
	"debug/macho"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func architectures(path string) []string {
	names := func(c macho.Cpu) string {
		switch c {
		case macho.CpuArm64:
			return "arm64"
		case macho.CpuAmd64:
			return "x86_64"
		default:
			return c.String()
		}
	}
	reader, err := openRegular(path)
	if err != nil {
		return nil
	}
	defer reader.Close()
	if f, e := macho.NewFatFile(reader); e == nil {
		a := []string{}
		for _, x := range f.Arches {
			a = append(a, names(x.Cpu))
		}
		return uniq(a)
	}
	if f, e := macho.NewFile(reader); e == nil {
		return []string{names(f.Cpu)}
	}
	return nil
}
func (s *Scanner) apps() {
	visiting := map[string]bool{}
	discovered := map[string]bool{}
	var applications []Item
	var walk func(string, string)
	walk = func(root, parent string) {
		if s.Config.Context != nil && s.Config.Context.Err() != nil {
			return
		}
		resolved := real(root)
		if resolved == "" {
			if _, e := os.Lstat(root); e == nil {
				s.diag("apps", root, "broken_link")
			} else if !os.IsNotExist(e) {
				s.diag("apps", root, "unreadable_root")
			}
			return
		}
		if visiting[resolved] {
			return
		}
		visiting[resolved] = true
		defer delete(visiting, resolved)
		err := filepath.WalkDir(resolved, func(p string, d fs.DirEntry, e error) error {
			if s.Config.Context != nil && s.Config.Context.Err() != nil {
				return fs.SkipAll
			}
			if d != nil && d.IsDir() && strings.HasPrefix(d.Name(), ".KeepUpdated-native-") {
				return filepath.SkipDir
			}
			rel, _ := filepath.Rel(resolved, p)
			p = filepath.Join(root, rel)
			if e != nil {
				s.diag("apps", p, "unreadable_path")
				return nil
			}
			if strings.HasSuffix(strings.ToLower(p), ".app") && (d.IsDir() || d.Type()&os.ModeSymlink != 0) {
				i := item("application", strings.TrimSuffix(filepath.Base(p), ".app"), p, "standalone")
				if parent != "" {
					i.Owner = "app-component"
					i.Related = []string{parent}
				}
				if !discovered[i.ID] {
					discovered[i.ID] = true
					applications = append(applications, i)
				}
				// Inspect embedded bundles as components, retaining their parent relationship.
				for _, sub := range []string{"Contents/Applications", "Contents/Helpers", "Contents/Frameworks", "Contents/Library"} {
					q := filepath.Join(p, sub)
					if exists(q) {
						walk(q, i.ID)
					}
				}
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				target := real(p)
				if target == "" {
					s.diag("apps", p, "broken_or_inaccessible_link")
				} else if st, e := os.Stat(p); e == nil && st.IsDir() {
					walk(p, parent)
				}
			}
			return nil
		})
		if err != nil {
			s.diag("apps", root, "walk_failed")
		}
	}
	for _, p := range s.Config.AppRoots {
		walk(p, "")
	}
	s.inspectApplications(applications, (*Scanner).applicationMetadata)
}

// Each worker owns its query log; merge by discovery order so concurrency cannot
// change inventory identities, diagnostics or command ordering in the report.
func (s *Scanner) inspectApplications(applications []Item, inspect func(*Scanner, Item) Item) {
	type inspected struct {
		item        Item
		queries     []string
		diagnostics []Diagnostic
		done        bool
	}
	results := make([]inspected, len(applications))
	total := len(applications)
	progress := NewPhaseProgress(s.Config.Context, s.Config.Progress, "applications", &total)
	defer progress.Finish()
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if s.cancelled() {
					continue
				}
				local := *s
				local.Report = Report{Architecture: s.Report.Architecture}
				progress.Begin(applications[index].Name)
				if s.cancelled() {
					continue
				}
				i := inspect(&local, applications[index])
				results[index] = inspected{i, local.Report.Queries, local.Report.Diagnostics, true}
				progress.Done(i.Health.Status != "ok")
			}
		}()
	}
	for index := range applications {
		if s.cancelled() {
			break
		}
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	for _, result := range results {
		if !result.done {
			continue
		}
		s.add(result.item)
		s.Report.Queries = append(s.Report.Queries, result.queries...)
		s.Report.Diagnostics = append(s.Report.Diagnostics, result.diagnostics...)
	}
}

func (s *Scanner) applicationMetadata(i Item) Item {
	p := i.Path
	b, e := s.run("/usr/bin/plutil", "-convert", "json", "-o", "-", filepath.Join(p, "Contents/Info.plist"))
	var m map[string]any
	if e != nil || json.Unmarshal(b, &m) != nil {
		i.Health = result("error", "bundle_metadata_unreadable")
	} else {
		i.Identity = str(m["CFBundleIdentifier"])
		i.Build = str(m["CFBundleVersion"])
		i.Version = str(m["CFBundleShortVersionString"])
		if i.Version == "" {
			i.Version = str(m["CFBundleVersion"])
		}
		i.Evidence = append(i.Evidence, "Contents/Info.plist")
		exe := str(m["CFBundleExecutable"])
		if exe != "" && !strings.Contains(exe, "/") {
			ep := filepath.Join(p, "Contents/MacOS", exe)
			i.Architecture = architectures(ep)
			i.Active = s.running[ep]
			i.ActivityKnown = s.processKnown
			if i.Active {
				i.ActiveVersion = i.Version
			}
		}
		i.Health = result("ok", "bundle_metadata_readable")
		if str(m["SUFeedURL"]) != "" {
			i.Source = "sparkle"
			i.lookup = str(m["SUFeedURL"])
			i.Channel = "default"
			i.Update = result("unknown", "online_query_required")
		}
		if exists(filepath.Join(p, "Contents/_MASReceipt/receipt")) {
			i.Source = "app-store"
			i.Owner = "app-store"
			i.lookup = i.Identity
			i.Update = result("unknown", "mac_store_not_queried")
		}
	}
	s.vendorSource(&i)
	s.electronSource(&i)
	return i
}
func str(v any) string { s, _ := v.(string); return s }
func stringList(v any) []string {
	a := []string{}
	switch x := v.(type) {
	case string:
		if x != "" {
			a = append(a, x)
		}
	case []any:
		for _, v := range x {
			if s := str(v); s != "" {
				a = append(a, s)
			}
		}
	}
	return a
}
func (s *Scanner) brew() {
	for _, root := range s.Config.BrewRoots {
		for _, rack := range s.entries("homebrew", filepath.Join(root, "Cellar")) {
			if !rack.IsDir() {
				continue
			}
			p := filepath.Join(root, "Cellar", rack.Name())
			i := item("formula", rack.Name(), p, "homebrew")
			i.Channel = releaseChannel(i.Name)
			opt := real(filepath.Join(root, "opt", rack.Name()))
			linked := real(filepath.Join(root, "var/homebrew/linked", rack.Name()))
			activeReceiptRead := false
			for _, keg := range s.entries("homebrew", p) {
				if !keg.IsDir() {
					continue
				}
				v := keg.Name()
				i.Versions = append(i.Versions, v)
				kp := filepath.Join(p, v)
				if opt == real(kp) {
					i.ActiveVersion = v
					i.Version = v
					i.Evidence = append(i.Evidence, "opt symlink")
				}
				if linked == real(kp) {
					i.Active = true
					i.ActivityKnown = true
					i.Evidence = append(i.Evidence, "linked keg")
				}
				var receipt struct {
					Source struct {
						Tap string `json:"tap"`
					} `json:"source"`
					Arch    string `json:"arch"`
					Runtime []struct {
						FullName string `json:"full_name"`
					} `json:"runtime_dependencies"`
				}
				b, e := readSmall(filepath.Join(kp, "INSTALL_RECEIPT.json"))
				if e == nil && json.Unmarshal(b, &receipt) == nil {
					if opt == real(kp) {
						activeReceiptRead = true
					}
					if opt == real(kp) || i.Source == "unknown" {
						i.Source = receipt.Source.Tap
						i.Architecture = []string{receipt.Arch}
					}
					for _, d := range receipt.Runtime {
						i.Evidence = append(i.Evidence, "dependency:"+d.FullName)
					}
				} else {
					s.diag("homebrew", kp, "receipt_unreadable")
				}
			}
			i.Versions = uniq(i.Versions)
			i.Evidence = uniq(i.Evidence)
			if i.ActiveVersion == "" {
				i.Health = result("unknown", "active_keg_unresolved")
				if len(i.Versions) == 1 {
					i.Version = i.Versions[0]
				}
			} else {
				i.Health = result("ok", "active_keg_resolved")
				if !activeReceiptRead {
					i.Source = "unknown"
					i.Architecture = nil
					i.Health = result("error", "active_receipt_unreadable")
				}
			}
			if i.Source == "homebrew/core" {
				i.lookup = rack.Name()
				i.Update = result("unknown", "online_query_required")
			} else {
				i.Update = result("unsupported", "custom_tap_update_unimplemented")
			}
			s.add(i)
		}
		for _, c := range s.entries("homebrew", filepath.Join(root, "Caskroom")) {
			if !c.IsDir() {
				continue
			}
			p := filepath.Join(root, "Caskroom", c.Name())
			i := item("cask", c.Name(), p, "homebrew")
			i.Channel = releaseChannel(i.Name)
			for _, v := range s.entries("homebrew", p) {
				if v.IsDir() && !strings.HasPrefix(v.Name(), ".") {
					i.Versions = append(i.Versions, v.Name())
				}
			}
			i.Versions = uniq(i.Versions)
			if len(i.Versions) == 1 {
				i.Version = i.Versions[0]
			}
			var receipt struct {
				Source struct {
					Tap     string `json:"tap"`
					Version string `json:"version"`
				} `json:"source"`
				Arch      string           `json:"arch"`
				Artifacts []map[string]any `json:"uninstall_artifacts"`
			}
			if b, err := readSmall(filepath.Join(p, ".metadata/INSTALL_RECEIPT.json")); err == nil && json.Unmarshal(b, &receipt) == nil {
				i.Source = receipt.Source.Tap
				if receipt.Arch != "" {
					i.Architecture = []string{receipt.Arch}
				}
				i.Evidence = append(i.Evidence, "Caskroom INSTALL_RECEIPT.json")
				for _, artifact := range receipt.Artifacts {
					for _, name := range stringList(artifact["app"]) {
						if filepath.Base(name) == name {
							i.Evidence = append(i.Evidence, "app-artifact:"+name)
						}
					}
				}
			}
			paths := glob(filepath.Join(p, ".metadata", "*", "*", "Casks", "*.json"))
			for _, q := range paths {
				var m map[string]any
				b, e := readSmall(q)
				if e != nil || json.Unmarshal(b, &m) != nil {
					continue
				}
				if str(m["token"]) != c.Name() {
					continue
				}
				v := str(m["version"])
				if i.Version != "" && v != i.Version {
					continue
				}
				i.Source = str(m["tap"])
				i.Evidence = append(i.Evidence, "Caskroom JSON metadata")
				if a, ok := m["artifacts"].([]any); ok {
					for _, a := range a {
						if am, ok := a.(map[string]any); ok {
							for _, ap := range stringList(am["app"]) {
								if filepath.Base(ap) == ap {
									i.Evidence = append(i.Evidence, "app-artifact:"+ap)
								}
							}
						}
					}
				}
			}
			if i.Source == "homebrew/cask" {
				i.lookup = c.Name()
				i.Update = result("unknown", "online_query_required")
			} else {
				i.Update = result("unsupported", "cask_source_unresolved")
			}
			if i.Version == "" {
				i.Health = result("unknown", "multiple_cask_versions")
			} else {
				i.Health = result("ok", "cask_installation_found")
			}
			s.add(i)
		}
		for _, tap := range glob(filepath.Join(root, "Library/Taps", "*", "*")) {
			if !exists(filepath.Join(tap, ".git")) {
				continue
			}
			i := item("tap", filepath.Base(filepath.Dir(tap))+"/"+filepath.Base(tap), tap, "homebrew")
			i.Source = "git"
			s.gitState(&i)
			i.Update = result("unsupported", "tap_remote_not_fetched")
			s.add(i)
		}
	}
}
func (s *Scanner) relate() {
	for a := range s.Report.Items {
		app := &s.Report.Items[a]
		if app.Kind != "application" || app.Owner == "app-component" {
			continue
		}
		for _, c := range s.Report.Items {
			if c.Kind != "cask" {
				continue
			}
			for _, v := range c.Evidence {
				if v != "app-artifact:"+filepath.Base(app.Path) {
					continue
				}
				// Only a resolved Caskroom artifact link establishes ownership, never the display name.
				for _, version := range c.Versions {
					p := filepath.Join(c.Path, version, filepath.Base(app.Path))
					if real(p) != "" && real(p) == app.RealPath {
						app.Owner = "homebrew"
						app.Related = append(app.Related, c.ID)
						app.Evidence = append(app.Evidence, "Caskroom artifact resolves to bundle")
						if app.Update.Code == "no_update_source" {
							app.Update = result("unsupported", "use_installation_owner")
						}
					}
				}
			}
		}
	}
}

func releaseChannel(name string) string {
	for _, channel := range []string{"beta", "nightly", "dev", "canary", "preview", "alpha"} {
		if strings.HasSuffix(name, "@"+channel) || strings.HasSuffix(name, "-"+channel) {
			return channel
		}
	}
	return "stable"
}
