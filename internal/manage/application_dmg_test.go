package manage

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

type dmgFixture struct {
	n                                    *nativeFixture
	payload                              []byte
	mount, device                        string
	attached, detached                   bool
	attachFail, detachFail, multiVolume  bool
	malformed, wrongMount, unknownDevice bool
	afterMount                           func(string)
	afterCopy                            func()
}

func dmgSetup(t *testing.T) *dmgFixture {
	n := nativeSetup(t)
	d := &dmgFixture{n: n, payload: n.data, device: "/dev/disk99"}
	n.data = make([]byte, 512)
	copy(n.data, "koly")
	d.sign()
	n.f.o.ApplicationHTTPClient = &http.Client{Transport: appTransport(func(r *http.Request) (*http.Response, error) {
		b := []byte(n.feed)
		if strings.HasSuffix(r.URL.Path, ".dmg") {
			if n.download != nil {
				n.download()
			}
			b = n.data
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), ContentLength: int64(len(b))}, nil
	})}
	original := n.runner
	n.runner = runFunc(func(ctx context.Context, c Command) ([]byte, error) {
		switch c.Path {
		case "/usr/bin/hdiutil":
			if c.Args[0] == "detach" {
				if len(c.Args) != 2 || c.Args[1] != d.device {
					t.Fatal(c)
				}
				if d.detachFail {
					return nil, errors.New("controlled detach failure")
				}
				d.detached = true
				return nil, nil
			}
			d.attached = true
			for k, a := range c.Args {
				if a == "-mountpoint" {
					d.mount = c.Args[k+1]
				}
			}
			for _, flag := range []string{"-readonly", "-nobrowse", "-noautoopen", "-noautofsck", "-plist"} {
				if !contains(c.Args, flag) {
					t.Fatal("missing mount guard", c)
				}
			}
			if _, err := extractApplicationZIP(ctx, d.payload, d.mount); err != nil {
				return nil, err
			}
			if d.afterMount != nil {
				d.afterMount(d.mount)
			}
			entities := []map[string]any{{"dev-entry": d.device, "content-hint": "GUID_partition_scheme"}, {"dev-entry": d.device + "s1", "content-hint": "Apple_HFS", "mount-point": d.mount, "potentially-mountable": true}}
			if d.multiVolume {
				entities = append(entities, map[string]any{"dev-entry": d.device + "s2", "mount-point": "/other"})
			}
			if d.wrongMount {
				entities[1]["mount-point"] = "/unbound"
			}
			if d.unknownDevice {
				entities = append(entities, map[string]any{"dev-entry": "/dev/disk123s1"})
			}
			b, _ := json.Marshal(entities)
			if d.malformed {
				b = []byte("not a plist")
			}
			if d.attachFail {
				return b, errors.New("controlled attach failure")
			}
			return b, nil
		case "/usr/bin/plutil":
			if c.Args[0] == "-extract" {
				return os.ReadFile(c.Args[len(c.Args)-1])
			}
		case "/bin/cp":
			b, e := (ExecRunner{}).Run(ctx, c)
			if d.afterCopy != nil {
				d.afterCopy()
			}
			return b, e
		}
		return original.Run(ctx, c)
	})
	return d
}
func (d *dmgFixture) sign() {
	n := d.n
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(n.privateKey, n.data))
	n.feed = fmt.Sprintf(`<rss xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle"><channel><item><sparkle:version>20</sparkle:version><sparkle:shortVersionString>2.0</sparkle:shortVersionString><enclosure url="https://vendor.example/Native.dmg" length="%d" sparkle:edSignature="%s"/></item></channel></rss>`, len(n.data), sig)
}
func TestApplicationDMGTransaction(t *testing.T) {
	d := dmgSetup(t)
	p := d.n.plan()
	if p.Items[0].Binding.ResolvedTarget.ApplicationUpdate.Archive.Format != "dmg" || p.Items[0].Reason != "signed_application_dmg_ready" {
		t.Fatal(p.Items)
	}
	result, e := d.n.apply(p)
	if e != nil || result.Results[0].Status != "updated" || !d.attached || !d.detached {
		t.Fatal(result, e, d)
	}
	old, e := readApplicationInfo(context.Background(), d.n.runner, result.Results[0].BackupPath)
	if e != nil || old.Build != "10" {
		t.Fatal(old, e)
	}
	now, e := readApplicationInfo(context.Background(), d.n.runner, d.n.i.Path)
	if e != nil || now.Build != "20" {
		t.Fatal(now, e)
	}
}
func TestApplicationDMGFailurePreservesOriginal(t *testing.T) {
	for _, scenario := range []string{"signature", "format", "multi-app", "pkg", "nested-pkg", "unknown", "escape", "cycle", "absolute", "mount", "multi-volume", "malformed", "wrong-mount", "unknown-device", "detach", "identity", "team", "architecture", "source-drift", "content-drift", "cancel", "cancel-download"} {
		t.Run(scenario, func(t *testing.T) {
			d := dmgSetup(t)
			n := d.n
			switch scenario {
			case "malformed":
				d.malformed = true
			case "wrong-mount":
				d.wrongMount = true
			case "unknown-device":
				d.unknownDevice = true
			case "mount":
				d.attachFail = true
			case "multi-volume":
				d.multiVolume = true
			case "detach":
				d.detachFail = true
			case "multi-app", "pkg", "nested-pkg", "unknown", "escape", "cycle", "absolute", "identity", "architecture":
				d.afterMount = func(m string) {
					root := filepath.Join(m, "Native.app")
					switch scenario {
					case "multi-app":
						n.f.write(filepath.Join(m, "Other.app/a"), "x", 0644)
					case "pkg":
						n.f.write(filepath.Join(m, "Installer.pkg"), "x", 0644)
					case "nested-pkg":
						n.f.write(filepath.Join(root, "Contents/Installer.pkg"), "x", 0644)
					case "unknown":
						n.f.write(filepath.Join(m, "install.sh"), "x", 0644)
					case "escape":
						if e := os.Symlink("../../outside", filepath.Join(root, "escape")); e != nil {
							t.Fatal(e)
						}
					case "absolute":
						if e := os.Symlink(root, filepath.Join(root, "absolute")); e != nil {
							t.Fatal(e)
						}
					case "cycle":
						os.Symlink("cycle", filepath.Join(root, "cycle"))
					case "identity":
						info := n.info
						info.ID = "com.other.Bad"
						b, _ := json.Marshal(info)
						n.f.write(filepath.Join(root, "Contents/Info.plist"), string(b), 0644)
					case "architecture":
						n.f.write(filepath.Join(root, "Contents/MacOS/Native"), "not macho", 0755)
					}
				}
			case "team":
				n.payloadTeam = "ZZZZZ99999"
			}
			p := n.plan()
			switch scenario {
			case "signature":
				n.data[10] ^= 1
			case "format":
				n.data[0] = 0
				d.sign()
				p = n.plan()
			case "source-drift":
				n.download = func() { n.feed = strings.Replace(n.feed, "Native.dmg", "Other.dmg", 1) }
			case "content-drift":
				n.download = func() { n.f.write(filepath.Join(n.i.Path, "user-file"), "preserve", 0644) }
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancel" {
				d.afterCopy = cancel
			}
			if scenario == "cancel-download" {
				n.download = cancel
			}
			result, e := Apply(ctx, p, p.PlanID, Engine{Snapshot: n.report, ResolveTargets: n.resolver, Runner: n.runner, Options: n.f.o, StateDir: n.f.state, Now: func() time.Time { return n.f.o.Now }})
			if e != nil || len(result.Results) != 1 || result.Results[0].Status == "updated" || result.Results[0].Changed {
				t.Fatal(result, e)
			}
			info, e := readApplicationInfo(context.Background(), n.runner, n.i.Path)
			if e != nil || info.Build != "10" {
				t.Fatal("original changed", info, e)
			}
			if (scenario == "signature" || scenario == "format" || scenario == "cancel-download") && d.attached {
				t.Fatal("mounted unauthenticated/mislabeled bytes")
			}
			if scenario == "cancel-download" && result.Results[0].Reason != "application_update_cancelled" {
				t.Fatal(result.Results[0])
			}
			if scenario == "detach" {
				if result.Results[0].Reason != "application_dmg_detach_failed" {
					t.Fatal(result.Results[0])
				}
				if _, e := os.Stat(filepath.Join(filepath.Dir(d.mount), "DMG-CLEANUP.json")); e != nil {
					t.Fatal("cleanup evidence lost", e)
				}
				if _, e := os.Stat(filepath.Join(d.mount, "Native.app")); e != nil {
					t.Fatal("mounted contents deleted", e)
				}
			} else if d.attached && !d.detached && scenario != "malformed" {
				t.Fatal("not detached")
			}
		})
	}
}

func TestApplicationDMGContainedLinksAndLimits(t *testing.T) {
	root := filepath.Join(t.TempDir(), "App.app")
	os.Mkdir(root, 0755)
	os.WriteFile(filepath.Join(root, "file"), []byte("test"), 0644)
	os.Symlink("file", filepath.Join(root, "link"))
	dest := filepath.Join(t.TempDir(), "App.app")
	if e := copyApplicationDMG(context.Background(), ExecRunner{}, root, dest); e != nil {
		t.Fatal(e)
	}
	if link, e := os.Readlink(filepath.Join(dest, "link")); e != nil || link != "file" {
		t.Fatal(link, e)
	}
	f, e := os.Create(filepath.Join(root, "large"))
	if e != nil {
		t.Fatal(e)
	}
	f.Truncate((2 << 30) + 1)
	f.Close()
	if e := copyApplicationDMG(context.Background(), ExecRunner{}, root, filepath.Join(t.TempDir(), "App.app")); e == nil || e.Error() != "application_dmg_limit" {
		t.Fatal(e)
	}
}

func TestApplicationDMGFormatChangesInvalidatePlans(t *testing.T) {
	d := dmgSetup(t)
	n := d.n
	p := n.plan()
	original, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	for _, format := range []string{"", "zip", "unknown"} {
		changed, e := DecodePlan(bytes.NewReader(original))
		if e != nil {
			t.Fatal(e)
		}
		changed.Items[0].Binding.ResolvedTarget.ApplicationUpdate.Archive.Format = format
		if planHash(changed) == p.PlanID {
			t.Fatal("format excluded from plan hash", format)
		}
		changed.PlanID = planHash(changed)
		if result, e := n.apply(changed); e == nil {
			t.Fatal("stale format binding executed", format, result)
		}
		if d.attached {
			t.Fatal("stale format reached mount")
		}
	}
}

func TestApplicationDMGRejectsCopiedContentDrift(t *testing.T) {
	d := dmgSetup(t)
	n := d.n
	p := n.plan()
	base := n.runner
	n.runner = runFunc(func(ctx context.Context, c Command) ([]byte, error) {
		b, e := base.Run(ctx, c)
		if c.Path == "/bin/cp" && strings.HasSuffix(c.Args[len(c.Args)-1], "/Native") && e == nil {
			path := c.Args[len(c.Args)-1]
			data, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			data[len(data)-1] ^= 1
			if e = os.WriteFile(path, data, 0755); e != nil {
				t.Fatal(e)
			}
		}
		return b, e
	})
	result, e := n.apply(p)
	if e != nil || result.Results[0].Reason != "application_dmg_copy_mismatch" || result.Results[0].Changed || !d.detached {
		t.Fatal(result, e)
	}
}

func TestApplicationDMGTotalSizeUsesCopiedFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "App.app")
	if e := os.Mkdir(root, 0755); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"a", "b", "c"} {
		f, e := os.Create(filepath.Join(root, name))
		if e != nil {
			t.Fatal(e)
		}
		if e = f.Truncate(2 << 30); e != nil {
			t.Fatal(e)
		}
		f.Close()
	}
	calls := 0
	runner := runFunc(func(ctx context.Context, c Command) ([]byte, error) {
		if c.Path != "/bin/cp" {
			t.Fatal(c)
		}
		calls++
		f, e := os.Create(c.Args[len(c.Args)-1])
		if e != nil {
			return nil, e
		}
		e = f.Truncate(2 << 30)
		f.Close()
		return nil, e
	})
	if e := copyApplicationDMG(context.Background(), runner, root, filepath.Join(t.TempDir(), "App.app")); e == nil || e.Error() != "application_dmg_limit" || calls != 2 {
		t.Fatal(calls, e)
	}
}

func TestApplicationDMGRejectsUnknownDecoration(t *testing.T) {
	for _, name := range []string{"installer.pkg", "script.sh", "nested", "link.png"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if e := os.Mkdir(filepath.Join(root, "App.app"), 0755); e != nil {
				t.Fatal(e)
			}
			bg := filepath.Join(root, ".background")
			if e := os.Mkdir(bg, 0755); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(bg, name)
			switch name {
			case "nested":
				os.Mkdir(path, 0755)
			case "link.png":
				os.Symlink("/etc/passwd", path)
			default:
				os.WriteFile(path, []byte("x"), 0644)
			}
			if _, e := applicationDMGRoot(root); e == nil {
				t.Fatal("unknown decoration accepted")
			}
		})
	}
}

func TestApplicationDMGEntryLimit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "App.app")
	if e := os.Mkdir(root, 0755); e != nil {
		t.Fatal(e)
	}
	// Directories exercise the real traversal limit without copying large files or
	// launching 100,000 subprocesses. The source root is also counted.
	for i := 0; i < 100000; i++ {
		if e := os.Mkdir(filepath.Join(root, fmt.Sprintf("%06d", i)), 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e := copyApplicationDMG(context.Background(), ExecRunner{}, root, filepath.Join(t.TempDir(), "App.app")); e == nil || e.Error() != "application_dmg_limit" {
		t.Fatal(e)
	}
}

func TestApplicationDMGLifecycle(t *testing.T) {
	for _, scenario := range []string{"success", "refused", "helper", "detach", "source-after-quit", "reopen_failed"} {
		t.Run(scenario, func(t *testing.T) {
			d := dmgSetup(t)
			n := d.n
			lifecycle := &lifecycleFixture{f: n.f, app: n.i, mode: scenario, running: []ApplicationInstance{{PID: 1234, LaunchedAt: "1234.5", Path: n.i.Path, Executable: filepath.Join(n.i.Path, "Contents/MacOS/Native"), Identity: n.i.Identity}}}
			native := n.runner
			n.runner = runFunc(func(ctx context.Context, c Command) ([]byte, error) {
				switch c.Path {
				case "/bin/ps", "/usr/bin/osascript", "/usr/bin/open":
					return lifecycle.Run(ctx, c)
				}
				return native.Run(ctx, c)
			})
			snapshot := func(ctx context.Context) (audit.Report, error) {
				r, e := n.report(ctx)
				r.Items[0].Active = len(lifecycle.running) > 0
				return r, e
			}
			selection := Selection{Action: "update", IDs: []string{n.i.ID}, QuitApps: []string{n.i.ID}, ReopenApps: []string{n.i.ID}}
			r, e := snapshot(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			n.f.o.ResolvedTargets, e = n.resolver(context.Background(), r, selection)
			if e != nil {
				t.Fatal(e)
			}
			p, e := NewPlan(r, selection, n.f.o)
			if e != nil || p.Items[0].Status != "ready" || len(p.Items[0].ApplicationLifecycle) != 1 {
				t.Fatal(p, e)
			}
			if scenario == "detach" {
				d.detachFail = true
			}
			if scenario == "source-after-quit" {
				lifecycle.afterExit = func() { n.feed = strings.Replace(n.feed, "Native.dmg", "Other.dmg", 1) }
			}
			result, e := Apply(context.Background(), p, p.PlanID, Engine{Snapshot: snapshot, ResolveTargets: n.resolver, Runner: n.runner, Options: n.f.o, StateDir: n.f.state, Now: func() time.Time { return n.f.o.Now }, LifecycleTimeout: 30 * time.Millisecond})
			if e != nil || len(result.Results) != 1 {
				t.Fatal(result, e)
			}
			got := result.Results[0]
			if lifecycle.quits != 1 {
				t.Fatal("exit not bound", lifecycle.quits)
			}
			if scenario == "success" || scenario == "reopen_failed" {
				expected := "reopened"
				if scenario == "reopen_failed" {
					expected = "failed"
				}
				if got.Status != "updated" || got.ApplicationLifecycle[0].ReopenStatus != expected || lifecycle.opens != 1 || !d.detached {
					t.Fatal(got, lifecycle.opens)
				}
			} else {
				if got.Status == "updated" || got.Changed || lifecycle.opens != 0 {
					t.Fatal(got, lifecycle.opens)
				}
				if scenario != "detach" && d.attached {
					t.Fatal("image mounted before lifecycle proof")
				}
			}
		})
	}
}

func TestApplicationDMGDispatchPreservesZIPComments(t *testing.T) {
	data := appZIP(t, appZipEntry{"One.app/content", "original ZIP content", 0644})
	// A valid ZIP comment can contain a disk-image marker at the same offset.
	// ZIP plans must continue through the ZIP parser, not a magic-byte heuristic.
	binary.LittleEndian.PutUint16(data[len(data)-2:], 512)
	data = append(data, []byte("koly"+strings.Repeat("x", 508))...)
	app, retained, e := stageApplicationArchive(context.Background(), ExecRunner{}, data, t.TempDir(), ApplicationUpdatePreview{})
	if e != nil || retained {
		t.Fatal(app, retained, e)
	}
	b, e := os.ReadFile(filepath.Join(app, "content"))
	if e != nil || string(b) != "original ZIP content" {
		t.Fatal(string(b), e)
	}
}

func TestApplicationDMGCopyCancellationAfterLastFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "App.app")
	if e := os.Mkdir(root, 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "last"), []byte("data"), 0644); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := runFunc(func(ctx context.Context, c Command) ([]byte, error) {
		b, e := (ExecRunner{}).Run(ctx, c)
		cancel()
		return b, e
	})
	if e := copyApplicationDMG(ctx, runner, root, filepath.Join(t.TempDir(), "App.app")); !errors.Is(e, context.Canceled) {
		t.Fatal("last-file cancellation lost", e)
	}
}
