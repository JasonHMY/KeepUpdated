package main

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestLinkedModuleNoticesPreserveOriginalsAndExcludeUnused(t *testing.T) {
	root := t.TempDir()
	for name, text := range map[string]string{"LICENSE": "original license\n", "NOTICE": "original notice\n", "pkg/LICENSE.txt": "subpackage license\n", "unused/LICENSE": "unused component\n"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, path, text)
	}
	module := &struct {
		Path, Version, Dir string
		Main               bool
		Replace            *struct{}
	}{Path: "example.invalid/library", Version: "v1.2.3", Dir: root}
	packages := []listedPackage{{ImportPath: module.Path + "/pkg", Dir: filepath.Join(root, "pkg"), Module: module}}
	modules := []*debug.Module{{Path: module.Path, Version: module.Version, Sum: "h1:fixture"}}
	files, err := collectModuleNotices(modules, packages, buildInfo{Commit: "fixture", GoVersion: "go1.27.2", Target: "darwin/arm64"})
	if err != nil {
		t.Fatal(err)
	}
	var manifest moduleManifest
	seen := map[string][]byte{}
	for _, file := range files {
		seen[file.name] = file.data
		if strings.Contains(string(file.data), root) {
			t.Fatal("private module cache path included")
		}
	}
	if err = json.Unmarshal(seen["LICENSES/modules.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Modules) != 1 || len(manifest.Modules[0].Files) != 3 {
		t.Fatalf("missing module notices: %+v", manifest)
	}
	if !bytes.Contains(seen["LICENSES/ModuleNotices.txt"], []byte("original notice\n")) || bytes.Contains(seen["LICENSES/ModuleNotices.txt"], []byte("unused component")) {
		t.Fatal("wrong original notice selection")
	}
	if string(seen["LICENSES/modules/"+module.Path+"@v1.2.3/LICENSE"]) != "original license\n" {
		t.Fatal("license text changed")
	}
	if err = os.Remove(filepath.Join(root, "LICENSE")); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(root, "pkg/LICENSE.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err = collectModuleNotices(modules, packages, buildInfo{}); err == nil {
		t.Fatal("missing linked license allowed")
	}
}

func TestLinkedModuleNoticesRejectReplacementAndUnsafeSources(t *testing.T) {
	mod := &debug.Module{Path: "example.invalid/library", Version: "v1.0.0", Replace: &debug.Module{Path: "local"}}
	if _, err := collectModuleNotices([]*debug.Module{mod}, nil, buildInfo{}); err == nil {
		t.Fatal("replacement accepted")
	}
	root := t.TempDir()
	if err := os.Symlink("/nonexistent", filepath.Join(root, "LICENSE")); err != nil {
		t.Fatal(err)
	}
	pkg := listedPackage{Dir: root, Module: &struct {
		Path, Version, Dir string
		Main               bool
		Replace            *struct{}
	}{Path: mod.Path, Version: mod.Version, Dir: root}}
	mod.Replace = nil
	if _, err := collectModuleNotices([]*debug.Module{mod}, []listedPackage{pkg}, buildInfo{}); err == nil {
		t.Fatal("symlink notice accepted")
	}
}

func TestPackageVerifierBindsLinkedModulesAndNoticeHashes(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	linked, err := buildinfo.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{}
	for _, setting := range linked.Settings {
		settings[setting.Key] = setting.Value
	}
	for _, fault := range []string{"none", "notice", "missing", "module-version", "source", "toolchain"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			info := buildInfo{Version: "0.1.0", Commit: "fixture", GoVersion: linked.GoVersion, Target: settings["GOOS"] + "/" + settings["GOARCH"]}
			manifest := moduleManifest{Schema: "keepupdated/module-notices/v1", Commit: info.Commit, GoVersion: info.GoVersion, Target: info.Target, Modules: []moduleNotice{}}
			for _, mod := range linked.Deps {
				file := "LICENSES/modules/" + mod.Path + "@" + mod.Version + "/LICENSE"
				hash := sha256.Sum256([]byte("fixture notice"))
				manifest.Modules = append(manifest.Modules, moduleNotice{Path: mod.Path, Version: mod.Version, Sum: mod.Sum, Files: []noticeHash{{Path: file, SHA256: hex.EncodeToString(hash[:])}}})
				path := filepath.Join(dir, file)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, path, "fixture notice")
			}
			for _, name := range []string{"LICENSE", "LICENSES/Go.txt", "THIRD_PARTY_NOTICES.txt", "LICENSES/ModuleNotices.txt", "skills/keepupdated/SKILL.md", "skills/keepupdated/scripts/run.sh"} {
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, path, "fixture")
			}
			if err := os.WriteFile(filepath.Join(dir, "keepupdated"), binary, 0755); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "notice", "missing":
				if len(manifest.Modules) == 0 {
					t.Skip("test executable has no module dependencies")
				}
				path := filepath.Join(dir, manifest.Modules[0].Files[0].Path)
				if fault == "notice" {
					mustWrite(t, path, "changed")
				} else if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "module-version":
				manifest.Modules = append(manifest.Modules, moduleNotice{Path: "unused.invalid/tool", Version: "v9.9.9"})
			case "source":
				manifest.Commit = "another-source"
			case "toolchain":
				manifest.GoVersion = "another-toolchain"
			}
			for name, value := range map[string]any{"BUILDINFO.json": info, "LICENSES/modules.json": manifest} {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
					t.Fatal(err)
				}
			}
			err := verifyPackage(dir)
			if fault == "none" && err != nil {
				t.Fatal(err)
			}
			if fault != "none" && err == nil {
				t.Fatalf("accepted %s", fault)
			}
		})
	}
}
