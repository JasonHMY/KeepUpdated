package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func voltaFixture(t *testing.T, s *Scanner, root, name, manager string) string {
	t.Helper()
	write(t, filepath.Join(root, "layout.v4"), "")
	write(t, filepath.Join(root, "tools/user/platform.json"), `{"node":{"runtime":"22.1.0","npm":null},"pnpm":null,"yarn":null}`)
	marker := filepath.Join(s.Config.Home, "unexpected-execution")
	script := "#!/bin/sh\ntouch '" + marker + "'\nexit 1\n"
	for _, path := range []string{"tools/image/node/20.1.0/bin/node", "tools/image/node/22.1.0/bin/node", "bin/node", "bin/npm", "bin/codex", "hooks/run", "bin/volta"} {
		p := filepath.Join(root, path)
		write(t, p, script)
		if err := os.Chmod(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(root, "hooks.json"), `{"node":{"index":{"bin":"`+filepath.Join(root, "hooks/run")+`"}}}`)
	suffix := "lib/node_modules"
	if manager == "Pnpm" {
		suffix = "5/node_modules"
	}
	p := filepath.Join(root, "tools/image/packages", name, suffix, name)
	write(t, filepath.Join(p, "package.json"), `{"name":"`+name+`","version":"1.0.0","bin":{"codex":"cli.js"},"engines":{"node":">=20.0.0"},"_resolved":"https://registry.npmjs.org/`+name+`/-/`+filepath.Base(name)+`-1.0.0.tgz"}`)
	write(t, filepath.Join(p, "cli.js"), script)
	platform := map[string]any{"node": "20.1.0", "npm": nil, "pnpm": nil, "yarn": nil}
	record := map[string]any{"name": name, "version": "1.0.0", "platform": platform, "bins": []string{"codex"}, "manager": manager}
	b, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "tools/user/packages", name+".json"), string(b))
	b, err = json.Marshal(map[string]any{"name": "codex", "package": name, "version": "1.0.0", "platform": platform, "manager": manager})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "tools/user/bins/codex.json"), string(b))
	return p
}

func voltaItem(t *testing.T, r Report, path string) Item {
	t.Helper()
	for _, i := range r.Items {
		if i.Path == path {
			return i
		}
	}
	t.Fatalf("item absent: %s", path)
	return Item{}
}

func TestVoltaStoredPlatformsAndConsumersDoNotRunLaunchers(t *testing.T) {
	for _, check := range []bool{false, true} {
		t.Run(map[bool]string{false: "scan", true: "check"}[check], func(t *testing.T) {
			s := fixture(t)
			root := filepath.Join(s.Config.Home, ".volta")
			pkg := voltaFixture(t, s, root, "@scope/tool", "Npm")
			s.Config.Path = filepath.Join(root, "bin")
			project := filepath.Join(s.Config.Home, "project")
			s.Config.Projects = []string{project}
			write(t, filepath.Join(project, "package.json"), `{"name":"project","volta":{"node":"20.1.0"}}`)
			write(t, filepath.Join(s.Config.Home, "unselected-project/package.json"), `{"volta":{"node":"22.1.0"}}`)
			bundled := filepath.Join(root, "tools/image/node/22.1.0/lib/node_modules/npm")
			write(t, filepath.Join(bundled, "package.json"), `{"name":"npm","version":"10.0.0"}`)
			r := s.Scan(check)
			installed := voltaItem(t, r, pkg)
			if installed.Owner != "volta" || installed.Source != "volta" || installed.NodeSelector != nil || installed.ActivityKnown || installed.Active || installed.Update.Code != "volta_owner_manual" {
				t.Fatalf("inner package escaped owner: %+v", installed)
			}
			selected := voltaItem(t, r, filepath.Join(root, "tools/image/node/22.1.0"))
			retained := voltaItem(t, r, filepath.Join(root, "tools/image/node/20.1.0"))
			if !selected.Active || !selected.ActivityKnown || selected.ActiveVersion != "22.1.0" || retained.Active || !retained.ActivityKnown || len(selected.Versions) != 2 {
				t.Fatalf("bad selection: %+v %+v", selected, retained)
			}
			if !slices.Contains(installed.Related, retained.ID) || !slices.Contains(retained.Related, installed.ID) {
				t.Fatal("package consumer missing", installed, retained)
			}
			pin := voltaItem(t, r, filepath.Join(project, "package.json"))
			if pin.Kind != "runtime-pin" || pin.ActivityKnown || !slices.Contains(pin.Related, retained.ID) {
				t.Fatal("bad project pin", pin)
			}
			if voltaItem(t, r, bundled).Owner != "volta" {
				t.Fatal("bundled npm escaped owner")
			}
			for _, i := range r.Items {
				if strings.Contains(i.Path, "unselected-project") {
					t.Fatal("unrequested project scanned")
				}
				if i.Kind == "executable" && i.Owner == "volta" && (i.Active || i.ActivityKnown || i.ActiveVersion != "") {
					t.Fatal("shim claimed active platform", i)
				}
			}
			if check {
				if installed.Health.Status != "unknown" {
					t.Fatal("stored platform treated as runtime probe", installed)
				}
				found := false
				for _, dep := range installed.Dependencies {
					if dep.Name == "runtime:node" {
						found = dep.Installed == "20.1.0" && dep.Status == "unknown" && dep.Code == "volta_runtime_not_probed"
					}
				}
				if !found {
					t.Fatal("stored runtime binding lost", installed.Dependencies)
				}
			}
			if _, err := os.Stat(filepath.Join(s.Config.Home, "unexpected-execution")); !os.IsNotExist(err) {
				t.Fatal("launcher/hook executed", err)
			}
			for _, q := range r.Queries {
				if strings.Contains(q, "volta") || strings.HasPrefix(q, "node ") || strings.HasPrefix(q, "npm ") || strings.HasPrefix(q, "codex ") {
					t.Fatal("Volta command attempted", q)
				}
			}
		})
	}
}

func TestVoltaMissingOrAmbiguousRecordsRemainUnknown(t *testing.T) {
	for _, scenario := range []string{"missing-layout", "missing-package-record", "unknown-manager", "package-version-mismatch", "unknown-platform-field", "duplicate-platform-field", "broken-shim", "ambiguous-bin-record", "missing-package-json", "missing-default-platform", "unknown-layout", "external-record", "project-extends"} {
		t.Run(scenario, func(t *testing.T) {
			s := fixture(t)
			root := filepath.Join(s.Config.Home, ".volta")
			p := voltaFixture(t, s, root, "tool", "Npm")
			s.Config.Path = filepath.Join(root, "bin")
			remove := func(path string) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, path)); err != nil {
					t.Fatal(err)
				}
			}
			record := filepath.Join(root, "tools/user/packages/tool.json")
			switch scenario {
			case "missing-layout":
				remove("layout.v4")
			case "unknown-layout":
				remove("layout.v4")
				write(t, filepath.Join(root, "layout.v5"), "")
			case "missing-package-record":
				remove("tools/user/packages/tool.json")
			case "unknown-manager", "package-version-mismatch", "unknown-platform-field", "duplicate-platform-field":
				b, err := os.ReadFile(record)
				if err != nil {
					t.Fatal(err)
				}
				data := string(b)
				switch scenario {
				case "unknown-manager":
					data = strings.Replace(data, `"Npm"`, `"Future"`, 1)
				case "package-version-mismatch":
					data = strings.Replace(data, `"1.0.0"`, `"2.0.0"`, 1)
				case "unknown-platform-field":
					data = strings.Replace(data, `"node":"20.1.0"`, `"node":"20.1.0","future":true`, 1)
				case "duplicate-platform-field":
					data = strings.Replace(data, `"node":"20.1.0"`, `"node":"22.1.0","node":"20.1.0"`, 1)
				}
				write(t, record, data)
			case "broken-shim":
				remove("bin/codex")
				link(t, "absent", filepath.Join(root, "bin/codex"))
			case "ambiguous-bin-record":
				write(t, filepath.Join(root, "tools/user/bins/codex.json"), `{"name":"codex","package":"other","version":"1.0.0","platform":{"node":"20.1.0"},"manager":"Npm"}`)
			case "missing-package-json":
				if err := os.Remove(filepath.Join(p, "package.json")); err != nil {
					t.Fatal(err)
				}
			case "missing-default-platform":
				remove("tools/user/platform.json")
			case "external-record":
				b, _ := os.ReadFile(record)
				other := filepath.Join(s.Config.Home, "outside.json")
				write(t, other, string(b))
				remove("tools/user/packages/tool.json")
				link(t, other, record)
			case "project-extends":
				project := filepath.Join(s.Config.Home, "project")
				s.Config.Projects = []string{project}
				write(t, filepath.Join(project, "package.json"), `{"volta":{"node":"20.1.0","extends":"../external.json"}}`)
			}
			s.packages()
			s.executables()
			i := voltaItem(t, s.Report, p)
			if i.Owner != "volta" || i.Update.Code != "volta_owner_manual" || i.Active || i.ActivityKnown || i.lookup != "" {
				t.Fatal("unsafe metadata fallback", i)
			}
			if scenario != "missing-default-platform" && scenario != "project-extends" && i.Health.Code == "volta_platform_recorded" {
				t.Fatal("invalid record accepted", i)
			}
			if scenario == "missing-default-platform" || scenario == "missing-layout" || scenario == "unknown-layout" {
				for _, i := range s.Report.Items {
					if i.Kind == "runtime" && (i.ActivityKnown || i.Active) {
						t.Fatal("unknown default claimed selected", i)
					}
				}
			}
			if scenario == "project-extends" {
				i := voltaItem(t, s.Report, filepath.Join(s.Config.Projects[0], "package.json"))
				if i.Health.Code != "volta_project_pin_unverified" || len(i.Related) > 0 {
					t.Fatal(i)
				}
			}
		})
	}
}

func TestVoltaCustomRootsKeepSameNamedPackagesIndependent(t *testing.T) {
	s := fixture(t)
	root := filepath.Join(s.Config.Home, ".volta")
	custom := filepath.Join(s.Config.Home, "custom-volta")
	a := voltaFixture(t, s, root, "tool", "Yarn")
	b := voltaFixture(t, s, custom, "tool", "Pnpm")
	s.Config.ManagerRoots = map[string]string{"volta": custom}
	s.Config.Path = filepath.Join(custom, "bin")
	project := filepath.Join(s.Config.Home, "project")
	s.Config.Projects = []string{project}
	write(t, filepath.Join(project, "package.json"), `{"volta":{"node":"20.1.0"}}`)
	s.packages()
	one, two := voltaItem(t, s.Report, a), voltaItem(t, s.Report, b)
	if one.ID == two.ID || one.Owner != "volta" || two.Owner != "volta" || one.Health.Code != "volta_platform_recorded" || two.Health.Code != "volta_platform_recorded" {
		t.Fatal(one, two)
	}
	ra := voltaItem(t, s.Report, filepath.Join(root, "tools/image/node/20.1.0"))
	rb := voltaItem(t, s.Report, filepath.Join(custom, "tools/image/node/20.1.0"))
	if !slices.Contains(one.Related, ra.ID) || slices.Contains(one.Related, rb.ID) || !slices.Contains(two.Related, rb.ID) || slices.Contains(two.Related, ra.ID) {
		t.Fatal("cross-root binding")
	}
	pin := voltaItem(t, s.Report, filepath.Join(project, "package.json"))
	if !slices.Contains(pin.Related, rb.ID) || slices.Contains(pin.Related, ra.ID) {
		t.Fatal("project root binding ambiguous", pin)
	}
}

func TestVoltaPathDiscoveryAndInnerAliasesStayManual(t *testing.T) {
	s := fixture(t)
	root := filepath.Join(s.Config.Home, "custom-volta")
	p := voltaFixture(t, s, root, "tool", "Npm")
	s.Config.Path = filepath.Join(root, "bin")
	alias := filepath.Join(s.Config.Home, ".npm-global/lib/node_modules/tool")
	link(t, p, alias)
	s.packages()
	if voltaItem(t, s.Report, p).Owner != "volta" || voltaItem(t, s.Report, alias).Owner != "volta" {
		t.Fatal("alias escaped outer owner")
	}
	if VoltaRoot(filepath.Join(s.Config.Home, "ordinary/lib/node_modules/tool"), s.Config.Home, nil) != "" {
		t.Fatal("ordinary npm classified Volta")
	}
}

func TestVoltaPartialProjectPinAndScopeBoundary(t *testing.T) {
	s := fixture(t)
	root := filepath.Join(s.Config.Home, ".volta")
	voltaFixture(t, s, root, "tool", "Npm")
	write(t, filepath.Join(root, "tools/image/yarn/1.22.22/bin/yarn"), "unused")
	project := filepath.Join(s.Config.Home, "project")
	outside := filepath.Join(s.Config.Home, "outside/package.json")
	write(t, filepath.Join(project, "package.json"), `{"volta":{"yarn":"1.22.22"}}`)
	write(t, outside, `{"volta":{"node":"20.1.0"}}`)
	link(t, outside, filepath.Join(s.Config.Home, "linked-project/package.json"))
	s.Config.Projects = []string{project, filepath.Join(s.Config.Home, "linked-project")}
	s.packages()
	pin := voltaItem(t, s.Report, filepath.Join(project, "package.json"))
	yarn := voltaItem(t, s.Report, filepath.Join(root, "tools/image/yarn/1.22.22"))
	if pin.Health.Code != "volta_project_pin_recorded" || !slices.Contains(pin.Related, yarn.ID) {
		t.Fatal(pin)
	}
	for _, i := range s.Report.Items {
		if strings.Contains(i.Path, "linked-project") {
			t.Fatal("project escaped selected scope", i)
		}
	}
}

func TestVoltaEmptyAndDuplicateDefaultsDoNotSelectRuntime(t *testing.T) {
	for _, data := range []string{`{"node":{"runtime":"22.1.0","npm":""}}`, `{"node":{"runtime":"20.1.0","runtime":"22.1.0"}}`, `{"node":{"runtime":"22.1.0"},"future":true}`} {
		s := fixture(t)
		root := filepath.Join(s.Config.Home, ".volta")
		voltaFixture(t, s, root, "tool", "Npm")
		write(t, filepath.Join(root, "tools/user/platform.json"), data)
		s.packages()
		i := voltaItem(t, s.Report, filepath.Join(root, "tools/image/node/22.1.0"))
		if i.ActivityKnown || i.Active {
			t.Fatal("invalid selection accepted", i)
		}
	}
}

func TestVoltaExternalImageAndManifestKeepUnknownConsumers(t *testing.T) {
	for _, scenario := range []string{"image", "manifest", "runtime"} {
		t.Run(scenario, func(t *testing.T) {
			s := fixture(t)
			root := filepath.Join(s.Config.Home, ".volta")
			p := voltaFixture(t, s, root, "tool", "Npm")
			source := filepath.Join(root, "tools/image/packages/tool")
			if scenario == "manifest" {
				source = filepath.Join(p, "package.json")
			}
			if scenario == "runtime" {
				source = filepath.Join(root, "tools/image/node")
			}
			outside := filepath.Join(s.Config.Home, "outside")
			if err := os.Rename(source, outside); err != nil {
				t.Fatal(err)
			}
			link(t, outside, source)
			s.packages()
			i := voltaItem(t, s.Report, p)
			if i.Owner != "volta" || i.Update.Code != "volta_owner_manual" || len(i.Related) != 0 {
				t.Fatal("external contents claimed a known consumer", i)
			}
			if scenario != "runtime" && i.Health.Code != "volta_metadata_unverified" {
				t.Fatal(i)
			}
		})
	}
}

func TestVoltaExplicitNestedRootTakesPrecedence(t *testing.T) {
	s := fixture(t)
	root := filepath.Join(s.Config.Home, ".volta", "custom")
	p := voltaFixture(t, s, root, "tool", "Npm")
	s.Config.ManagerRoots = map[string]string{"volta": root}
	s.packages()
	i := voltaItem(t, s.Report, p)
	if i.Health.Code != "volta_platform_recorded" || !slices.Contains(i.Evidence, "volta-root:"+root) {
		t.Fatal("explicit root lost to conventional ancestor", i)
	}
}
