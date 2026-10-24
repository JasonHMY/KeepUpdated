// Command prepare-mas extracts a pinned upstream manager without installing or running it.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/macho"
	"embed"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

//go:embed assets.json licenses
var inputs embed.FS

type asset struct {
	Architecture  string `json:"architecture"`
	URL           string `json:"url"`
	ArchiveSHA256 string `json:"archive_sha256"`
	BinarySHA256  string `json:"binary_sha256"`
}
type manifest struct {
	Version      string  `json:"version"`
	SourceCommit string  `json:"source_commit"`
	MinimumMacOS string  `json:"minimum_macos"`
	Assets       []asset `json:"assets"`
}
type prepared struct {
	Version      string   `json:"version"`
	SourceCommit string   `json:"source_commit"`
	Asset        asset    `json:"asset"`
	MinimumMacOS string   `json:"minimum_macos"`
	Dylibs       []string `json:"dylibs"`
	RPaths       []string `json:"rpaths"`
}

func main() {
	arch := flag.String("arch", "", "arm64 or x86_64")
	out := flag.String("out", "", "new output directory inside a private staging directory")
	flag.Parse()
	if flag.NArg() != 0 || *out == "" || runtime.GOOS != "darwin" {
		fail(errors.New("requires macOS, --arch and --out; use --help"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	result, err := prepare(ctx, *arch, *out, &http.Client{Timeout: 2 * time.Minute})
	if err != nil {
		fail(err)
	}
	if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fail(err)
	}
}
func fail(err error)         { fmt.Fprintln(os.Stderr, "prepare-mas:", err); os.Exit(1) }
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func pinned(arch string) (manifest, asset, error) {
	var m manifest
	data, _ := inputs.ReadFile("assets.json")
	if err := json.Unmarshal(data, &m); err != nil {
		return m, asset{}, err
	}
	if arch != "arm64" && arch != "x86_64" {
		return m, asset{}, errors.New("unsupported architecture")
	}
	for _, a := range m.Assets {
		if a.Architecture == arch {
			return m, a, nil
		}
	}
	return m, asset{}, errors.New("architecture pin unavailable")
}

func prepare(ctx context.Context, arch, out string, client *http.Client) (result prepared, err error) {
	m, a, err := pinned(arch)
	if err != nil {
		return result, err
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return result, errors.New("invalid output directory")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(out))
	if err != nil {
		return result, errors.New("staging parent unavailable")
	}
	st, err := os.Lstat(parent)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return result, errors.New("staging parent must be a private directory")
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) {
		return result, errors.New("staging parent must belong to the current user")
	}
	out = filepath.Join(parent, filepath.Base(out))
	if _, err = os.Lstat(out); !os.IsNotExist(err) {
		return result, errors.New("output exists or is inaccessible; choose a new directory")
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return result, errors.New("invalid pinned URL")
	}
	response, err := client.Do(request)
	if err != nil {
		return result, errors.New("official asset download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, errors.New("official asset download failed")
	}
	archive, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if err != nil || len(archive) > 16<<20 {
		return result, errors.New("official asset exceeds download limit or is unreadable")
	}
	if digest(archive) != a.ArchiveSHA256 {
		return result, errors.New("official archive SHA-256 mismatch")
	}

	scratch, err := os.MkdirTemp(parent, ".prepare-mas-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(scratch)
	archivePath := filepath.Join(scratch, "official.pkg")
	if err = os.WriteFile(archivePath, archive, 0600); err != nil {
		return result, err
	}
	expanded := filepath.Join(scratch, "expanded")
	// pkgutil only expands files. Never invoke installer, package scripts, or the manager.
	command := exec.CommandContext(ctx, "/usr/sbin/pkgutil", "--expand-full", archivePath, expanded)
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	if command.Run() != nil {
		return result, errors.New("official package extraction failed")
	}
	var info struct {
		Version    string `xml:"version,attr"`
		Identifier string `xml:"identifier,attr"`
	}
	metadata, err := os.ReadFile(filepath.Join(expanded, "mas.pkg/PackageInfo"))
	if err != nil || xml.Unmarshal(metadata, &info) != nil || info.Version != m.Version || info.Identifier != "io.github.mas-cli.mas" {
		return result, errors.New("official package identity/version mismatch")
	}
	binaryPath := filepath.Join(expanded, "mas.pkg/Payload/usr/local/opt/mas/libexec/bin/mas")
	real, err := filepath.EvalSymlinks(binaryPath)
	if err != nil || real != binaryPath {
		return result, errors.New("unexpected manager path")
	}
	st, err = os.Lstat(binaryPath)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 32<<20 {
		return result, errors.New("manager is not a bounded regular file")
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil || digest(binary) != a.BinarySHA256 {
		return result, errors.New("extracted manager SHA-256 mismatch")
	}
	result, err = inspect(binary, m, a)
	if err != nil {
		return result, err
	}

	if err = os.Mkdir(out, 0700); err != nil {
		return result, errors.New("output directory could not be created")
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(out)
		}
	}()
	if err = os.WriteFile(filepath.Join(out, "mas"), binary, 0755); err != nil {
		return result, err
	}
	if err = fs.WalkDir(inputs, "licenses", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		target := filepath.Join(out, filepath.FromSlash(path))
		if d.IsDir() {
			return os.Mkdir(target, 0755)
		}
		data, e := inputs.ReadFile(path)
		if e != nil {
			return e
		}
		return os.WriteFile(target, data, 0644)
	}); err != nil {
		return result, err
	}
	source, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return result, err
	}
	if err = os.WriteFile(filepath.Join(out, "SOURCE.json"), append(source, '\n'), 0644); err != nil {
		return result, err
	}
	return result, nil
}

func inspect(binary []byte, m manifest, a asset) (prepared, error) {
	r := prepared{Version: m.Version, SourceCommit: m.SourceCommit, Asset: a, MinimumMacOS: m.MinimumMacOS, Dylibs: []string{}, RPaths: []string{}}
	f, err := macho.NewFile(bytes.NewReader(binary))
	if err != nil {
		return r, errors.New("manager is not a supported Mach-O")
	}
	defer f.Close()
	cpu := macho.CpuArm64
	if a.Architecture == "x86_64" {
		cpu = macho.CpuAmd64
	}
	if f.Cpu != cpu || f.Type != macho.TypeExec {
		return r, errors.New("manager architecture/type mismatch")
	}
	if !bytes.Contains(binary, []byte(m.SourceCommit)) || !bytes.Contains(binary, []byte(m.Version+"\x00")) {
		return r, errors.New("manager build identity mismatch")
	}
	buildFound := false
	for _, load := range f.Loads {
		raw := load.Raw()
		if len(raw) < 8 {
			return r, errors.New("invalid Mach-O load command")
		}
		kind := f.ByteOrder.Uint32(raw[:4])
		switch kind {
		case 0x32: // LC_BUILD_VERSION
			if len(raw) < 24 || f.ByteOrder.Uint32(raw[8:12]) != 1 || f.ByteOrder.Uint32(raw[12:16]) != 0x000d0000 {
				return r, errors.New("manager minimum macOS mismatch")
			}
			buildFound = true
		case 0xc, 0x80000018, 0x8000001f, 0x20, 0x80000023: // All dylib import commands, including weak imports.
			path, e := loadPath(raw, f.ByteOrder.Uint32, 24)
			if e != nil {
				return r, e
			}
			if !strings.HasPrefix(path, "/System/Library/") && !strings.HasPrefix(path, "/usr/lib/") {
				return r, errors.New("manager imports a non-system dylib")
			}
			r.Dylibs = append(r.Dylibs, path)
		case 0x8000001c: // LC_RPATH is recorded separately; the shared boundary checks dylib imports.
			path, e := loadPath(raw, f.ByteOrder.Uint32, 12)
			if e != nil {
				return r, e
			}
			r.RPaths = append(r.RPaths, path)
		}
	}
	if !buildFound || len(r.Dylibs) == 0 {
		return r, errors.New("manager platform/dependencies unavailable")
	}
	return r, nil
}
func loadPath(raw []byte, u32 func([]byte) uint32, minimum uint32) (string, error) {
	if len(raw) < int(minimum) {
		return "", errors.New("invalid Mach-O path command")
	}
	offset := u32(raw[8:12])
	if offset < minimum || uint64(offset) >= uint64(len(raw)) {
		return "", errors.New("invalid Mach-O path offset")
	}
	path := raw[offset:]
	end := bytes.IndexByte(path, 0)
	if end <= 0 {
		return "", errors.New("invalid Mach-O path")
	}
	return string(path[:end]), nil
}
