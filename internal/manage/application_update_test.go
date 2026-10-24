package manage

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
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

type appTransport func(*http.Request) (*http.Response, error)

func (f appTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type appZipEntry struct {
	name, body string
	mode       os.FileMode
}

func appZIP(t *testing.T, entries ...appZipEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, entry := range entries {
		h := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		h.SetMode(entry.mode)
		w, e := z.CreateHeader(h)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write([]byte(entry.body)); e != nil {
			t.Fatal(e)
		}
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func appMachO() string {
	b := make([]byte, 32)
	binary.LittleEndian.PutUint32(b, 0xfeedfacf)
	binary.LittleEndian.PutUint32(b[4:], 0x100000c)
	binary.LittleEndian.PutUint32(b[12:], 2)
	return string(b)
}

type nativeFixture struct {
	privateKey        ed25519.PrivateKey
	payloadTeam       string
	f                 *fixture
	i                 audit.Item
	info              applicationInfo
	data              []byte
	feed              string
	runner            Runner
	download          func()
	signatureFailure  bool
	gatekeeperFailure bool
	running           bool
}

func nativeSetup(t *testing.T) *nativeFixture {
	f := setup(t)
	n := &nativeFixture{f: f}
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	n.privateKey = priv
	n.info = applicationInfo{ID: "com.example.Native", Version: "1.0", Build: "10", Executable: "Native", Minimum: "13.0", Feed: "https://vendor.example/appcast.xml", Key: base64.StdEncoding.EncodeToString(pub)}
	path := filepath.Join(f.home, "Applications/Native.app")
	b, _ := json.Marshal(n.info)
	f.write(filepath.Join(path, "Contents/Info.plist"), string(b), 0644)
	f.write(filepath.Join(path, "Contents/MacOS/Native"), appMachO(), 0755)
	n.i = audit.Item{ID: "native", Kind: "application", Name: "Native", Owner: "standalone", Source: "sparkle", Channel: "default", Identity: n.info.ID, Version: n.info.Version, Build: n.info.Build, Path: path, RealPath: path, ActivityKnown: true, Architecture: []string{"arm64"}, Health: audit.Result{Status: "ok"}}
	f.r.Architecture = "arm64"
	f.r.Items = []audit.Item{n.i}
	next := n.info
	next.Version = "2.0"
	next.Build = "20"
	b, _ = json.Marshal(next)
	n.data = appZIP(t, appZipEntry{"Native.app/Contents/Info.plist", string(b), 0644}, appZipEntry{"Native.app/Contents/MacOS/Native", appMachO(), 0755})
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, n.data))
	n.feed = fmt.Sprintf(`<rss xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle"><channel><item><sparkle:version>20</sparkle:version><sparkle:shortVersionString>2.0</sparkle:shortVersionString><enclosure url="https://vendor.example/Native.zip" length="%d" sparkle:edSignature="%s"/></item></channel></rss>`, len(n.data), sig)
	f.o.ApplicationHTTPClient = &http.Client{Transport: appTransport(func(r *http.Request) (*http.Response, error) {
		b := []byte(n.feed)
		if strings.HasSuffix(r.URL.Path, ".zip") {
			if n.download != nil {
				n.download()
			}
			b = n.data
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), ContentLength: int64(len(b))}, nil
	})}
	n.runner = runFunc(func(_ context.Context, c Command) ([]byte, error) {
		switch c.Path {
		case "/bin/ps":
			if n.running {
				return []byte(path + "/Contents/MacOS/Native\n"), nil
			}
			return []byte("/usr/bin/other\n"), nil
		case "/usr/bin/sw_vers":
			return []byte("14.0\n"), nil
		case "/usr/bin/plutil":
			return os.ReadFile(c.Args[len(c.Args)-1])
		case "/usr/bin/codesign":
			if c.Args[0] == "-d" {
				if n.payloadTeam != "" && strings.Contains(c.Args[len(c.Args)-1], "/stage/") {
					return []byte("certificate leaf[subject.OU] = " + n.payloadTeam), nil
				}
				return []byte(`designated => identifier "com.example.Native" and anchor apple generic and certificate leaf[subject.OU] = ABCDE12345`), nil
			}
			if !strings.Contains(strings.Join(c.Args, " "), "--all-architectures -R =anchor apple generic") {
				t.Fatal("missing identity verification", c)
			}
			if n.signatureFailure {
				return nil, errors.New("controlled signature rejection")
			}
			return nil, nil
		case "/usr/sbin/spctl":
			if n.gatekeeperFailure {
				return nil, errors.New("controlled Gatekeeper rejection")
			}
			return nil, nil
		default:
			t.Fatal("unexpected native command", c)
		}
		return nil, nil
	})
	return n
}
func (n *nativeFixture) report(ctx context.Context) (audit.Report, error) {
	r := n.f.r
	i := n.i
	info, e := readApplicationInfo(ctx, n.runner, i.Path)
	if e != nil {
		return r, e
	}
	i.Version, i.Build = info.Version, info.Build
	r.Items = []audit.Item{i}
	return r, nil
}
func (n *nativeFixture) resolver(ctx context.Context, r audit.Report, s Selection) (map[string]ResolvedTarget, error) {
	return ResolveApplicationTargets(ctx, r, s, n.f.o, n.runner)
}
func (n *nativeFixture) plan() Plan {
	n.f.t.Helper()
	s := Selection{Action: "update", IDs: []string{n.i.ID}}
	targets, e := n.resolver(context.Background(), n.f.r, s)
	if e != nil {
		n.f.t.Fatal(e)
	}
	n.f.o.ResolvedTargets = targets
	p := n.f.plan(s)
	if p.Items[0].Status != "ready" {
		n.f.t.Fatal(p.Items)
	}
	return p
}
func (n *nativeFixture) apply(p Plan) (ApplyResult, error) {
	return Apply(context.Background(), p, p.PlanID, Engine{Snapshot: n.report, ResolveTargets: n.resolver, Runner: n.runner, Options: n.f.o, StateDir: n.f.state, Now: func() time.Time { return n.f.o.Now }})
}

func TestNativeApplicationUpdateDispatchRetainsBackup(t *testing.T) {
	n := nativeSetup(t)
	p := n.plan()
	result, e := n.apply(p)
	if e != nil || len(result.Results) != 1 || result.Results[0].Status != "updated" || !result.Results[0].Changed || !result.Results[0].CommandSucceeded {
		t.Fatalf("%#v %v", result, e)
	}
	backup := result.Results[0].BackupPath
	info, e := readApplicationInfo(context.Background(), n.runner, backup)
	if e != nil || info.Version != "1.0" {
		t.Fatal("original backup unavailable", info, e)
	}
	if _, e = n.apply(p); e == nil {
		t.Fatal("replay accepted")
	}
}

func TestNativeApplicationRejectsDriftAndInvalidSignatures(t *testing.T) {
	for _, scenario := range []string{"version", "source", "local-edit", "signature", "preferences"} {
		t.Run(scenario, func(t *testing.T) {
			n := nativeSetup(t)
			p := n.plan()
			switch scenario {
			case "version":
				info := n.info
				info.Version = "1.1"
				b, _ := json.Marshal(info)
				n.f.write(filepath.Join(n.i.Path, "Contents/Info.plist"), string(b), 0644)
			case "source":
				n.feed = strings.Replace(n.feed, "Native.zip", "Other.zip", 1)
			case "local-edit":
				n.f.write(filepath.Join(n.i.Path, "local-user-file"), "preserve", 0644)
			case "signature":
				n.signatureFailure = true
			case "preferences":
				n.f.write(filepath.Join(n.f.home, "Library/Preferences", n.i.Identity+".plist"), `{"SUSkippedVersion":"20"}`, 0600)
			}
			if result, e := n.apply(p); e == nil {
				t.Fatal("drift executed", result)
			}
			if _, e := os.Stat(n.i.Path); e != nil {
				t.Fatal("original missing", e)
			}
		})
	}
}

func TestNativeApplicationRejectsChangesDuringDownload(t *testing.T) {
	for _, scenario := range []string{"corrupt", "running", "content", "gatekeeper", "source"} {
		t.Run(scenario, func(t *testing.T) {
			n := nativeSetup(t)
			p := n.plan()
			n.download = func() {
				switch scenario {
				case "corrupt":
					n.data[10] ^= 1
				case "running":
					n.running = true
				case "content":
					n.f.write(filepath.Join(n.i.Path, "local-edit"), "preserve", 0644)
				case "gatekeeper":
					n.gatekeeperFailure = true
				case "source":
					n.feed = strings.Replace(n.feed, "Native.zip", "Other.zip", 1)
				}
			}
			result, e := n.apply(p)
			if e != nil || result.Results[0].Status != "failed" || result.Results[0].Changed {
				t.Fatal(result, e)
			}
			info, e := readApplicationInfo(context.Background(), n.runner, n.i.Path)
			if e != nil || info.Version != "1.0" {
				t.Fatal("original replaced", info, e)
			}
		})
	}
}

func TestApplicationZIPRejectsEscapeAndConflicts(t *testing.T) {
	for name, entries := range map[string][]appZipEntry{
		"traversal":     {{"../escape", "x", 0644}},
		"absolute":      {{"/escape", "x", 0644}},
		"two-apps":      {{"One.app/a", "x", 0644}, {"Two.app/a", "x", 0644}},
		"case-alias":    {{"One.app/a", "x", 0644}, {"One.app/A", "x", 0644}},
		"escaping-link": {{"One.app/Contents/link", "../../../escape", os.ModeSymlink | 0777}},
		"link-parent":   {{"One.app/link", "inside", os.ModeSymlink | 0777}, {"One.app/link/file", "x", 0644}},
		"link-ancestor": {{"One.app/sub", ".", os.ModeSymlink | 0777}, {"One.app/sub/a", "..", os.ModeSymlink | 0777}, {"One.app/sub/a/outside/new", "x", os.ModeSymlink | 0777}},
		"chain-cycle":   {{"One.app/a", "b", os.ModeSymlink | 0777}, {"One.app/b", "a", os.ModeSymlink | 0777}},
		"special":       {{"One.app/a", "x", os.ModeNamedPipe | 0600}},
		"root-link":     {{"One.app", "elsewhere", os.ModeSymlink | 0777}},
	} {
		t.Run(name, func(t *testing.T) {
			if p, e := extractApplicationZIP(context.Background(), appZIP(t, entries...), t.TempDir()); e == nil {
				t.Fatal("accepted", p)
			}
		})
	}
}

func TestApplicationZIPPreservesContainedFrameworkLinks(t *testing.T) {
	p, e := extractApplicationZIP(context.Background(), appZIP(t, appZipEntry{"One.app/Contents/Frameworks/Thing.framework/Versions/A/Thing", "binary", 0755}, appZipEntry{"One.app/Contents/Frameworks/Thing.framework/Versions/Current", "A", os.ModeSymlink | 0777}, appZipEntry{"One.app/Contents/Frameworks/Thing.framework/Thing", "Versions/Current/Thing", os.ModeSymlink | 0777}), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(filepath.Join(p, "Contents/Frameworks/Thing.framework/Thing")); e != nil || string(b) != "binary" {
		t.Fatal(string(b), e)
	}
}

func TestApplicationReplacementRestoresOnSwapAndVerificationFailure(t *testing.T) {
	for _, scenario := range []string{"swap", "verify", "restore-failure", "concurrent-content"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			old, stage, backup := filepath.Join(f.home, "Old.app"), filepath.Join(f.home, "Stage.app"), filepath.Join(f.home, "Backup.app")
			f.write(filepath.Join(old, "content"), "original", 0644)
			f.write(filepath.Join(stage, "content"), "new", 0644)
			oldHash, _ := applicationTreeHash(context.Background(), old)
			newHash, _ := applicationTreeHash(context.Background(), stage)
			calls := 0
			exchange := func(a, b string) error {
				calls++
				if scenario == "swap" || calls == 2 && scenario == "restore-failure" {
					return errors.New("injected swap failure")
				}
				return exchangeApplications(a, b)
			}
			verify := func(string) error {
				if scenario == "concurrent-content" {
					f.write(filepath.Join(old, "user-file"), "preserve", 0644)
				}
				return errors.New("injected readback failure")
			}
			saved, changed, e := replaceApplication(context.Background(), old, stage, backup, oldHash, newHash, verify, exchange)
			if e == nil {
				t.Fatal("failure reported success")
			}
			if scenario == "restore-failure" || scenario == "concurrent-content" {
				if saved != backup || !changed {
					t.Fatal(saved, changed, e)
				}
				if b, e := os.ReadFile(filepath.Join(backup, "content")); e != nil || string(b) != "original" {
					t.Fatal("backup lost")
				}
				if scenario == "concurrent-content" {
					if b, e := os.ReadFile(filepath.Join(old, "user-file")); e != nil || string(b) != "preserve" {
						t.Fatal("concurrent content lost")
					}
				}
			} else {
				if changed {
					t.Fatal(saved, changed, e)
				}
				if b, e := os.ReadFile(filepath.Join(old, "content")); e != nil || string(b) != "original" {
					t.Fatal("original not restored")
				}
				if scenario == "verify" {
					if saved != backup || !strings.HasSuffix(e.Error(), "_original_restored") {
						t.Fatal(saved, e)
					}
					if b, e := os.ReadFile(filepath.Join(backup, "content")); e != nil || string(b) != "new" {
						t.Fatal("failed candidate lost")
					}
				}
			}
		})
	}
}

func TestNativeApplicationRejectsMismatchedPayload(t *testing.T) {
	for _, scenario := range []string{"bundle", "version", "build", "feed", "key", "minimum", "architecture", "team"} {
		t.Run(scenario, func(t *testing.T) {
			n := nativeSetup(t)
			next := n.info
			next.Version = "2.0"
			next.Build = "20"
			binaryData := appMachO()
			switch scenario {
			case "bundle":
				next.ID = "com.example.Other"
			case "version":
				next.Version = "3.0"
			case "build":
				next.Build = "21"
			case "feed":
				next.Feed = "https://other.example/feed.xml"
			case "key":
				next.Key = "changed"
			case "minimum":
				next.Minimum = "99"
			case "architecture":
				binaryData = "not a Mach-O"
			case "team":
				n.payloadTeam = "ZZZZZ99999"
			}
			b, _ := json.Marshal(next)
			n.data = appZIP(t, appZipEntry{"Native.app/Contents/Info.plist", string(b), 0644}, appZipEntry{"Native.app/Contents/MacOS/Native", binaryData, 0755})
			sig := base64.StdEncoding.EncodeToString(ed25519.Sign(n.privateKey, n.data))
			n.feed = fmt.Sprintf(`<rss xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle"><channel><item><sparkle:version>20</sparkle:version><sparkle:shortVersionString>2.0</sparkle:shortVersionString><enclosure url="https://vendor.example/Native.zip" length="%d" sparkle:edSignature="%s"/></item></channel></rss>`, len(n.data), sig)
			p := n.plan()
			result, e := n.apply(p)
			if e != nil || result.Results[0].Status != "failed" || result.Results[0].Changed {
				t.Fatal(result, e)
			}
			info, e := readApplicationInfo(context.Background(), n.runner, n.i.Path)
			if e != nil || info.Version != "1.0" {
				t.Fatal("original lost", info, e)
			}
		})
	}
}

func TestNativeApplicationBuildOnlyUpdate(t *testing.T) {
	n := nativeSetup(t)
	// Match the displayed version while advancing the signed bundle build.
	info := n.info
	info.Version = "2.0"
	b, _ := json.Marshal(info)
	n.f.write(filepath.Join(n.i.Path, "Contents/Info.plist"), string(b), 0644)
	n.i.Version = "2.0"
	n.f.r.Items[0] = n.i
	p := n.plan()
	result, e := n.apply(p)
	if e != nil || result.Results[0].Status != "updated" || !result.Results[0].Changed {
		t.Fatal(result, e)
	}
}
