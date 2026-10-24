package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNumericDependencyConstraints(t *testing.T) {
	for _, c := range []struct {
		v, s         string
		match, known bool
	}{{"3.14.0", ">=3.10,<4", true, true}, {"3.9.0", ">=3.10", false, true}, {"1.4.8", "~=1.4.5", true, true}, {"1.5.0", "~=1.4.5", false, true}, {"1.5.0", "~=1.4", true, true}, {"2.4.0", "==2.*", true, true}, {"2.4.0", "!=2.4.*", false, true}, {"1.0rc1", ">=1", false, false}, {"1.0", "@ https://secret.invalid", false, false}, {"1.0", "", true, true}} {
		m, k := pythonConstraint(c.v, c.s)
		if m != c.match || k != c.known {
			t.Fatal(c, m, k)
		}
	}
	for _, c := range []struct {
		v, s         string
		match, known bool
	}{{"24.22.0", ">= 20 <25", true, true}, {"24.22.0", "^24.0.0", true, true}, {"25.0.0", "^24.0.0", false, true}, {"0.2.8", "^0.2.1", true, true}, {"0.3.0", "^0.2.1", false, true}, {"1.2.9", "~1.2.3", true, true}, {"1.3.0", "~1.2.3", false, true}, {"24.22.0", "20.x || 24.x", true, true}, {"24.22.0", "git+https://example.invalid", false, false}, {"1.2.0-beta", "*", false, false}} {
		m, k := nodeConstraint(c.v, c.s)
		if m != c.match || k != c.known {
			t.Fatal(c, m, k)
		}
	}
}
func TestPythonMarkers(t *testing.T) {
	env := map[string]string{"python_version": "3.14", "sys_platform": "darwin", "os_name": "posix"}
	for _, c := range []struct {
		s            string
		match, known bool
	}{{`python_version >= '3.10' and sys_platform == 'darwin'`, true, true}, {`sys_platform == 'win32'`, false, true}, {`(python_version < '3.10' or sys_platform == 'darwin') and os_name == 'posix'`, true, true}, {`extra == 'test'`, false, false}, {`sys_platform == 'win32' and extra == 'x'`, false, true}, {`'3.10' < python_version`, true, true}, {`sys_platform not in 'win32,linux'`, true, true}, {`sys_platform == 'darwin' or`, false, false}} {
		m, k := evaluateMarker(c.s, env)
		if m != c.match || k != c.known {
			t.Fatal(c, m, k)
		}
	}
}
func TestNodeDependenciesResolveNestedOptionalAndRuntime(t *testing.T) {
	s := fixture(t)
	root := filepath.Join(s.Config.Home, "lib/node_modules/example")
	write(t, filepath.Join(root, "package.json"), `{"name":"example","version":"1.0.0","bin":"cli.js","dependencies":{"child":"^2.0.0","missing":"1"},"optionalDependencies":{"optional":"*"},"engines":{"node":">=24"}}`)
	write(t, filepath.Join(root, "cli.js"), "#!/usr/bin/env node\n")
	if err := os.Chmod(filepath.Join(root, "cli.js"), 0755); err != nil {
		t.Fatal(err)
	}
	link(t, filepath.Join(root, "cli.js"), filepath.Join(s.Config.Path, "example"))
	write(t, filepath.Join(root, "node_modules/child/package.json"), `{"name":"child","version":"1.0.0"}`)
	s.nodePackage(root, "npm")
	node := item("executable", "node", "fixture-node", "PATH")
	node.Active = true
	node.ActiveVersion = "24.2.0"
	node.Health = result("ok", "entry_resolved")
	s.add(node)
	s.dependencies()
	i := s.Report.Items[0]
	codes := map[string]string{}
	for _, d := range i.Dependencies {
		codes[d.Name] = d.Code
	}
	if codes["child"] != "declared_constraint_unsatisfied" || codes["missing"] != "dependency_missing" || codes["optional"] != "optional_dependency_absent" || codes["runtime:node"] != "PATH_runtime_constraint_satisfied" {
		t.Fatal(i)
	}
}
func TestPythonDependenciesStayInEnvironment(t *testing.T) {
	s := fixture(t)
	root := filepath.Join(s.Config.Home, "env")
	site := filepath.Join(root, "lib/python3.14/site-packages")
	write(t, filepath.Join(root, "pyvenv.cfg"), "version = 3.14.0\ninclude-system-site-packages = false\n")
	write(t, filepath.Join(root, "bin/python"), "fixture")
	write(t, filepath.Join(site, "example-1.0.dist-info/METADATA"), "Name: example\nVersion: 1.0\nRequires-Python: >=3.10\nRequires-Dist: child>=2\nRequires-Dist: missing; sys_platform == 'win32'\nRequires-Dist: optional; extra == 'test'\n\n")
	write(t, filepath.Join(site, "child-1.0.dist-info/METADATA"), "Name: child\nVersion: 1.0\n\n")
	s.pythonEnv(root, "uv")
	runtime := item("executable", "python3", filepath.Join(root, "bin/python"), "PATH")
	runtime.Version, runtime.Health, runtime.Evidence = "3.14.0", result("ok", "entry_resolved"), []string{"runtime version probe"}
	s.add(runtime)
	s.dependencies()
	for _, i := range s.Report.Items {
		if i.Name != "example" {
			continue
		}
		codes := map[string]string{}
		for _, d := range i.Dependencies {
			codes[d.Name] = d.Code
		}
		if codes["child"] != "declared_constraint_unsatisfied" || codes["missing"] != "marker_not_applicable" || codes["optional"] != "marker_not_supported" {
			t.Fatal(i)
		}
	}
}
func TestHomebrewDependencyArchitectureAndMissing(t *testing.T) {
	s := fixture(t)
	root := s.Config.BrewRoots[0]
	write(t, filepath.Join(root, "Cellar/example/1.0/INSTALL_RECEIPT.json"), `{"arch":"arm64","source":{"tap":"homebrew/core"},"runtime_dependencies":[{"full_name":"missing"},{"full_name":"child","version":"999.0"}]}`)
	write(t, filepath.Join(root, "Cellar/child/2.0/INSTALL_RECEIPT.json"), `{"arch":"x86_64","source":{"tap":"homebrew/core"}}`)
	link(t, "../Cellar/example/1.0", filepath.Join(root, "opt/example"))
	link(t, "../Cellar/child/2.0", filepath.Join(root, "opt/child"))
	s.brew()
	s.dependencies()
	for _, i := range s.Report.Items {
		if i.Name != "example" {
			continue
		}
		if len(i.Dependencies) != 2 || i.Health.Code != "dependency_issue" {
			t.Fatal(i)
		}
		for _, d := range i.Dependencies {
			if d.Name == "child" && d.Code != "dependency_architecture_mismatch" {
				t.Fatal(d)
			}
		}
	}
}
func TestDependencyURLsDoNotEscape(t *testing.T) {
	s := fixture(t)
	p := filepath.Join(s.Config.Home, "example")
	write(t, filepath.Join(p, "package.json"), `{"name":"example","dependencies":{"child":"git+https://user:SECRET@example.invalid/repo"}}`)
	write(t, filepath.Join(p, "node_modules/child/package.json"), `{"name":"child","version":"1.0.0"}`)
	s.nodePackage(p, "npm")
	s.dependencies()
	b, _ := json.Marshal(s.Report)
	if strings.Contains(string(b), "SECRET") {
		t.Fatal("credential leaked")
	}
}

func TestPythonMissingIsUnknownForOpenSearchPath(t *testing.T) {
	s := fixture(t)
	site := filepath.Join(s.Config.Home, "lib/python3.14/site-packages")
	write(t, filepath.Join(site, "example-1.0.dist-info/METADATA"), "Name: example\nVersion: 1.0\nRequires-Dist: absent>=1\n\n")
	s.pythonDists(site, "global-python", filepath.Dir(site))
	s.dependencies()
	if s.Report.Items[0].Dependencies[0].Code != "dependency_not_found_in_scanned_paths" {
		t.Fatal(s.Report.Items)
	}
	env := filepath.Join(s.Config.Home, "venv")
	write(t, filepath.Join(env, "pyvenv.cfg"), "include-system-site-packages = false\n")
	if pythonSearchClosed(env, site) {
		t.Fatal("site outside environment treated as isolated")
	}
	site = filepath.Join(env, "lib/python3.14/site-packages")
	write(t, filepath.Join(site, "example.dist-info/METADATA"), "Name: example\n")
	if !pythonSearchClosed(env, site) {
		t.Fatal("isolated environment not recognized")
	}
	write(t, filepath.Join(site, "custom.pth"), "/some/shared/site-packages\n")
	if pythonSearchClosed(env, site) {
		t.Fatal("path hook ignored")
	}
}

func TestCaskRuntimeDependencies(t *testing.T) {
	s := fixture(t)
	root := s.Config.BrewRoots[0]
	write(t, filepath.Join(root, "Caskroom/example/1.0/bin/tool"), "fixture")
	write(t, filepath.Join(root, "Caskroom/example/.metadata/INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/cask"},"runtime_dependencies":{"formula":[{"full_name":"missing-python"}]}}`)
	s.brew()
	s.dependencies()
	i := s.Report.Items[0]
	if len(i.Dependencies) != 1 || i.Dependencies[0].Code != "dependency_missing" {
		t.Fatal(i)
	}
}

func TestUnknownConstraintsCannotHideBehindMatchingAlternatives(t *testing.T) {
	for _, spec := range []string{"* || arbitrary", "arbitrary || *", ">=1 || 2.0.0-beta", "^01.0.0", "1.x.3"} {
		if _, known := nodeConstraint("1.2.3", spec); known {
			t.Fatalf("unsupported constraint reported known: %q", spec)
		}
	}
	for _, c := range []struct {
		version, spec string
		match         bool
	}{
		{"1.9.0", "<=1", true}, {"2.0.0", "<=1", false},
		{"1.9.0", ">1", false}, {"2.0.0", ">1", true},
		{"0.0.9", "^0.0", true}, {"0.1.0", "^0.0", false},
		{"0.9.0", "^0", true}, {"1.0.0", "^0", false},
	} {
		if match, known := nodeConstraint(c.version, c.spec); !known || match != c.match {
			t.Fatalf("%+v: match=%v known=%v", c, match, known)
		}
	}
	for _, version := range []string{"1", "1.0", "01.00.3"} {
		if match, known := pythonConstraint(version, "==1.0.*"); !known || !match {
			t.Fatalf("PEP 440 prefix normalization: %s %v %v", version, match, known)
		}
	}
	if _, known := pythonConstraint("18446744073709551616", ""); known {
		t.Fatal("unrepresentable installed version reported known")
	}
}

func TestInvalidMarkerCannotHideBehindBooleanShortCircuit(t *testing.T) {
	env := map[string]string{"sys_platform": "darwin"}
	for _, raw := range []string{
		`sys_platform == 'darwin' or undocumented == 'x'`,
		`sys_platform == 'darwin' or sys_platform invalid 'x'`,
		`sys_platform == 'darwin' or sys_platform not 'x'`,
		`sys_platform == 'darwin' or (sys_platform == 'x'`,
	} {
		if _, known := evaluateMarker(raw, env); known {
			t.Fatalf("invalid marker reported known: %s", raw)
		}
	}
}

func TestNodePeersKeepIndependentConstraintsAndOptionality(t *testing.T) {
	s := fixture(t)
	root := filepath.Join(s.Config.Home, "lib/node_modules/example")
	write(t, filepath.Join(root, "package.json"), `{"name":"example","version":"1.0.0","dependencies":{"child":"^1","required":"*"},"peerDependencies":{"child":"^2","required":"*","absent":"*"},"peerDependenciesMeta":{"required":{"optional":true},"absent":{"optional":true}}}`)
	write(t, filepath.Join(root, "node_modules/child/package.json"), `{"name":"child","version":"1.2.3"}`)
	write(t, filepath.Join(filepath.Dir(root), "child/package.json"), `{"name":"child","version":"1.2.3"}`)
	s.nodePackage(root, "npm")
	s.dependencies()
	got := map[string]Dependency{}
	for _, d := range s.Report.Items[0].Dependencies {
		got[d.Relationship+":"+d.Name] = d
	}
	for key, code := range map[string]string{
		"dependency:child":       "declared_constraint_satisfied",
		"peer:child":             "declared_constraint_unsatisfied",
		"dependency:required":    "dependency_missing",
		"optional-peer:required": "optional_dependency_absent",
		"optional-peer:absent":   "optional_dependency_absent",
	} {
		if got[key].Code != code {
			t.Fatalf("%s: %+v", key, got)
		}
	}
}

func TestNodeRuntimeRequiresObservedEntryBinding(t *testing.T) {
	for _, c := range []struct {
		owner, shebang string
		active         bool
		code           string
	}{
		{"npm", "#!/usr/bin/env node\n", true, "PATH_runtime_constraint_satisfied"},
		{"npm", "#!/usr/bin/env node\n", false, "runtime_binding_unverified"},
		{"bun", "#!/usr/bin/env node\n", true, "runtime_binding_unverified"},
		{"npm", "#!/opt/private/node\n", true, "runtime_binding_unverified"},
	} {
		t.Run(c.owner, func(t *testing.T) {
			s := fixture(t)
			root := filepath.Join(s.Config.Home, "example")
			write(t, filepath.Join(root, "package.json"), `{"name":"example","version":"1.0.0","bin":"cli.js","engines":{"node":">=20"}}`)
			write(t, filepath.Join(root, "cli.js"), c.shebang)
			if err := os.Chmod(filepath.Join(root, "cli.js"), 0755); err != nil {
				t.Fatal(err)
			}
			if c.active {
				link(t, filepath.Join(root, "cli.js"), filepath.Join(s.Config.Path, "example"))
			}
			s.nodePackage(root, c.owner)
			node := item("executable", "node", filepath.Join(s.Config.Home, "node"), "PATH")
			node.Active, node.ActiveVersion, node.Health = true, "24.0.0", result("ok", "entry_resolved")
			s.add(node)
			s.dependencies()
			if got := s.Report.Items[0].Dependencies[0]; got.Code != c.code {
				t.Fatal(got)
			}
		})
	}
}

func TestPythonRuntimeRequiresMatchingProbe(t *testing.T) {
	s := fixture(t)
	root := filepath.Join(s.Config.Home, "venv")
	site := filepath.Join(root, "lib/python3.14/site-packages")
	write(t, filepath.Join(root, "pyvenv.cfg"), "version = 3.14.0\ninclude-system-site-packages = false\n")
	write(t, filepath.Join(root, "bin/python"), "fixture")
	write(t, filepath.Join(site, "example.dist-info/METADATA"), "Name: example\nVersion: 1.0\nRequires-Python: >=3.14\nRequires-Dist: absent; python_version >= '3.14'\n\n")
	s.pythonEnv(root, "uv")
	s.dependencies()
	for _, i := range s.Report.Items {
		if i.Kind == "python-package" {
			if i.Dependencies[0].Code != "marker_not_supported" || i.Dependencies[1].Code != "runtime_version_unverified" {
				t.Fatal(i.Dependencies)
			}
		}
	}
	r := item("executable", "python3", filepath.Join(root, "bin/python"), "PATH")
	r.Version, r.Health, r.Evidence = "3.13.9", result("ok", "entry_resolved"), []string{"runtime version probe"}
	s.add(r)
	s.dependencies()
	for _, i := range s.Report.Items {
		if i.Kind == "python-package" {
			if i.Dependencies[0].Code != "marker_not_applicable" || i.Dependencies[1].Code != "declared_constraint_unsatisfied" {
				t.Fatal(i.Dependencies)
			}
		}
	}
}

func TestPythonSearchHooksKeepInstalledResolutionUnknown(t *testing.T) {
	s := fixture(t)
	root := filepath.Join(s.Config.Home, "venv")
	site := filepath.Join(root, "lib/python3.14/site-packages")
	write(t, filepath.Join(root, "pyvenv.cfg"), "include-system-site-packages = false\n")
	write(t, filepath.Join(site, "example.dist-info/METADATA"), "Name: example\nVersion: 1.0\nRequires-Dist: child>=1\n\n")
	write(t, filepath.Join(site, "child.dist-info/METADATA"), "Name: child\nVersion: 2.0\n\n")
	write(t, filepath.Join(site, "hook.pth"), "import code_that_must_not_execute\n")
	s.pythonEnv(root, "uv")
	s.dependencies()
	for _, i := range s.Report.Items {
		if i.Name == "example" && i.Dependencies[0].Code != "python_search_path_not_verified" {
			t.Fatal(i)
		}
	}
	if err := os.Remove(filepath.Join(site, "hook.pth")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PYTHONPATH", filepath.Join(s.Config.Home, "another-site"))
	if pythonSearchClosed(root, site) {
		t.Fatal("PYTHONPATH ignored")
	}
}

func TestBrewDependencyChecksSourceAndExactKeg(t *testing.T) {
	s := fixture(t)
	root := s.Config.BrewRoots[0]
	write(t, filepath.Join(root, "Cellar/example/1.0/INSTALL_RECEIPT.json"), `{"arch":"arm64","source":{"tap":"homebrew/core"},"runtime_dependencies":[{"full_name":"child"},{"full_name":"vendor/tools/child"},{"full_name":"nested"}]}`)
	write(t, filepath.Join(root, "Cellar/child/2.0/INSTALL_RECEIPT.json"), `{"arch":"arm64","source":{"tap":"vendor/tools"}}`)
	write(t, filepath.Join(root, "Cellar/nested/1.0/subdir/INSTALL_RECEIPT.json"), `{"arch":"arm64","source":{"tap":"homebrew/core"}}`)
	link(t, "../Cellar/example/1.0", filepath.Join(root, "opt/example"))
	link(t, "../Cellar/child/2.0", filepath.Join(root, "opt/child"))
	link(t, "../Cellar/nested/1.0/subdir", filepath.Join(root, "opt/nested"))
	s.brew()
	s.dependencies()
	for _, i := range s.Report.Items {
		if i.Name == "example" {
			got := map[string]string{}
			for _, d := range i.Dependencies {
				got[d.Name] = d.Code
			}
			if got["child"] != "dependency_source_mismatch" || got["vendor/tools/child"] != "dependency_active_keg_present" || got["nested"] != "dependency_active_link_unresolved" {
				t.Fatal(got)
			}
		}
	}
}

func TestMissingDependencyDeclarationsStayUnknown(t *testing.T) {
	s := fixture(t)
	root := s.Config.BrewRoots[0]
	write(t, filepath.Join(root, "Cellar/example/1.0/INSTALL_RECEIPT.json"), `{"arch":"arm64","source":{"tap":"homebrew/core"}}`)
	link(t, "../Cellar/example/1.0", filepath.Join(root, "opt/example"))
	s.brew()
	s.dependencies()
	if got := s.Report.Items[0].Dependencies; len(got) != 1 || got[0].Code != "dependency_declarations_unavailable" {
		t.Fatal(got)
	}
}

func TestUnreadableDependencyIsNotHealthy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires ordinary-user permission semantics")
	}
	s := fixture(t)
	root := s.Config.BrewRoots[0]
	write(t, filepath.Join(root, "Cellar/example/1.0/INSTALL_RECEIPT.json"), `{"arch":"arm64","source":{"tap":"homebrew/core"},"runtime_dependencies":[{"full_name":"child"}]}`)
	child := filepath.Join(root, "Cellar/child/2.0")
	write(t, filepath.Join(child, "INSTALL_RECEIPT.json"), `{"arch":"arm64","source":{"tap":"homebrew/core"}}`)
	link(t, "../Cellar/example/1.0", filepath.Join(root, "opt/example"))
	link(t, "../Cellar/child/2.0", filepath.Join(root, "opt/child"))
	if err := os.Chmod(child, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(child, 0755) })
	i := item("formula", "example", filepath.Join(root, "Cellar/example"), "homebrew")
	i.ActiveVersion = "1.0"
	s.brewDependencies(&i)
	if len(i.Dependencies) != 1 || i.Dependencies[0].Status != "unknown" || i.Dependencies[0].Code != "dependency_unreadable" {
		t.Fatal(i.Dependencies)
	}
}

func TestNodeNonDirectoryCannotFallThroughToAncestor(t *testing.T) {
	s := fixture(t)
	root := filepath.Join(s.Config.Home, "lib/node_modules/example")
	write(t, filepath.Join(root, "node_modules/child"), "not package metadata")
	write(t, filepath.Join(filepath.Dir(root), "child/package.json"), `{"name":"child","version":"1.0.0"}`)
	if path, code := resolveNodePackage(root, "child"); path != "" || code != "dependency_not_package_directory" {
		t.Fatal(path, code)
	}
}
