package audit

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestGlobalPythonConfiguredRoots(t *testing.T) {
	for _, name := range []string{"custom", "repeated", "nil", "empty"} {
		t.Run(name, func(t *testing.T) {
			s := fixture(t)
			root := s.Config.BrewRoots[0]
			custom := filepath.Join(root, "lib/python3.14/site-packages/example-1.0.dist-info")
			user := filepath.Join(s.Config.Home, "Library/Python/3.14/lib/python/site-packages/user-2.0.dist-info")
			other := filepath.Join(s.Config.Home, "other-brew/lib/python3.14/site-packages/other-3.0.dist-info")
			for path, metadata := range map[string]string{
				custom: "Name: example\nVersion: 1.0\n",
				user:   "Name: user\nVersion: 2.0\n",
				other:  "Name: other\nVersion: 3.0\n",
			} {
				write(t, filepath.Join(path, "METADATA"), metadata)
			}
			want := map[string]string{user: "2.0"}
			switch name {
			case "custom":
				want[custom] = "1.0"
			case "repeated":
				s.Config.BrewRoots = []string{root, root, root + "/."}
				want[custom] = "1.0"
			case "nil":
				s.Config.BrewRoots = nil
			case "empty":
				s.Config.BrewRoots = []string{}
			}
			s.packages()
			s.finish()
			if len(s.Report.Items) != len(want) {
				t.Fatalf("got %d items, want %d: %+v", len(s.Report.Items), len(want), s.Report.Items)
			}
			for _, i := range s.Report.Items {
				version, ok := want[i.Path]
				if !ok || i.Kind != "python-package" || i.Owner != "global-python" || i.Version != version || i.ID != item("python-package", "", i.Path, "").ID {
					t.Fatalf("unexpected discovery: %+v", i)
				}
			}
			if len(s.Report.Queries) != 0 {
				t.Fatalf("discovery executed commands: %v", s.Report.Queries)
			}
		})
	}
}

func TestGlobalPythonDefaultRoots(t *testing.T) {
	want := []string{"/opt/homebrew", "/usr/local"}
	if got := New(DefaultConfig()).Config.BrewRoots; !reflect.DeepEqual(got, want) {
		t.Fatalf("default roots = %v, want %v", got, want)
	}
}

func TestGlobalPythonRootIsLiteralPath(t *testing.T) {
	for _, name := range []string{"brew[ab]", "brew*", "brew?", `brew\root`} {
		t.Run(name, func(t *testing.T) {
			s := fixture(t)
			root := filepath.Join(s.Config.Home, name)
			relative := "lib/python3.14/site-packages/example-1.0.dist-info"
			path := filepath.Join(root, relative)
			write(t, filepath.Join(path, "METADATA"), "Name: example\nVersion: 1.0\n")
			for _, other := range []string{"brewa", "brewb", "brewroot"} {
				write(t, filepath.Join(s.Config.Home, other, relative, "METADATA"), "Name: other\nVersion: 1.0\n")
			}
			s.Config.BrewRoots = []string{root}
			s.packages()
			if len(s.Report.Items) != 1 || s.Report.Items[0].Path != path {
				t.Fatalf("configured literal root was not respected: %+v", s.Report.Items)
			}
		})
	}
}

func TestGlobalPythonOverlappingUserRoot(t *testing.T) {
	s := fixture(t)
	root := filepath.Join(s.Config.Home, "Library/Python/3.14")
	path := filepath.Join(root, "lib/python/site-packages/example-1.0.dist-info")
	write(t, filepath.Join(path, "METADATA"), "Name: example\nVersion: 1.0\n")
	s.Config.BrewRoots = []string{root, root}
	s.packages()
	if len(s.Report.Items) != 1 || s.Report.Items[0].ID != item("python-package", "", path, "").ID {
		t.Fatalf("overlapping roots changed discovery identity: %+v", s.Report.Items)
	}
}

func TestGlobalPythonAliasesRetainDiscoveryIDs(t *testing.T) {
	s := fixture(t)
	root := s.Config.BrewRoots[0]
	alias := filepath.Join(s.Config.Home, "brew-alias")
	relative := "lib/python3.14/site-packages/example-1.0.dist-info"
	path := filepath.Join(root, relative)
	write(t, filepath.Join(path, "METADATA"), "Name: example\nVersion: 1.0\n")
	link(t, root, alias)
	want := map[string]string{
		path:                           item("python-package", "", path, "").ID,
		filepath.Join(alias, relative): item("python-package", "", filepath.Join(alias, relative), "").ID,
	}
	for _, roots := range [][]string{{root, alias, root}, {alias, root, alias}} {
		config := s.Config
		config.BrewRoots = roots
		scan := New(config)
		scan.packages()
		if len(scan.Report.Items) != 2 {
			t.Fatalf("expected two distinct discovery paths: %+v", scan.Report.Items)
		}
		for _, i := range scan.Report.Items {
			if i.ID != want[i.Path] || i.RealPath != real(path) {
				t.Fatalf("alias changed path identity: %+v", i)
			}
		}
	}
}
