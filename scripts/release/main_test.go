package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestArchiveReproducibleAndPortable(t *testing.T) {
	files := []archiveFile{{"keepupdated", []byte("synthetic binary"), 0755}, {"LICENSE", []byte("synthetic license"), 0644}}
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first.tar.gz"), filepath.Join(dir, "second.tar.gz")
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("test", 3600))
	for _, name := range []string{first, second} {
		if err := writeArchive(name, files, stamp); err != nil {
			t.Fatal(err)
		}
	}
	a, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(second)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("archives are not reproducible: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	if gz.Name != "" || gz.Comment != "" || !gz.ModTime.IsZero() {
		t.Fatal("gzip header contains variable metadata")
	}
	tr := tar.NewReader(gz)
	for _, file := range files {
		h, err := tr.Next()
		if err != nil {
			t.Fatal(err)
		}
		if h.Name != file.name || h.Mode != file.mode || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || !h.ModTime.Equal(stamp) {
			t.Fatalf("non-portable archive header: %+v", h)
		}
		data, err := io.ReadAll(tr)
		if err != nil || !bytes.Equal(data, file.data) {
			t.Fatalf("changed file contents: %v", err)
		}
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatalf("unexpected extra archive contents: %v", err)
	}
}

func TestRuntimeNoticesPreserveGoCopyright(t *testing.T) {
	files, err := runtimeNotices(runtime.GOROOT(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].name != "LICENSES/Go.txt" || files[1].name != "THIRD_PARTY_NOTICES.txt" {
		t.Fatalf("unexpected notice file list: %+v", files)
	}
	license, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(files[0].data, license) || !bytes.Contains(files[0].data, []byte("Copyright 2009 The Go Authors.")) || !bytes.Contains(files[0].data, []byte("Redistributions in binary form")) {
		t.Fatal("Go binary redistribution notice was altered or omitted")
	}
	if bytes.Contains(files[1].data, []byte(runtime.GOROOT())) {
		t.Fatal("notice contains an absolute toolchain path")
	}
}

func TestRuntimeNoticesSelectOnlyImportedSources(t *testing.T) {
	root := t.TempDir()
	texts := map[string]string{
		"LICENSE":                                        "synthetic Go license\n",
		"PATENTS":                                        "synthetic Go patents\n",
		"src/vendor/example.org/network/LICENSE":         "network copyright and original license\n",
		"src/vendor/example.org/network/PATENTS":         "network original patent grant\n",
		"src/vendor/example.org/unused/LICENSE":          "unused vendor must not ship\n",
		"src/cmd/vendor/example.org/tool/LICENSE":        "compiler tool must not ship\n",
		"src/crypto/internal/fips140/nistec/fiat/README": "synthetic fiat-crypto original copyright\n",
		"src/crypto/internal/boring/LICENSE":             "unlinked BoringSSL must not ship\n",
	}
	for path, text := range texts {
		file := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	imports := []string{"vendor/example.org/network/http", "crypto/internal/boring", "vendor/example.org/network/http", "crypto/internal/fips140/nistec/fiat"}
	files, err := runtimeNotices(root, imports)
	if err != nil {
		t.Fatal(err)
	}
	notices := string(files[1].data)
	for _, path := range []string{"PATENTS", "src/vendor/example.org/network/LICENSE", "src/vendor/example.org/network/PATENTS", "src/crypto/internal/fips140/nistec/fiat/README"} {
		if strings.Count(notices, "===== "+path+" =====") != 1 || !strings.Contains(notices, texts[path]) {
			t.Fatalf("missing, duplicated or altered notice: %s", path)
		}
	}
	for _, excluded := range []string{root, "unused vendor", "compiler tool", "unlinked BoringSSL"} {
		if strings.Contains(notices, excluded) {
			t.Fatalf("unrelated or private source included: %s", excluded)
		}
	}
	reversed := []string{imports[3], imports[2], imports[1], imports[0]}
	again, err := runtimeNotices(root, reversed)
	if err != nil || !bytes.Equal(files[1].data, again[1].data) {
		t.Fatal("dependency enumeration order changes notices")
	}
	if _, err := runtimeNotices(root, []string{"vendor/example.org/missing/package"}); err == nil {
		t.Fatal("missing imported vendor license did not stop packaging")
	}
}

func TestReleaseRejectsSourceChangeAfterInputsCollected(t *testing.T) {
	for _, change := range []string{"skill-content", "file-mode", "same-content-symlink", "head"} {
		t.Run(change, func(t *testing.T) {
			root := releaseFixture(t)
			t.Chdir(root)
			commit, err := git("rev-parse", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile("SKILL.md")
			if err != nil {
				t.Fatal(err)
			}
			files := []archiveFile{{"skills/keepupdated/SKILL.md", data, 0644}}
			switch change {
			case "skill-content":
				mustWrite(t, "SKILL.md", "concurrent edit")
			case "file-mode":
				if err := os.Chmod("SKILL.md", 0755); err != nil {
					t.Fatal(err)
				}
			case "same-content-symlink":
				if err := os.Remove("SKILL.md"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("same.txt", "SKILL.md"); err != nil {
					t.Fatal(err)
				}
			case "head":
				fixtureGit(t, root, "commit", "--allow-empty", "-m", "concurrent commit")
			}
			out := filepath.Join(t.TempDir(), "release")
			_, err = writeRelease(out, "release.tar.gz", files, time.Unix(0, 0), func() error { return verifyCleanSource(commit) })
			if err == nil {
				t.Fatal("changed source was published")
			}
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatalf("rejected build created output: %v", err)
			}
		})
	}
}

func TestReleasePreservesEitherExistingOutput(t *testing.T) {
	for _, existing := range []string{"release.tar.gz", "SHA256SUMS", "dangling-checksum-link"} {
		t.Run(existing, func(t *testing.T) {
			out := t.TempDir()
			path := filepath.Join(out, existing)
			if existing == "dangling-checksum-link" {
				path = filepath.Join(out, "SHA256SUMS")
				if err := os.Symlink("absent", path); err != nil {
					t.Fatal(err)
				}
			} else {
				mustWrite(t, path, "preserved artifact")
			}
			checked := false
			_, err := writeRelease(out, "release.tar.gz", nil, time.Unix(0, 0), func() error { checked = true; return nil })
			if err == nil || checked {
				t.Fatalf("existing output not rejected before publication: %v", err)
			}
			if existing == "dangling-checksum-link" {
				if target, err := os.Readlink(path); err != nil || target != "absent" {
					t.Fatal("existing symlink changed")
				}
			} else if data, err := os.ReadFile(path); err != nil || string(data) != "preserved artifact" {
				t.Fatal("existing artifact changed")
			}
			entries, err := os.ReadDir(out)
			if err != nil || len(entries) != 1 {
				t.Fatalf("rejected build left partial outputs: %v", err)
			}
		})
	}
}

func TestReleaseRollsBackWhenChecksumAppearsDuringPublication(t *testing.T) {
	out := t.TempDir()
	_, err := writeRelease(out, "release.tar.gz", nil, time.Unix(0, 0), func() error {
		return os.WriteFile(filepath.Join(out, "SHA256SUMS"), []byte("another publisher"), 0644)
	})
	if err == nil {
		t.Fatal("concurrent checksum was overwritten")
	}
	if data, err := os.ReadFile(filepath.Join(out, "SHA256SUMS")); err != nil || string(data) != "another publisher" {
		t.Fatal("concurrent output changed")
	}
	if _, err := os.Lstat(filepath.Join(out, "release.tar.gz")); !os.IsNotExist(err) {
		t.Fatal("failed publication left its archive")
	}
}

func TestArchiveFailureRemovesOnlyItsNewOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "release.tar.gz")
	if err := writeArchive(out, []archiveFile{{strings.Repeat("long/", 100), nil, 0644}}, time.Unix(0, 0)); err == nil {
		t.Fatal("invalid USTAR name accepted")
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatal("failed archive left partial output")
	}
	mustWrite(t, out, "earlier archive")
	if err := writeArchive(out, nil, time.Unix(0, 0)); err == nil {
		t.Fatal("existing archive overwritten")
	}
	if data, err := os.ReadFile(out); err != nil || string(data) != "earlier archive" {
		t.Fatal("earlier archive changed")
	}
}

func TestReleasePublishesMatchingArchiveAndChecksum(t *testing.T) {
	out := t.TempDir()
	sum, err := writeRelease(out, "release.tar.gz", []archiveFile{{"sample", []byte("fixture"), 0644}}, time.Unix(0, 0), func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
	if err != nil || string(data) != sum {
		t.Fatal("checksum was not published")
	}
	if _, err := os.Stat(filepath.Join(out, "release.tar.gz")); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopDigestBindsModeAndSameContentLinks(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("desktop script uses macOS stat")
	}
	script, err := os.ReadFile("../desktop/build.sh")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(script), "source_digest() {\n")
	if start < 0 {
		t.Fatal("source_digest missing")
	}
	end := strings.Index(string(script[start:]), "\n}\n")
	if end < 0 {
		t.Fatal("source_digest end missing")
	}
	function := string(script[start : start+end+3])
	for _, change := range []string{"mode", "file-to-link", "link-target", "link-target-newline"} {
		t.Run(change, func(t *testing.T) {
			root := releaseFixture(t)
			path := filepath.Join(root, "SKILL.md")
			if strings.HasPrefix(change, "link-target") {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("same.txt", path); err != nil {
					t.Fatal(err)
				}
				fixtureGit(t, root, "add", ".")
				fixtureGit(t, root, "commit", "-m", "link baseline")
			}
			digest := func() string {
				cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+function+"\nsource_digest")
				cmd.Dir = root
				data, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("digest: %v: %s", err, data)
				}
				return string(data)
			}
			before := digest()
			switch change {
			case "mode":
				if err := os.Chmod(path, 0755); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				target := "same.txt"
				if change == "link-target" {
					target = "another.txt"
				}
				if change == "link-target-newline" {
					target = "same.txt\n"
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if before == digest() {
				t.Fatal("source shape changed without changing digest")
			}
		})
	}
}

func releaseFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"SKILL.md", "same.txt", "another.txt", "same.txt\n"} {
		mustWrite(t, filepath.Join(root, name), "fixture skill\n")
	}
	fixtureGit(t, root, "init", "-q")
	fixtureGit(t, root, "config", "core.filemode", "true")
	fixtureGit(t, root, "config", "core.fsmonitor", "false")
	fixtureGit(t, root, "add", ".")
	fixtureGit(t, root, "commit", "-m", "baseline")
	return root
}

func fixtureGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
}

func mustWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0644); err != nil {
		t.Fatal(err)
	}
}
