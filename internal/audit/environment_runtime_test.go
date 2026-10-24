package audit

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustedRuntimeExactPythonLayouts(t *testing.T) {
	for _, c := range []struct {
		formula, keg, relative, tap string
		trusted                     bool
	}{
		{"python@3.14", "3.14.3_1", "Frameworks/Python.framework/Versions/3.14/bin/python3.14", "homebrew/core", true},
		{"python@3.14", "3.14.3_1", "bin/python3.14", "homebrew/core", true},
		{"python@3.14", "3.14.3_1", "bin/python3", "homebrew/core", true},
		{"python", "3.14.3_1", "bin/python", "homebrew/core", true},
		{"python@3.14", "3.14.3_1", "Frameworks/Python.framework/Versions/3.12/bin/python3.12", "homebrew/core", false},
		{"python@3.12", "3.14.3_1", "Frameworks/Python.framework/Versions/3.14/bin/python3.14", "homebrew/core", false},
		{"python@3.14", "3.14.3_1", "Frameworks/Python.framework/Versions/3.14/bin/python3.12", "homebrew/core", false},
		{"python@3.14", "3.14.3_1", "Frameworks/Python.framework/Versions/3.14/bin/python3.14-config", "homebrew/core", false},
		{"python@3.14", "3.14.3_1", "Frameworks/Other.framework/Versions/3.14/bin/python3.14", "homebrew/core", false},
		{"python@3.14", "3.14.3_1", "bin/nested/python3.14", "homebrew/core", false},
		{"python@3.14", "3.14.3_1", "lib/private/bin/python3.14", "homebrew/core", false},
		{"python@3.14", "3.14.3_1", "bin/python3.12", "homebrew/core", false},
		{"python@3.14-preview", "3.14.3_1", "bin/python3.14", "homebrew/core", false},
		{"python@3.14", "3.14.3-preview", "bin/python3.14", "homebrew/core", false},
		{"python@3.14", "3.14.3_1", "bin/python3.14", "vendor/tools", false},
	} {
		t.Run(c.formula+"/"+c.relative+"/"+c.tap, func(t *testing.T) {
			s := fixture(t)
			keg := filepath.Join(s.Config.BrewRoots[0], "Cellar", c.formula, c.keg)
			path := filepath.Join(keg, c.relative)
			write(t, path, "synthetic metadata only; never execute")
			write(t, filepath.Join(keg, "INSTALL_RECEIPT.json"), `{"source":{"tap":"`+c.tap+`"}}`)
			if trusted := s.trustedRuntime(real(path), "python3"); trusted != c.trusted {
				t.Fatal(trusted, c)
			}
		})
	}
}

// Minimal Mach-O metadata supports static architecture discovery; this fixture is never executed.
func syntheticMachO(t *testing.T, path string) {
	t.Helper()
	b := make([]byte, 32)
	binary.LittleEndian.PutUint32(b[0:4], 0xfeedfacf)
	binary.LittleEndian.PutUint32(b[4:8], 0x0100000c)
	binary.LittleEndian.PutUint32(b[12:16], 2)
	write(t, path, string(b))
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentRuntimeReusesSameTargetEvidence(t *testing.T) {
	for _, status := range []string{"ok", "error", "unknown"} {
		t.Run(status, func(t *testing.T) {
			s := fixture(t)
			keg := filepath.Join(s.Config.BrewRoots[0], "Cellar/python@3.14/3.14.3_1")
			path := filepath.Join(keg, "Frameworks/Python.framework/Versions/3.14/bin/python3.14")
			syntheticMachO(t, path)
			write(t, filepath.Join(keg, "INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"}}`)
			for _, name := range []string{"first", "second"} {
				env := filepath.Join(s.Config.Home, name)
				write(t, filepath.Join(env, "pyvenv.cfg"), "version = 3.13.0\ninclude-system-site-packages = false\n")
				link(t, path, filepath.Join(env, "bin/python"))
				s.pythonEnv(env, "uv")
			}
			previous := item("executable", "python3", path, "PATH")
			previous.Health = result(status, "runtime_probe_failed")
			previous.Evidence = []string{"runtime probe attempted"}
			if status == "ok" {
				previous.Version, previous.Health = "3.14.3", result("ok", "runtime_version_verified")
				previous.Evidence = append(previous.Evidence, "runtime version probe")
			}
			s.add(previous)
			s.environmentRuntimes()
			if len(s.Report.Queries) != 0 {
				t.Fatal("synthetic binary was executed", s.Report.Queries)
			}
			if len(s.Report.Items) != 3 {
				t.Fatal("duplicate target added", s.Report.Items)
			}
			for _, env := range s.Report.Items {
				if env.Kind != "python-environment" {
					continue
				}
				if env.Health.Status != status {
					t.Fatal(env)
				}
				want := ""
				if status == "ok" {
					want = "3.14.3"
				}
				if env.Version != want || len(env.Related) != 1 || env.Related[0] != previous.ID {
					t.Fatal(env)
				}
				if !strings.Contains(strings.Join(env.Evidence, "|"), "creation-version:3.13.0") {
					t.Fatal("creation evidence lost", env)
				}
			}
		})
	}
}

func TestEnvironmentRuntimeNeverStartsUntrustedCode(t *testing.T) {
	s := fixture(t)
	env := filepath.Join(s.Config.Home, "environment")
	write(t, filepath.Join(env, "pyvenv.cfg"), "version = 3.14.0\n")
	write(t, filepath.Join(env, "bin/python"), "#!/bin/sh\ntouch SHOULD_NOT_EXIST\n")
	if err := os.Chmod(filepath.Join(env, "bin/python"), 0755); err != nil {
		t.Fatal(err)
	}
	s.pythonEnv(env, "uv")
	s.environmentRuntimes()
	if len(s.Report.Queries) != 0 {
		t.Fatal(s.Report.Queries)
	}
	if len(s.Report.Items) != 1 || s.Report.Items[0].Version != "" || s.Report.Items[0].Health.Code != "interpreter_version_unverified" {
		t.Fatal(s.Report.Items)
	}
}

func TestRuntimeVersionOutputMustMatchRuntimeFamily(t *testing.T) {
	for _, c := range []struct{ name, raw, version string }{
		{"python3", "Python 3.14.3\n", "3.14.3"},
		{"python", "error 3.14.3", ""},
		{"node", "v24.22.0\n", "24.22.0"},
		{"node", "warning 24.22.0", ""},
		{"go", "go version go1.25.2 darwin/arm64\n", "1.25.2"},
		{"bun", "1.3.0\n", "1.3.0"},
	} {
		if got := parseRuntimeVersion(c.name, c.raw); got != c.version {
			t.Fatal(c, got)
		}
	}
}

// Explicit opt-in: reads core Homebrew Python installations and runs only -I -S --version.
func TestLiveEnvironmentRuntimeProbe(t *testing.T) {
	if os.Getenv("KEEPUPDATED_LIVE_RUNTIME_TEST") != "1" {
		t.Skip("live read-only runtime probe not requested")
	}
	for _, path := range []string{"/opt/homebrew/bin/python3", "/opt/homebrew/opt/python@3.12/bin/python3.12"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if real(path) == "" {
				t.Skip("runtime not installed")
			}
			s := fixture(t)
			s.Config.BrewRoots = []string{"/opt/homebrew"}
			for _, name := range []string{"first", "second"} {
				env := filepath.Join(s.Config.Home, name)
				write(t, filepath.Join(env, "pyvenv.cfg"), "version = 3.0.0\ninclude-system-site-packages = false\n")
				link(t, path, filepath.Join(env, "bin/python"))
				minor := strings.TrimPrefix(filepath.Base(real(path)), "python")
				site := filepath.Join(env, "lib/python"+minor+"/site-packages")
				write(t, filepath.Join(site, "example.dist-info/METADATA"), "Name: example\nVersion: 1.0\nRequires-Python: >=3.0\nRequires-Dist: child>=1\nRequires-Dist: absent; python_version < '3.0'\n\n")
				write(t, filepath.Join(site, "child.dist-info/METADATA"), "Name: child\nVersion: 1.2\n\n")
				s.pythonEnv(env, "uv")
			}
			for _, name := range []string{"python3", "python"} {
				link(t, path, filepath.Join(s.Config.Path, name))
			}
			s.executables()
			s.environmentRuntimes()
			s.dependencies()
			if len(s.Report.Queries) != 1 || !strings.HasSuffix(s.Report.Queries[0], " -I -S --version") {
				t.Fatal("expected one isolated probe", s.Report.Queries)
			}
			for _, env := range s.Report.Items {
				if env.Kind == "python-environment" && (env.Version == "" || env.Version == "3.0.0" || env.Health.Status != "ok") {
					t.Fatal("actual version not verified", env)
				}
				if env.Kind == "python-environment" {
					t.Logf("verified actual Python %s", env.Version)
				}
				if env.Kind == "python-package" && env.Name == "example" {
					if len(env.Dependencies) != 3 || env.Dependencies[0].Code != "marker_not_applicable" || env.Dependencies[1].Code != "declared_constraint_satisfied" || env.Dependencies[2].Code != "declared_constraint_satisfied" {
						t.Fatal("interpreter evidence not applied to dependencies", env.Dependencies)
					}
				}
			}
		})
	}
}
