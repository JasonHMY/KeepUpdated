package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
)

type moduleNotice struct {
	Path    string       `json:"path"`
	Version string       `json:"version"`
	Sum     string       `json:"sum"`
	Files   []noticeHash `json:"files"`
}
type noticeHash struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type moduleManifest struct {
	Schema    string         `json:"schema"`
	Commit    string         `json:"commit"`
	GoVersion string         `json:"go_version"`
	Target    string         `json:"target"`
	Modules   []moduleNotice `json:"modules"`
}
type listedPackage struct {
	ImportPath string
	Dir        string
	Module     *struct {
		Path, Version, Dir string
		Main               bool
		Replace            *struct{}
	}
}

// Build metadata identifies linked modules; go list supplies only their source
// directories. Neither module-cache paths nor unlinked build tools are shipped.
func moduleNotices(goPath, binary string, env []string, info buildInfo) ([]archiveFile, error) {
	linked, err := buildinfo.ReadFile(binary)
	if err != nil {
		return nil, err
	}
	deps := exec.Command(goPath, "list", "-deps", "-mod=readonly", "-json", "./cmd/keepupdated")
	deps.Env = env
	data, err := deps.Output()
	if err != nil {
		return nil, errors.New("could not enumerate linked module source notices")
	}
	var packages []listedPackage
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	for {
		var p listedPackage
		if err := decoder.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		packages = append(packages, p)
	}
	return collectModuleNotices(linked.Deps, packages, info)
}

func collectModuleNotices(modules []*debug.Module, packages []listedPackage, info buildInfo) ([]archiveFile, error) {
	manifest := moduleManifest{Schema: "keepupdated/module-notices/v1", Commit: info.Commit, GoVersion: info.GoVersion, Target: info.Target, Modules: []moduleNotice{}}
	var files []archiveFile
	var notices strings.Builder
	for _, mod := range modules {
		if mod.Replace != nil {
			return nil, errors.New("release modules must not use replacements")
		}
		record := moduleNotice{Path: mod.Path, Version: mod.Version, Sum: mod.Sum, Files: []noticeHash{}}
		selected := map[string]string{}
		foundLicense := false
		for _, p := range packages {
			if p.Module == nil || p.Module.Main || p.Module.Path != mod.Path || p.Module.Version != mod.Version {
				continue
			}
			if p.Module.Replace != nil {
				return nil, errors.New("release modules must not use replacements")
			}
			root := filepath.Clean(p.Module.Dir)
			dir := filepath.Clean(p.Dir)
			for {
				rel, err := filepath.Rel(root, dir)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return nil, errors.New("module source escapes its root")
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					return nil, err
				}
				for _, entry := range entries {
					name := entry.Name()
					upper := strings.ToUpper(name)
					kind := strings.SplitN(strings.SplitN(upper, ".", 2)[0], "-", 2)[0]
					if kind != "LICENSE" && kind != "LICENCE" && kind != "NOTICE" && kind != "COPYING" && kind != "PATENTS" {
						continue
					}
					source := filepath.Join(dir, name)
					st, err := os.Lstat(source)
					if err != nil || !st.Mode().IsRegular() || st.Size() > 4<<20 {
						return nil, errors.New("module notice must be a bounded regular file")
					}
					relative, err := filepath.Rel(root, source)
					if err != nil {
						return nil, err
					}
					selected[filepath.ToSlash(relative)] = source
					foundLicense = foundLicense || kind == "LICENSE" || kind == "LICENCE" || kind == "COPYING"
				}
				if dir == root {
					break
				}
				dir = filepath.Dir(dir)
			}
		}
		if !foundLicense {
			return nil, fmt.Errorf("missing original license for linked module %s@%s", mod.Path, mod.Version)
		}
		var paths []string
		for path := range selected {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, relative := range paths {
			text, err := os.ReadFile(selected[relative])
			if err != nil {
				return nil, err
			}
			name := "LICENSES/modules/" + mod.Path + "@" + mod.Version + "/" + relative
			files = append(files, archiveFile{name, text, 0644})
			hash := sha256.Sum256(text)
			record.Files = append(record.Files, noticeHash{name, hex.EncodeToString(hash[:])})
			fmt.Fprintf(&notices, "\n===== %s@%s/%s =====\n", mod.Path, mod.Version, relative)
			notices.Write(text)
			if len(text) == 0 || text[len(text)-1] != '\n' {
				notices.WriteByte('\n')
			}
		}
		manifest.Modules = append(manifest.Modules, record)
	}
	sort.Slice(manifest.Modules, func(i, j int) bool { return manifest.Modules[i].Path < manifest.Modules[j].Path })
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	files = append(files, archiveFile{"LICENSES/modules.json", append(encoded, '\n'), 0644})
	files = append(files, archiveFile{"LICENSES/ModuleNotices.txt", []byte(notices.String()), 0644})
	return files, nil
}

func verifyPackage(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "BUILDINFO.json"))
	if err != nil {
		return err
	}
	var info buildInfo
	if err = json.Unmarshal(data, &info); err != nil {
		return err
	}
	data, err = os.ReadFile(filepath.Join(dir, "LICENSES/modules.json"))
	if err != nil {
		return err
	}
	var manifest moduleManifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	if manifest.Schema != "keepupdated/module-notices/v1" || manifest.Commit != info.Commit || manifest.GoVersion != info.GoVersion || manifest.Target != info.Target {
		return errors.New("module notices do not match build metadata")
	}
	linked, err := buildinfo.ReadFile(filepath.Join(dir, "keepupdated"))
	if err != nil {
		return err
	}
	if linked.GoVersion != info.GoVersion {
		return errors.New("packaged executable toolchain does not match notices")
	}
	target := map[string]string{}
	for _, setting := range linked.Settings {
		target[setting.Key] = setting.Value
	}
	if target["GOOS"]+"/"+target["GOARCH"] != info.Target {
		return errors.New("packaged executable target does not match notices")
	}
	expected := map[string]string{}
	for _, mod := range linked.Deps {
		if mod.Replace != nil {
			return errors.New("packaged module replacement is unsupported")
		}
		expected[mod.Path] = mod.Version + "\n" + mod.Sum
	}
	actual := map[string]string{}
	for _, mod := range manifest.Modules {
		if _, ok := actual[mod.Path]; ok {
			return errors.New("duplicate module notice")
		}
		actual[mod.Path] = mod.Version + "\n" + mod.Sum
		if len(mod.Files) == 0 {
			return errors.New("module notice files missing")
		}
		for _, file := range mod.Files {
			prefix := "LICENSES/modules/" + mod.Path + "@" + mod.Version + "/"
			if !strings.HasPrefix(file.Path, prefix) || filepath.ToSlash(filepath.Clean(file.Path)) != file.Path || strings.Contains(file.Path, "../") {
				return errors.New("unsafe notice path")
			}
			st, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(file.Path)))
			if err != nil || !st.Mode().IsRegular() || st.Size() > 4<<20 {
				return errors.New("packaged notice is missing or unsafe")
			}
			text, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file.Path)))
			if err != nil {
				return err
			}
			hash := sha256.Sum256(text)
			if hex.EncodeToString(hash[:]) != file.SHA256 {
				return errors.New("packaged module notice hash mismatch")
			}
		}
	}
	if !reflect.DeepEqual(expected, actual) {
		return errors.New("packaged module notices do not match linked modules")
	}
	for _, file := range []string{"LICENSE", "LICENSES/Go.txt", "THIRD_PARTY_NOTICES.txt", "LICENSES/ModuleNotices.txt", "skills/keepupdated/SKILL.md", "skills/keepupdated/scripts/run.sh"} {
		if st, err := os.Stat(filepath.Join(dir, file)); err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("required package input missing: %s", file)
		}
	}
	return nil
}
