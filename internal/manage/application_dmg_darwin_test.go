//go:build darwin

package manage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Archive authentication, mount, copy, ad-hoc code signature, atomic exchange
// and readback are real. Developer ID and Gatekeeper authority are controlled.
func TestApplicationDMGNativeMountAndExchange(t *testing.T) {
	d := dmgSetup(t)
	n := d.n
	ctx := context.Background()
	isolated, e := os.MkdirTemp("/private/tmp", "keepupdated-dmg-native-")
	if e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(isolated, "Applications/Native.app")
	if e = os.Mkdir(filepath.Dir(target), 0700); e != nil {
		t.Fatal(e)
	}
	n.f.o.Home = isolated
	if e = os.Rename(n.i.Path, target); e != nil {
		t.Fatal(e)
	}
	n.i.Path, n.i.RealPath = target, target
	n.f.r.Items[0] = n.i
	t.Cleanup(func() {
		if d.attached && !d.detached {
			t.Log("Native mount evidence retained:", isolated)
			return
		}
		if e := os.RemoveAll(isolated); e != nil {
			t.Error(e)
		}
	})
	root := t.TempDir()
	candidate, e := extractApplicationZIP(ctx, d.payload, root)
	if e != nil {
		t.Fatal(e)
	}
	command := func(path string, args ...string) []byte {
		t.Helper()
		b, e := appCommand(ctx, ExecRunner{}, path, args...)
		if e != nil {
			t.Fatalf("%s %v: %v %s", path, args, e, b)
		}
		return b
	}
	for _, app := range []string{n.i.Path, candidate} {
		command("/bin/cp", "/usr/bin/true", filepath.Join(app, "Contents/MacOS/Native"))
		metadata := filepath.Join(app, "Contents/Info.plist")
		b, e := os.ReadFile(metadata)
		if e != nil {
			t.Fatal(e)
		}
		var fields map[string]any
		if e = json.Unmarshal(b, &fields); e != nil {
			t.Fatal(e)
		}
		for k, v := range fields {
			if v == nil {
				delete(fields, k)
			}
		}
		b, e = json.Marshal(fields)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(metadata, b, 0644); e != nil {
			t.Fatal(e)
		}
		command("/usr/bin/plutil", "-convert", "xml1", metadata)
		command("/usr/bin/codesign", "--force", "--sign", "-", app)
	}
	image := filepath.Join(t.TempDir(), "fixture.dmg")
	command("/usr/bin/hdiutil", "create", "-srcfolder", root, "-volname", "KeepUpdated DMG Fixture", "-fs", "HFS+", "-format", "UDZO", image)
	n.data, e = os.ReadFile(image)
	if e != nil {
		t.Fatal(e)
	}
	d.sign()
	controlled := n.runner
	n.runner = runFunc(func(ctx context.Context, c Command) ([]byte, error) {
		switch c.Path {
		case "/usr/bin/plutil", "/usr/bin/hdiutil", "/bin/cp":
			if c.Path == "/usr/bin/hdiutil" {
				if c.Args[0] == "attach" {
					d.attached = true
				}

			}
			b, e := (ExecRunner{}).Run(ctx, c)
			if c.Path == "/usr/bin/hdiutil" && c.Args[0] == "detach" {
				d.detached = e == nil
			}
			if c.Path == "/usr/bin/hdiutil" && c.Args[0] == "attach" {
				// Ensure native mounts are detached before TempDir's cleanup even
				// when testing a parser rejection. This is only fixture cleanup.
				evidence := filepath.Join(t.TempDir(), "attach.plist")
				if we := os.WriteFile(evidence, b, 0600); we != nil {
					t.Fatal(we)
				}
				raw, pe := appCommand(context.Background(), ExecRunner{}, "/usr/bin/plutil", "-extract", "system-entities", "json", "-o", "-", evidence)
				var entities []struct {
					Device  string `json:"dev-entry"`
					Content string `json:"content-hint"`
				}
				if pe == nil && json.Unmarshal(raw, &entities) == nil {
					for _, entity := range entities {
						if entity.Content == "GUID_partition_scheme" || entity.Content == "Apple_partition_scheme" {
							device := entity.Device
							t.Cleanup(func() {
								if !d.detached {
									_, cleanupErr := appCommand(context.Background(), ExecRunner{}, "/usr/bin/hdiutil", "detach", device)
									d.detached = cleanupErr == nil
								}
							})
						}
					}
				}
			}
			return b, e
		case "/usr/bin/codesign":
			if c.Args[0] == "--verify" {
				return appCommand(ctx, ExecRunner{}, c.Path, "--verify", "--deep", "--strict", "--all-architectures", c.Args[len(c.Args)-1])
			}
		}
		return controlled.Run(ctx, c)
	})
	p := n.plan()
	result, e := n.apply(p)
	if e != nil || result.Results[0].Status != "updated" || !d.attached || !d.detached {
		t.Fatal(result, e)
	}
	backup := result.Results[0].BackupPath
	for path, build := range map[string]string{n.i.Path: "20", backup: "10"} {
		info, e := readApplicationInfo(ctx, n.runner, path)
		if e != nil || info.Build != build {
			t.Fatal(info, e)
		}
		command("/usr/bin/codesign", "--verify", "--deep", "--strict", path)
	}
	if strings.Contains(backup, "/mount/") {
		t.Fatal("backup remains mounted", backup)
	}
}

func TestApplicationDMGCaseSensitivePaths(t *testing.T) {
	root, e := os.MkdirTemp("/private/tmp", "keepupdated-dmg-case-")
	if e != nil {
		t.Fatal(e)
	}
	image := filepath.Join(root, "case.dmg")
	mount := filepath.Join(root, "mount")
	if e = os.Mkdir(mount, 0700); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	run := func(path string, args ...string) ([]byte, error) { return appCommand(ctx, ExecRunner{}, path, args...) }
	if b, e := run("/usr/bin/hdiutil", "create", "-size", "32m", "-fs", "Case-sensitive HFS+", "-volname", "Case Fixture", image); e != nil {
		t.Fatalf("create: %s %v", b, e)
	}
	// No automatic RemoveAll: an unknown attachment must retain its private parent.
	b, e := run("/usr/bin/hdiutil", "attach", "-readwrite", "-nobrowse", "-noautoopen", "-mountpoint", mount, "-plist", image)
	result := filepath.Join(root, "attach.plist")
	os.WriteFile(result, b, 0600)
	if e != nil {
		t.Fatalf("attach; retained %s: %v", root, e)
	}
	raw, e := run("/usr/bin/plutil", "-extract", "system-entities", "json", "-o", "-", result)
	if e != nil {
		t.Fatalf("parse; retained %s: %v", root, e)
	}
	var entities []struct {
		Device  string `json:"dev-entry"`
		Content string `json:"content-hint"`
	}
	if e = json.Unmarshal(raw, &entities); e != nil {
		t.Fatal(e)
	}
	device := ""
	for _, entry := range entities {
		if entry.Content == "GUID_partition_scheme" || entry.Content == "Apple_partition_scheme" {
			if device != "" {
				t.Fatal("multiple devices")
			}
			device = entry.Device
		}
	}
	if device == "" {
		t.Fatalf("unknown device; retained %s", root)
	}
	defer func() {
		if _, e := run("/usr/bin/hdiutil", "detach", device); e != nil {
			t.Errorf("detach; retained %s: %v", root, e)
			return
		}
		if e = os.RemoveAll(root); e != nil {
			t.Error(e)
		}
	}()
	app := filepath.Join(mount, "App.app")
	if e = os.Mkdir(app, 0755); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"a", "A"} {
		if e = os.WriteFile(filepath.Join(app, name), []byte(name), 0644); e != nil {
			t.Fatal(e)
		}
	}
	entries, e := os.ReadDir(app)
	if e != nil || len(entries) != 2 {
		t.Fatal("case-sensitive fixture unavailable", entries, e)
	}
	if e := copyApplicationDMG(ctx, ExecRunner{}, app, filepath.Join(root, "stage.app")); e == nil || e.Error() != "application_dmg_duplicate_path" {
		t.Fatal(e)
	}
}

func TestApplicationDMGCountsResourceForkBytes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "App.app")
	if e := os.Mkdir(root, 0755); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(root, "payload")
	f, e := os.Create(file)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Truncate(2 << 30); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(file+"/..namedfork/rsrc", []byte("x"), 0600); e != nil {
		t.Fatal(e)
	}
	copies := 0
	runner := runFunc(func(ctx context.Context, c Command) ([]byte, error) {
		copies++
		target := c.Args[len(c.Args)-1]
		f, e := os.Create(target)
		if e != nil {
			return nil, e
		}
		e = f.Truncate(2 << 30)
		f.Close()
		return nil, e
	})
	if e := copyApplicationDMG(context.Background(), runner, root, filepath.Join(t.TempDir(), "App.app")); e == nil || e.Error() != "application_dmg_limit" || copies != 0 {
		t.Fatalf("resource fork exceeded file limit: copies=%d err=%v", copies, e)
	}
}

func TestApplicationDMGPreservesNativeFileMetadata(t *testing.T) {
	root := filepath.Join(t.TempDir(), "App.app")
	if e := os.Mkdir(root, 0755); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(root, "payload")
	if e := os.WriteFile(file, []byte("data"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(file+"/..namedfork/rsrc", []byte("resource"), 0600); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	if _, e := appCommand(ctx, ExecRunner{}, "/usr/bin/xattr", "-w", "com.example.keepupdated.fixture", "metadata", file); e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(t.TempDir(), "App.app")
	if e := copyApplicationDMG(ctx, ExecRunner{}, root, dest); e != nil {
		t.Fatal(e)
	}
	copied := filepath.Join(dest, "payload")
	if b, e := os.ReadFile(copied + "/..namedfork/rsrc"); e != nil || string(b) != "resource" {
		t.Fatal(string(b), e)
	}
	if b, e := appCommand(ctx, ExecRunner{}, "/usr/bin/xattr", "-p", "com.example.keepupdated.fixture", copied); e != nil || strings.TrimSpace(string(b)) != "metadata" {
		t.Fatal(string(b), e)
	}
}
