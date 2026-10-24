//go:build darwin

package manage

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Only signature authority is synthetic in this acceptance fixture. AppKit
// inspection, the detached worker process, normal exit, directory exchange,
// plist/Mach-O readback and exact-path reopening all run on macOS.
func selfNativeFixtureRunner() Runner {
	return runFunc(func(ctx context.Context, c Command) ([]byte, error) {
		switch c.Path {
		case "/usr/bin/codesign":
			if c.Args[0] == "-d" {
				return []byte("certificate leaf[subject.OU] = ABCDE12345"), nil
			}
			if !strings.Contains(strings.Join(c.Args, " "), `identifier "com.keepupdated.desktop" and certificate leaf[subject.OU] = "ABCDE12345"`) {
				return nil, errors.New("fixture unexpected signature requirement")
			}
			return nil, nil
		case "/usr/sbin/spctl":
			return nil, nil
		default:
			return (ExecRunner{}).Run(ctx, c)
		}
	})
}
func TestSelfUpdateDetachedFixtureWorker(t *testing.T) {
	path := os.Getenv("KEEPUPDATED_SELF_FIXTURE_PLAN")
	if path == "" {
		return
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var p SelfUpdatePlan
	if json.Unmarshal(b, &p) != nil {
		t.Fatal("bad fixture plan")
	}
	// This entry exists only in the Go test executable, never the product CLI.
	certificate, e := os.ReadFile(filepath.Join(p.Directory, "fixture-cert.der"))
	if e != nil {
		t.Fatal(e)
	}
	cert, e := x509.ParseCertificate(certificate)
	if e != nil {
		t.Fatal(e)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	u := productionSelfUpdater()
	u.api = os.Getenv("KEEPUPDATED_SELF_FIXTURE_API")
	u.runner = selfNativeFixtureRunner()
	u.team = p.Check.TeamID
	u.client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Timeout: time.Minute}
	if e = u.finish(context.Background(), p); e != nil {
		t.Fatal(e)
	}
}
func TestSelfUpdateRealIsolatedHandoff(t *testing.T) {
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(root, "fixture")
	if b, e := exec.CommandContext(ctx, "/usr/bin/xcrun", "swiftc", "-module-cache-path", filepath.Join(root, "swift-cache"), "testdata/LifecycleFixture.swift", "-o", binary).CombinedOutput(); e != nil {
		t.Fatalf("compile: %v %s", e, b)
	}
	executable, e := os.ReadFile(binary)
	if e != nil {
		t.Fatal(e)
	}
	oldControl, newControl := filepath.Join(root, "old-control"), filepath.Join(root, "new-control")
	for _, c := range []string{oldControl, newControl} {
		if e = os.Mkdir(c, 0700); e != nil {
			t.Fatal(e)
		}
	}
	plist := func(version, build, control string) string {
		return fmt.Sprintf(`<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.keepupdated.desktop</string><key>CFBundleName</key><string>Self Update Fixture</string><key>CFBundleExecutable</key><string>KeepUpdatedDesktop</string><key>CFBundlePackageType</key><string>APPL</string><key>CFBundleShortVersionString</key><string>%s</string><key>CFBundleVersion</key><string>%s</string><key>LSMinimumSystemVersion</key><string>13.0</string><key>KeepUpdatedFixtureControl</key><string>%s</string><key>LSUIElement</key><true/></dict></plist>`, version, build, control)
	}
	app := filepath.Join(root, "KeepUpdated.app")
	for _, d := range []string{"Contents/MacOS", "Contents/Resources"} {
		if e = os.MkdirAll(filepath.Join(app, d), 0700); e != nil {
			t.Fatal(e)
		}
	}
	write := func(path string, data []byte, mode os.FileMode) {
		t.Helper()
		if e := os.WriteFile(path, data, mode); e != nil {
			t.Fatal(e)
		}
	}
	write(filepath.Join(app, "Contents/Info.plist"), []byte(plist("1.0.0", "10", oldControl)), 0600)
	write(filepath.Join(app, "Contents/MacOS/KeepUpdatedDesktop"), executable, 0700)
	testBinary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	testBytes, e := os.ReadFile(testBinary)
	if e != nil {
		t.Fatal(e)
	}
	write(filepath.Join(app, "Contents/Resources/keepupdated"), testBytes, 0700)
	payload := appZIP(t, appZipEntry{"KeepUpdated.app/Contents/Info.plist", plist("2.0.0", "20", newControl), 0600}, appZipEntry{"KeepUpdated.app/Contents/MacOS/KeepUpdatedDesktop", string(executable), 0700}, appZipEntry{"KeepUpdated.app/Contents/Resources/keepupdated", "synthetic candidate CLI", 0700})
	f := &selfFixture{t: t, archive: payload}
	f.release = SelfUpdateRelease{ID: 1, Tag: "v2.0.0", URL: "https://github.com/" + selfRepository + "/releases/tag/v2.0.0", Published: "2026-01-01T00:00:00Z", Notes: "Isolated fixture"}
	f.setAssets()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			_ = json.NewEncoder(w).Encode(f.release)
		case strings.HasSuffix(r.URL.Path, ".zip"):
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "_SHA256SUMS"):
			_, _ = w.Write(f.checksums)
		default:
			fmt.Fprintf(w, `{"id":123,"full_name":%q,"private":false}`, selfRepository)
		}
	}))
	defer server.Close()
	t.Cleanup(func() {
		for _, c := range []string{oldControl, newControl} {
			_ = os.WriteFile(filepath.Join(c, "stop"), nil, 0600)
		}
		for n := 0; n < 60; n++ {
			live, e := inspectApplication(context.Background(), ExecRunner{}, app)
			if e == nil && len(live) == 0 && applicationProcessesClosed(context.Background(), ExecRunner{}, app) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Error("fixture failed to stop normally")
	})
	if b, e := exec.CommandContext(ctx, "/usr/bin/open", "-n", "-a", app).CombinedOutput(); e != nil {
		t.Fatalf("launch: %v %s", e, b)
	}
	var live []ApplicationInstance
	for n := 0; n < 60; n++ {
		live, e = inspectApplication(ctx, ExecRunner{}, app)
		if e == nil && len(live) == 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(live) != 1 {
		t.Fatal("fixture did not register", live, e)
	}
	u := productionSelfUpdater()
	u.api = server.URL + "/repos/" + selfRepository
	u.runner = selfNativeFixtureRunner()
	u.executable = func() (string, error) { return filepath.Join(app, "Contents/Resources/keepupdated"), nil }
	u.parentPID = func() int { return live[0].PID }
	endpoint, _ := url.Parse(server.URL)
	transport := server.Client().Transport
	u.client = &http.Client{Transport: appTransport(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		address := *r.URL
		address.Host, address.Scheme = endpoint.Host, endpoint.Scheme
		copy.URL = &address
		return transport.RoundTrip(copy)
	})}
	check, e := u.check(ctx, app)
	if e != nil || !check.CanInstall {
		t.Fatal(check, e)
	}
	p, e := u.prepare(ctx, check, live[0].PID)
	if e != nil {
		t.Fatal(e)
	}
	write(filepath.Join(p.Directory, "fixture-cert.der"), server.Certificate().Raw, 0600)
	child := exec.CommandContext(ctx, filepath.Join(p.Directory, "worker"), "-test.run=^TestSelfUpdateDetachedFixtureWorker$")
	child.Env = append(os.Environ(), "KEEPUPDATED_SELF_FIXTURE_PLAN="+filepath.Join(p.Directory, "plan.json"), "KEEPUPDATED_SELF_FIXTURE_API="+u.api)
	var output strings.Builder
	child.Stdout = &output
	child.Stderr = &output
	if e = child.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	for {
		if _, e = os.Stat(filepath.Join(p.Directory, "ready.json")); e == nil {
			break
		}
		select {
		case e := <-done:
			t.Fatalf("worker failed before ready: %v %s", e, output.String())
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	// The original fixture acknowledges ready before its own normal-exit control.
	if e = writeSelfJSON(filepath.Join(p.Directory, "exit-approved.json"), map[string]string{"id": p.ID}); e != nil {
		t.Fatal(e)
	}
	write(filepath.Join(oldControl, "stop"), nil, 0600)
	if e = <-done; e != nil {
		t.Fatalf("detached worker: %v %s", e, output.String())
	}
	var state SelfUpdateState
	b, e := os.ReadFile(filepath.Join(p.Directory, "status.json"))
	if e != nil || json.Unmarshal(b, &state) != nil || state.Status != "reopened" {
		t.Fatal(string(b), e)
	}
	reopened, e := inspectApplication(ctx, ExecRunner{}, app)
	if e != nil || len(reopened) != 1 || reopened[0].PID == live[0].PID || reopened[0].LaunchedAt == live[0].LaunchedAt {
		t.Fatal("new process not proven", reopened, e)
	}
	old, e := applicationTreeHash(ctx, state.Backup)
	if e != nil || old != p.OriginalSHA256 {
		t.Fatal("original backup mismatch", e)
	}
	current, e := applicationTreeHash(ctx, app)
	if e != nil || current != p.CandidateSHA256 {
		t.Fatal("candidate readback mismatch", e)
	}
}
