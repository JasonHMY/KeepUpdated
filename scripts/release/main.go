// Command release builds and packages the macOS Apple Silicon release.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type buildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Target    string `json:"target"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func run() error {
	version := flag.String("version", "0.1.2", "release version without v prefix")
	out := flag.String("out", "dist/product-v0.1.2", "output directory")
	verify := flag.String("verify-package", "", "verify notices against an extracted package executable and build metadata")
	allowDirty := flag.Bool("allow-dirty", false, "build a local candidate with +dirty commit metadata")
	flag.Parse()
	if *verify != "" {
		if flag.NArg() != 0 {
			return errors.New("verify-package takes one directory")
		}
		return verifyPackage(*verify)
	}
	if flag.NArg() != 0 || *version != "0.1.2" {
		return errors.New("this source and paired Skill require version 0.1.2")
	}
	if _, err := os.Stat("cmd/keepupdated/main.go"); err != nil {
		return errors.New("run from the repository root")
	}
	commit, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	date, err := git("show", "-s", "--format=%cI", "HEAD")
	if err != nil {
		return err
	}
	timestamp, err := time.Parse(time.RFC3339, date)
	if err != nil {
		return fmt.Errorf("invalid commit time: %w", err)
	}
	status, err := git("status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return err
	}
	if status != "" {
		if !*allowDirty {
			return errors.New("working tree is dirty; commit the reviewed source or use --allow-dirty for a local candidate")
		}
		commit += "+dirty"
	}
	sourceCommit := strings.TrimSuffix(commit, "+dirty")
	name := "keepupdated_" + *version + "_darwin_arm64.tar.gz"
	if err := outputsAbsent(*out, name); err != nil {
		return err
	}
	info := buildInfo{*version, commit, timestamp.UTC().Format(time.RFC3339), runtime.Version(), "darwin/arm64"}
	staging, err := os.MkdirTemp("", "keepupdated-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	goPath := filepath.Join(runtime.GOROOT(), "bin", "go")
	flags := fmt.Sprintf("-s -w -X main.version=%s -X main.commit=%s -X main.buildDate=%s", info.Version, info.Commit, info.BuildDate)
	cmd := exec.Command(goPath, "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags", flags, "-o", filepath.Join(staging, "keepupdated"), "./cmd/keepupdated")
	cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=arm64", "GOARM64=v8.0", "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOFLAGS=", "GOWORK=off", "GOENV=off", "GOEXPERIMENT=", "GOPROXY=off", "GOSUMDB=off")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	deps := exec.Command(goPath, "list", "-deps", "-mod=readonly", "-f", "{{if .Goroot}}{{.ImportPath}}{{end}}", "./cmd/keepupdated")
	deps.Env = cmd.Env
	dependencyOutput, err := deps.Output()
	if err != nil {
		return errors.New("could not enumerate runtime dependencies for notices")
	}
	goNotices, err := runtimeNotices(runtime.GOROOT(), strings.Fields(string(dependencyOutput)))
	if err != nil {
		return err
	}
	moduleFiles, err := moduleNotices(goPath, filepath.Join(staging, "keepupdated"), cmd.Env, info)
	if err != nil {
		return err
	}
	license, err := os.ReadFile("LICENSE")
	if err != nil {
		return err
	}
	metadata, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	files := []archiveFile{{"keepupdated", nil, 0755}, {"LICENSE", license, 0644}, {"README.md", []byte(packageReadme), 0644}, {"BUILDINFO.json", append(metadata, '\n'), 0644}}
	files = append(files, goNotices...)
	files = append(files, moduleFiles...)
	for _, file := range []archiveFile{{"skills/keepupdated/SKILL.md", nil, 0644}, {"skills/keepupdated/scripts/run.sh", nil, 0755}} {
		file.data, err = os.ReadFile(filepath.FromSlash(file.name))
		if err != nil {
			return err
		}
		files = append(files, file)
	}
	binary, err := os.ReadFile(filepath.Join(staging, "keepupdated"))
	if err != nil {
		return err
	}
	files[0].data = binary
	sum, err := writeRelease(*out, name, files, timestamp, func() error {
		if !*allowDirty {
			return verifyCleanSource(sourceCommit)
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Print(sum)
	return nil
}

func verifyCleanSource(commit string) error {
	current, err := git("rev-parse", "HEAD")
	if err != nil || current != commit {
		return errors.New("source commit changed during build")
	}
	current, err = git("status", "--porcelain", "--untracked-files=normal")
	if err != nil || current != "" {
		return errors.New("working tree changed during build")
	}
	return nil
}

func outputsAbsent(out, name string) error {
	for _, file := range []string{name, "SHA256SUMS"} {
		if _, err := os.Lstat(filepath.Join(out, file)); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return errors.New("output already exists; use a new --out directory to preserve the earlier candidate")
		}
	}
	return nil
}

func writeRelease(out, name string, files []archiveFile, timestamp time.Time, verifySource func() error) (sum string, err error) {
	if err := outputsAbsent(out, name); err != nil {
		return "", err
	}
	// All archive inputs are in memory before this final source check.
	if err := verifySource(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(out, name)
	if err := writeArchive(path, files, timestamp); err != nil {
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, os.Remove(path))
		}
	}()
	archive, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum = fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), name)
	checksumPath := filepath.Join(out, "SHA256SUMS")
	checksum, err := os.OpenFile(checksumPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return "", err
	}
	_, writeErr := checksum.WriteString(sum)
	err = errors.Join(writeErr, checksum.Close())
	if err != nil {
		return "", errors.Join(err, os.Remove(checksumPath))
	}
	complete = true
	return sum, nil
}

func git(args ...string) (string, error) {
	data, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s failed", args[0])
	}
	return strings.TrimSpace(string(data)), nil
}

type archiveFile struct {
	name string
	data []byte
	mode int64
}

func runtimeNotices(goRoot string, imports []string) ([]archiveFile, error) {
	license, err := os.ReadFile(filepath.Join(goRoot, "LICENSE"))
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{"PATENTS": true}
	for _, imp := range imports {
		if strings.HasSuffix(imp, "/nistec/fiat") {
			paths["src/"+imp+"/README"] = true
		}
		if !strings.HasPrefix(imp, "vendor/") {
			continue
		}
		foundLicense := false
		for dir := "src/" + imp; dir != "src/vendor"; dir = filepath.ToSlash(filepath.Dir(dir)) {
			for _, name := range []string{"LICENSE", "PATENTS"} {
				path := dir + "/" + name
				_, err := os.Stat(filepath.Join(goRoot, filepath.FromSlash(path)))
				if err == nil {
					paths[path] = true
					foundLicense = foundLicense || name == "LICENSE"
				} else if !os.IsNotExist(err) {
					return nil, err
				}
			}
		}
		if !foundLicense {
			return nil, fmt.Errorf("missing runtime vendor license for %s", imp)
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	var notices strings.Builder
	notices.WriteString("Go runtime notices\nSources below are relative to the build toolchain GOROOT; original texts follow.\n")
	for _, path := range ordered {
		text, err := os.ReadFile(filepath.Join(goRoot, filepath.FromSlash(path)))
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&notices, "\n===== %s =====\n", path)
		notices.Write(text)
		if len(text) == 0 || text[len(text)-1] != '\n' {
			notices.WriteByte('\n')
		}
	}
	return []archiveFile{{"LICENSES/Go.txt", license, 0644}, {"THIRD_PARTY_NOTICES.txt", []byte(notices.String()), 0644}}, nil
}

func writeArchive(path string, files []archiveFile, timestamp time.Time) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, f.Close())
		if err != nil {
			err = errors.Join(err, os.Remove(path))
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, file := range files {
		if err := tw.WriteHeader(&tar.Header{Name: file.name, Mode: file.mode, Size: int64(len(file.data)), ModTime: timestamp.UTC(), Format: tar.FormatUSTAR}); err != nil {
			return err
		}
		if _, err := tw.Write(file.data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// Keep the package self-contained: instructions and required licenses travel with it.
const packageReadme = `# KeepUpdated

Software inventory, update planning and confirmed changes for Apple Silicon, macOS 13+.
No Go, Node or Python is needed to run this binary.

From this extracted directory:

    ./keepupdated version
    ./keepupdated --help
    ./keepupdated scan > scan.json
    ./keepupdated check --online --store-country AU > check.json

For an Agent, explicitly load skills/keepupdated/SKILL.md from this extracted
directory. Keep its scripts/ folder with the package. The Skill calls only this
0.1.2 binary after checking version metadata; it does not download or install.
The version metadata must include the planning and apply schemas. A binary
without the required protocols is not compatible with this Skill.
Explicit-path loading depends on the Agent client's actual capabilities.

scan and check only inspect software; check uses the network with --online.
plan previews updates or uninstall actions. apply executes the exact confirmed
plan after a fresh check. JSON is the default. See the bundled Skill for examples.
Review diagnostics and per-item unknown/unsupported states before using results.

mcp starts the local stdio server. This standalone CLI archive has no native
approval companion. Read-only tools and previews work; execution needs the
matching KeepUpdatedAgent.app or full App bundle and its native confirmation.
Reports contain installed software and local paths; review them before sharing.

This binary is not Developer ID signed or Apple notarized. Gatekeeper may block
a downloaded copy. Follow Apple's normal security review; no protection changes
are required by this tool. Source build instructions and coverage:
https://github.com/JasonHMY/KeepUpdated

BUILDINFO.json records the version, source commit, toolchain and target.
LICENSE contains the MIT license. Verify the downloaded archive against
SHA256SUMS from the same release before extracting it.
LICENSES/Go.txt and THIRD_PARTY_NOTICES.txt preserve the Go runtime and imported
standard-library/vendor copyright, license and patent notices.
LICENSES/modules.json binds actual linked third-party modules and original notice
hashes to this build; LICENSES/modules/ and ModuleNotices.txt retain those texts.

`
