package manage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type selfFixture struct {
	onRelease                           func()
	reopenFailure                       bool
	postVerifyChange                    bool
	t                                   *testing.T
	u                                   selfUpdater
	app                                 string
	release                             SelfUpdateRelease
	archive, checksums                  []byte
	repoStatus, latestStatus            int
	listing                             string
	team, payloadTeam                   string
	signatureFailure, gatekeeperFailure bool
	running                             atomic.Bool
	reopened                            atomic.Bool
	instance                            ApplicationInstance
	onDownload                          func()
}

func selfSetup(t *testing.T) *selfFixture {
	t.Helper()
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	f := &selfFixture{t: t, app: filepath.Join(root, "KeepUpdated.app"), repoStatus: 200, latestStatus: 200, listing: "[]", team: "ABCDE12345"}
	f.running.Store(true)
	f.instance = ApplicationInstance{PID: 123, LaunchedAt: "1234567.5", Path: f.app, Identity: selfBundleID, Executable: filepath.Join(f.app, "Contents/MacOS/KeepUpdatedDesktop")}
	info := applicationInfo{ID: selfBundleID, Version: "1.0.0", Build: "10", Executable: "KeepUpdatedDesktop", Minimum: "13.0"}
	write := func(path string, b []byte, mode os.FileMode) {
		t.Helper()
		if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(path, b, mode); e != nil {
			t.Fatal(e)
		}
	}
	b, _ := json.Marshal(info)
	write(filepath.Join(f.app, "Contents/Info.plist"), b, 0600)
	write(filepath.Join(f.app, "Contents/MacOS/KeepUpdatedDesktop"), []byte(appMachO()), 0700)
	write(filepath.Join(f.app, "Contents/Resources/keepupdated"), []byte("authenticated installed fixture worker"), 0700)
	info.Version, info.Build = "2.0.0", "20"
	b, _ = json.Marshal(info)
	f.archive = appZIP(t, appZipEntry{"KeepUpdated.app/Contents/Info.plist", string(b), 0600}, appZipEntry{"KeepUpdated.app/Contents/MacOS/KeepUpdatedDesktop", appMachO(), 0700}, appZipEntry{"KeepUpdated.app/Contents/Resources/keepupdated", "candidate worker", 0700})
	f.release = SelfUpdateRelease{ID: 1, Tag: "v2.0.0", URL: "https://github.com/" + selfRepository + "/releases/tag/v2.0.0", Published: "2026-01-01T00:00:00Z", Notes: "Synthetic release notes"}
	f.setAssets()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("public check sent authentication")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			if f.onRelease != nil {
				f.onRelease()
			}
			w.WriteHeader(f.latestStatus)
			_ = json.NewEncoder(w).Encode(f.release)
		case strings.HasSuffix(r.URL.Path, "/releases"):
			_, _ = io.WriteString(w, f.listing)
		case strings.HasSuffix(r.URL.Path, ".zip"):
			if f.onDownload != nil {
				f.onDownload()
			}
			_, _ = w.Write(f.archive)
		case strings.HasSuffix(r.URL.Path, "_SHA256SUMS"):
			_, _ = w.Write(f.checksums)
		default:
			w.WriteHeader(f.repoStatus)
			fmt.Fprintf(w, `{"id":123,"full_name":%q,"private":false}`, selfRepository)
		}
	}))
	t.Cleanup(server.Close)
	base, _ := url.Parse(server.URL)
	transport := server.Client().Transport
	f.u = selfUpdater{api: server.URL + "/repos/" + selfRepository, team: f.team, client: &http.Client{Transport: appTransport(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		address := *r.URL
		address.Scheme, address.Host = base.Scheme, base.Host
		copy.URL = &address
		return transport.RoundTrip(copy)
	})}, executable: func() (string, error) { return filepath.Join(f.app, "Contents/Resources/keepupdated"), nil }, parentPID: func() int { return f.instance.PID }}
	f.u.runner = runFunc(func(ctx context.Context, c Command) ([]byte, error) {
		switch c.Path {
		case "/usr/bin/plutil":
			path := c.Args[len(c.Args)-1]
			b, e := os.ReadFile(path)
			if f.postVerifyChange && path == filepath.Join(f.app, "Contents/Info.plist") && bytes.Contains(b, []byte(`"CFBundleShortVersionString":"2.0.0"`)) {
				_ = os.WriteFile(filepath.Join(f.app, "concurrent-edit"), []byte("external edit"), 0600)
				return nil, errors.New("controlled post-exchange verification failure")
			}
			return b, e
		case "/usr/bin/sw_vers":
			return []byte("14.0"), nil
		case "/usr/bin/codesign":
			if c.Args[0] == "-d" {
				team := f.team
				if strings.Contains(c.Args[len(c.Args)-1], "/stage/") && f.payloadTeam != "" {
					team = f.payloadTeam
				}
				if team == "" {
					return []byte("adhoc"), nil
				}
				return []byte("certificate leaf[subject.OU] = " + team), nil
			}
			if f.signatureFailure {
				return nil, errors.New("fixture signature invalid")
			}
			return nil, nil
		case "/usr/sbin/spctl":
			if f.gatekeeperFailure {
				return nil, errors.New("fixture Gatekeeper rejection")
			}
			return nil, nil
		case "/bin/ps":
			if f.running.Load() {
				return []byte(f.instance.Executable + "\n"), nil
			}
			return []byte("/usr/bin/fixture-worker\n"), nil
		case "/usr/bin/osascript":
			live := []ApplicationInstance{}
			if f.running.Load() {
				i := f.instance
				if f.reopened.Load() {
					i.PID++
					i.LaunchedAt = "9999999"
				}
				live = append(live, i)
			}
			return json.Marshal(map[string]any{"instances": live})
		case "/usr/bin/open":
			if f.reopenFailure {
				return nil, errors.New("controlled reopen failure")
			}
			f.running.Store(true)
			f.reopened.Store(true)
			return nil, nil
		}
		t.Fatalf("unexpected command %v", c)
		return nil, nil
	})
	return f
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (f *selfFixture) setAssets() {
	name := "KeepUpdated_2.0.0_darwin_arm64.zip"
	f.checksums = []byte(digest(f.archive) + "  " + name + "\n")
	f.release.Assets = []SelfUpdateAsset{{ID: 2, Name: name, URL: "https://github.com/" + selfRepository + "/releases/download/v2.0.0/" + name, Size: int64(len(f.archive)), Digest: "sha256:" + digest(f.archive), State: "uploaded"}, {ID: 3, Name: "KeepUpdated_2.0.0_SHA256SUMS", URL: "https://github.com/" + selfRepository + "/releases/download/v2.0.0/KeepUpdated_2.0.0_SHA256SUMS", Size: int64(len(f.checksums)), Digest: "sha256:" + digest(f.checksums), State: "uploaded"}}
}
func (f *selfFixture) check() SelfUpdateCheck {
	f.t.Helper()
	c, e := f.u.check(context.Background(), f.app)
	if e != nil || !c.CanInstall {
		f.t.Fatal(c, e)
	}
	return c
}
func (f *selfFixture) prepare() SelfUpdatePlan {
	f.t.Helper()
	p, e := f.u.prepare(context.Background(), f.check(), f.instance.PID)
	if e != nil {
		f.t.Fatal(e)
	}
	return p
}

func TestSelfUpdatePublicReleaseStates(t *testing.T) {
	for _, scenario := range []string{"available", "not-published", "repository-hidden", "rate-limit", "draft", "prerelease", "ambiguous-404", "adhoc-current", "wrong-parent", "detached-cli", "wrong-asset", "duplicate-asset", "no-digest", "same-version-adhoc", "same-version-signed", "older-release", "invalid-version"} {
		t.Run(scenario, func(t *testing.T) {
			f := selfSetup(t)
			want := "failed"
			install := false
			switch scenario {
			case "available":
				want = "available"
				install = true
			case "not-published":
				f.latestStatus = 404
				want = "not_published"
			case "repository-hidden":
				f.repoStatus = 404
			case "rate-limit":
				f.latestStatus = 403
			case "draft":
				f.release.Draft = true
			case "prerelease":
				f.release.Prerelease = true
			case "ambiguous-404":
				f.latestStatus = 404
				f.listing = `[{"prerelease":true}]`
			case "adhoc-current":
				f.team = ""
				want = "available"
			case "wrong-parent":
				f.u.parentPID = func() int { return 456 }
				want = "available"
			case "detached-cli":
				f.u.executable = func() (string, error) { return "/tmp/unbound", nil }
				want = "available"
			case "wrong-asset":
				f.release.Assets[0].URL = "https://example.com/payload.zip"
				want = "available"
			case "duplicate-asset":
				f.release.Assets = append(f.release.Assets, f.release.Assets[0])
				want = "available"
			case "no-digest":
				f.release.Assets[0].Digest = ""
				want = "available"
			case "older-release":
				f.release.Tag = "v0.9.0"
				f.release.URL = "https://github.com/" + selfRepository + "/releases/tag/v0.9.0"
				want = "newer_than_release"
			case "invalid-version":
				f.release.Tag = "v9999999999999999999999999999999.0.0"
				f.release.URL = "https://github.com/" + selfRepository + "/releases/tag/" + f.release.Tag
			case "same-version-adhoc", "same-version-signed":
				f.release.Tag = "v1.0.0"
				f.release.URL = "https://github.com/" + selfRepository + "/releases/tag/v1.0.0"
				if scenario == "same-version-adhoc" {
					f.team = ""
				} else {
					want = "up_to_date"
				}
			}
			c, e := f.u.check(context.Background(), f.app)
			if c.Evidence == nil || c.Evidence.ArchiveHash == "verified" || c.Evidence.ArchivePublisherSignature == "verified" || c.Evidence.MetadataFreshness != "unverified" || c.Evidence.InstallationReadback != "" || c.Evidence.Reopen != "" {
				t.Fatalf("check invented evidence: %+v", c.Evidence)
			}
			if install || want == "up_to_date" || want == "newer_than_release" {
				if c.Evidence.InstalledPayloadSignature != "verified" {
					t.Fatalf("missing payload proof: %+v", c.Evidence)
				}
			} else if c.Evidence.InstalledPayloadSignature == "verified" {
				t.Fatalf("untrusted app verified: %+v", c.Evidence)
			}
			if scenario == "not-published" && (c.Evidence.Repository != "verified" || c.Evidence.Release != "not_applicable") {
				t.Fatal(c.Evidence)
			}
			if scenario == "rate-limit" && (c.Evidence.Repository != "verified" || c.Evidence.Release != "unverified") {
				t.Fatal(c.Evidence)
			}
			if c.Status != want || c.CanInstall != install || (e != nil) != (want == "failed") {
				t.Fatal(c, e, want)
			}
		})
	}
}
func TestSelfUpdatePreparationRejectsChangedOrUntrustedInputs(t *testing.T) {
	for _, scenario := range []string{"digest", "checksum", "different-team", "signature", "gatekeeper", "release-drift", "original-drift", "wrong-pid", "wrong-version", "old-build"} {
		t.Run(scenario, func(t *testing.T) {
			f := selfSetup(t)
			c := f.check()
			switch scenario {
			case "digest":
				f.archive = append(f.archive, 0)
			case "checksum":
				f.checksums = []byte("incorrect")
				f.release.Assets[1].Size = int64(len(f.checksums))
				f.release.Assets[1].Digest = "sha256:" + digest(f.checksums)
				c = f.check()
			case "different-team":
				f.payloadTeam = "OTHER12345"
			case "signature":
				f.signatureFailure = true
			case "gatekeeper":
				f.gatekeeperFailure = true
			case "release-drift":
				f.onDownload = func() { f.release.ID++ }
			case "original-drift":
				f.onDownload = func() { _ = os.WriteFile(filepath.Join(f.app, "unsealed-edit"), []byte("edit"), 0600) }
			case "wrong-pid":
				f.instance.LaunchedAt = "changed"
			case "wrong-version", "old-build":
				info := applicationInfo{ID: selfBundleID, Version: "2.0.0", Build: "20", Executable: "KeepUpdatedDesktop"}
				if scenario == "wrong-version" {
					info.Version = "3.0.0"
				} else {
					info.Build = "9"
				}
				b, _ := json.Marshal(info)
				f.archive = appZIP(t, appZipEntry{"KeepUpdated.app/Contents/Info.plist", string(b), 0600}, appZipEntry{"KeepUpdated.app/Contents/MacOS/KeepUpdatedDesktop", appMachO(), 0700})
				f.setAssets()
				c = f.check()
			}
			pid := f.instance.PID
			if scenario == "wrong-pid" {
				pid++
			}
			p, e := f.u.prepare(context.Background(), c, pid)
			if p.Evidence != nil {
				if p.Evidence.InstallationReadback == "verified" || p.Evidence.Reopen == "verified" || p.Evidence.ArchivePublisherSignature == "verified" || p.Evidence.MetadataFreshness == "verified" {
					t.Fatal("failed prepare invented success", p.Evidence)
				}
				if scenario == "digest" && p.Evidence.ArchiveHash == "verified" {
					t.Fatal("bad bytes verified")
				}
				if scenario == "different-team" && p.Evidence.CandidatePayloadSignature != "unverified" {
					t.Fatal("wrong team verified", p.Evidence)
				}
			}
			if e == nil {
				t.Fatal("unverified update prepared", scenario)
			}
			info, e := readApplicationInfo(context.Background(), f.u.runner, f.app)
			if e != nil || info.Version != "1.0.0" {
				t.Fatal("installed app changed", info, e)
			}
		})
	}
}
func TestSelfUpdatePreparedPlanAndRecovery(t *testing.T) {
	f := selfSetup(t)
	c := f.check()
	p, e := f.u.prepare(context.Background(), c, f.instance.PID)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(c, p.Check) || p.Check.Evidence.ArchiveHash != "not_checked" {
		t.Fatal("prepare changed stable check")
	}
	if p.Evidence == nil || p.Evidence.ArchiveLength != "verified" || p.Evidence.ArchiveHash != "verified" || p.Evidence.CandidatePayloadSignature != "verified" || p.Evidence.ArchivePublisherSignature != "unverified" || p.Evidence.MetadataFreshness != "unverified" {
		t.Fatalf("wrong prepared evidence: %+v", p.Evidence)
	}
	var prepared SelfUpdateState
	data, e := os.ReadFile(filepath.Join(p.Directory, "status.json"))
	if e != nil || json.Unmarshal(data, &prepared) != nil || !reflect.DeepEqual(prepared.Evidence, p.Evidence) {
		t.Fatal("prepared state lost evidence", e)
	}
	got, e := ReadSelfUpdatePlan(filepath.Join(p.Directory, "plan.json"), p.ID)
	if e != nil || got.ID != p.ID {
		t.Fatal(e)
	}
	if _, e = ReadSelfUpdatePlan(filepath.Join(p.Directory, "plan.json"), "wrong"); e == nil {
		t.Fatal("wrong consent accepted")
	}
	done := make(chan error, 1)
	go func() { done <- f.u.finish(context.Background(), p) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, e := os.Stat(filepath.Join(p.Directory, "ready.json")); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// This fixture transition models the UI acknowledging ready before exiting.
	if e = writeSelfJSON(filepath.Join(p.Directory, "exit-approved.json"), map[string]string{"id": p.ID}); e != nil {
		t.Fatal(e)
	}
	f.running.Store(false)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	var state SelfUpdateState
	b, e := os.ReadFile(filepath.Join(p.Directory, "status.json"))
	if e != nil || json.Unmarshal(b, &state) != nil || state.Status != "reopened" {
		t.Fatal(string(b), e)
	}
	if state.Evidence == nil || state.Evidence.InstallationReadback != "verified" || state.Evidence.Reopen != "verified" || state.Evidence.ArchivePublisherSignature != "unverified" {
		t.Fatalf("wrong final evidence: %+v", state.Evidence)
	}
	old, e := applicationTreeHash(context.Background(), state.Backup)
	if e != nil || old != p.OriginalSHA256 {
		t.Fatal("backup not retained", e)
	}
	now, e := applicationTreeHash(context.Background(), f.app)
	if e != nil || now != p.CandidateSHA256 {
		t.Fatal("installed tree not verified", e)
	}
	if !f.reopened.Load() {
		t.Fatal("reopen never called")
	}
}
func TestSelfUpdateHandoffRejectsConcurrentChanges(t *testing.T) {
	for _, scenario := range []string{"content", "worker", "instance", "team", "expired", "lock", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			f := selfSetup(t)
			p := f.prepare()
			switch scenario {
			case "content":
				_ = os.WriteFile(filepath.Join(f.app, "edit"), []byte("new"), 0600)
			case "worker":
				_ = os.WriteFile(filepath.Join(p.Directory, "worker"), []byte("different"), 0700)
			case "instance":
				f.instance.LaunchedAt = "different"
			case "team":
				f.team = "OTHER12345"
			case "expired":
				p.Expires = time.Now().Add(-time.Minute)
				p.ID = selfPlanID(p)
			case "lock":
				lock, e := lockSelfUpdateTarget(f.app)
				if e != nil {
					t.Fatal(e)
				}
				defer lock.Close()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancel" {
				cancel()
			}
			if e := f.u.finish(ctx, p); e == nil {
				t.Fatal("handoff accepted changed input")
			}
			info, e := readApplicationInfo(context.Background(), f.u.runner, f.app)
			if e != nil || info.Version != "1.0.0" || f.reopened.Load() {
				t.Fatal("original changed", info, e)
			}
		})
	}
}
func TestSelfUpdateDownloadRejectsOffOriginRedirect(t *testing.T) {
	u := productionSelfUpdater()
	u.client = &http.Client{Transport: appTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://untrusted.example/file"}}, Body: io.NopCloser(bytes.NewReader(nil)), Request: r}, nil
	})}
	if _, _, e := u.get(context.Background(), "https://github.com/"+selfRepository+"/releases/download/v1.0.0/file", 100, true); e == nil {
		t.Fatal("untrusted redirect accepted")
	}
}

func TestSelfUpdateLostReadyResponseCannotInstallOnLaterExit(t *testing.T) {
	f := selfSetup(t)
	p := f.prepare()
	done := make(chan error, 1)
	go func() { done <- f.u.finish(context.Background(), p) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, e := os.Stat(filepath.Join(p.Directory, "ready.json")); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A duplicate worker cannot overwrite this live operation with a false terminal state.
	if e := f.u.finish(context.Background(), p); e == nil || e.Error() != "self_update_already_started" {
		t.Fatal(e)
	}
	var active SelfUpdateState
	b, _ := os.ReadFile(filepath.Join(p.Directory, "status.json"))
	_ = json.Unmarshal(b, &active)
	if active.Status != "waiting_for_exit" {
		t.Fatal("duplicate worker clobbered recovery", active)
	}
	// No exit-approved acknowledgment: a later unrelated quit is not consent delivery.
	f.running.Store(false)
	if e := <-done; e == nil || e.Error() != "self_update_exit_not_acknowledged" {
		t.Fatal(e)
	}
	hash, e := applicationTreeHash(context.Background(), f.app)
	if e != nil || hash != p.OriginalSHA256 || f.reopened.Load() {
		t.Fatal("unacknowledged handoff changed app", e)
	}
}

func TestSelfUpdateLateReadyCannotInstallAfterCallerTimeout(t *testing.T) {
	f := selfSetup(t)
	p := f.prepare()
	entered, release := make(chan struct{}), make(chan struct{})
	f.onRelease = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { done <- f.u.finish(context.Background(), p) }()
	<-entered
	timeout, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, e := waitSelfUpdateReady(timeout, p); e == nil {
		t.Fatal("expected readiness timeout")
	}
	close(release)
	// A normal exit after the failed handoff must not arm the old worker.
	f.running.Store(false)
	if e := <-done; e == nil || e.Error() != "self_update_exit_not_acknowledged" {
		t.Fatal(e)
	}
	hash, e := applicationTreeHash(context.Background(), f.app)
	if e != nil || hash != p.OriginalSHA256 || f.reopened.Load() {
		t.Fatal("late worker installed", e)
	}
}
func TestSelfUpdatePreservesInstalledAndUnknownOutcomes(t *testing.T) {
	for _, scenario := range []string{"reopen-failure", "post-swap-change"} {
		t.Run(scenario, func(t *testing.T) {
			f := selfSetup(t)
			p := f.prepare()
			f.reopenFailure = scenario == "reopen-failure"
			f.postVerifyChange = scenario == "post-swap-change"
			done := make(chan error, 1)
			go func() { done <- f.u.finish(context.Background(), p) }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, e := os.Stat(filepath.Join(p.Directory, "ready.json")); e == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("no ready")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if e := writeSelfJSON(filepath.Join(p.Directory, "exit-approved.json"), map[string]string{"id": p.ID}); e != nil {
				t.Fatal(e)
			}
			f.running.Store(false)
			if e := <-done; e == nil {
				t.Fatal("expected controlled failure")
			}
			var state SelfUpdateState
			b, e := os.ReadFile(filepath.Join(p.Directory, "status.json"))
			if e != nil || json.Unmarshal(b, &state) != nil {
				t.Fatal(e)
			}
			if !state.Changed {
				t.Fatal("lost mutation fact", state)
			}
			if state.Evidence == nil {
				t.Fatal("missing final evidence")
			}
			if scenario == "reopen-failure" {
				if state.Evidence.InstallationReadback != "verified" || state.Evidence.Reopen != "unverified" {
					t.Fatal(state.Evidence)
				}
				if state.Status != "installed" || state.Verification != "verified" || state.ReopenStatus != "failed" {
					t.Fatal(state)
				}
			} else if state.Status != "verification_unknown" || state.Verification != "unknown" {
				t.Fatal(state)
			}
			if scenario == "post-swap-change" && (state.Evidence.InstallationReadback != "unverified" || state.Evidence.Reopen == "verified") {
				t.Fatal(state.Evidence)
			}
			hash, e := applicationTreeHash(context.Background(), state.Backup)
			if e != nil || hash != p.OriginalSHA256 {
				t.Fatal("lost original backup", e)
			}
		})
	}
}
func TestSelfUpdateRejectsSymlinkedStagingWithIdenticalExternalApp(t *testing.T) {
	f := selfSetup(t)
	p := f.prepare()
	external := filepath.Join(filepath.Dir(f.app), "external-stage")
	if e := os.Rename(filepath.Join(p.Directory, "stage"), external); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(external, filepath.Join(p.Directory, "stage")); e != nil {
		t.Fatal(e)
	}
	// The bytes still match: inode and canonical-parent binding must reject it.
	hash, e := applicationTreeHash(context.Background(), p.Staged)
	if e != nil || hash != p.CandidateSHA256 {
		t.Fatal("fixture content differs", e)
	}
	if e = f.u.finish(context.Background(), p); e == nil || e.Error() != "self_update_staging_changed" {
		t.Fatal(e)
	}
	hash, e = applicationTreeHash(context.Background(), filepath.Join(external, "KeepUpdated.app"))
	if e != nil || hash != p.CandidateSHA256 {
		t.Fatal("external candidate moved or edited", e)
	}
	hash, e = applicationTreeHash(context.Background(), f.app)
	if e != nil || hash != p.OriginalSHA256 {
		t.Fatal("original changed", e)
	}
}

func TestSelfUpdateLegacyEvidenceAndPlanCompatibility(t *testing.T) {
	// Omitted evidence fields must preserve the original v1 encoding.
	if got := selfPlanID(SelfUpdatePlan{}); got != "adf3be75da3dc6ee4584b5c3b112025cbc0dd72b1c35785a90a53937f96fcb57" {
		t.Fatal("legacy v1 hash changed", got)
	}
	f := selfSetup(t)
	c := f.check()
	c.Evidence = nil
	p, e := f.u.prepare(context.Background(), c, f.instance.PID)
	if e != nil || p.Check.Evidence != nil {
		t.Fatal("old check no longer prepares", e)
	}
	p.Evidence = nil
	p.ID = selfPlanID(p)
	data, e := json.Marshal(p)
	if e != nil || bytes.Contains(data, []byte(`"evidence"`)) {
		t.Fatal("missing evidence must stay omitted", e)
	}
	var decoded SelfUpdatePlan
	if e = json.Unmarshal(data, &decoded); e != nil || decoded.ID != selfPlanID(decoded) {
		t.Fatal("legacy plan hash changed", e)
	}
	if e = writeSelfJSON(filepath.Join(p.Directory, "plan.json"), p); e != nil {
		t.Fatal(e)
	}
	if _, e = ReadSelfUpdatePlan(filepath.Join(p.Directory, "plan.json"), p.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.u.validatePrepared(context.Background(), decoded, true); e != nil {
		t.Fatal("valid legacy plan rejected", e)
	}
	decoded.Check.CanInstall = false
	decoded.ID = selfPlanID(decoded)
	if e = f.u.validatePrepared(context.Background(), decoded, true); e == nil {
		t.Fatal("missing evidence bypassed eligibility")
	}
	decoded = p
	decoded.Expires = time.Now().Add(-time.Minute)
	decoded.ID = selfPlanID(decoded)
	if e = f.u.validatePrepared(context.Background(), decoded, true); e == nil {
		t.Fatal("expired legacy plan accepted")
	}
	decoded = p
	decoded.Check.Evidence = &SelfUpdateEvidence{Repository: "verified"}
	if decoded.ID == selfPlanID(decoded) {
		t.Fatal("present evidence was not bound to hash")
	}
	var old SelfUpdateState
	if e = json.Unmarshal([]byte(`{"schema":"keepupdated/self-update/v1","id":"old","status":"reopened"}`), &old); e != nil || old.Evidence != nil {
		t.Fatal("legacy state fabricated evidence", e)
	}
}

func TestSelfUpdateEvidenceUsesReadbackNotPhase(t *testing.T) {
	for _, status := range []string{"prepared", "installed", "reopened"} {
		t.Run(status, func(t *testing.T) {
			p := SelfUpdatePlan{Directory: t.TempDir(), ID: "fixture", Evidence: &SelfUpdateEvidence{InstallationReadback: "verified", Reopen: "verified"}}
			if e := writeSelfResult(p, SelfUpdateState{Status: status}); e != nil {
				t.Fatal(e)
			}
			var state SelfUpdateState
			data, e := os.ReadFile(filepath.Join(p.Directory, "status.json"))
			if e != nil || json.Unmarshal(data, &state) != nil {
				t.Fatal(e)
			}
			if state.Evidence.InstallationReadback != "" || state.Evidence.Reopen != "" || state.Evidence.ArchiveHash != "" {
				t.Fatal("phase invented evidence", state.Evidence)
			}
		})
	}
}

func TestSelfUpdateEvidenceRejectsSameLengthBadHash(t *testing.T) {
	f := selfSetup(t)
	c := f.check()
	f.archive[10] ^= 1
	p, e := f.u.prepare(context.Background(), c, f.instance.PID)
	if e == nil || e.Error() != "self_update_download_verification_failed" {
		t.Fatal("changed bytes accepted", e)
	}
	if p.Evidence == nil || p.Evidence.ArchiveHash != "unverified" || p.Evidence.CandidatePayloadSignature != "not_checked" {
		t.Fatalf("wrong failed download evidence: %+v", p.Evidence)
	}
}
