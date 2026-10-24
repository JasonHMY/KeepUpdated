package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

type mcpRegistryTransport struct {
	base     http.RoundTripper
	endpoint *url.URL
	t        *testing.T
}

func (m mcpRegistryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.t.Helper()
	if req.URL.Scheme != "https" || req.URL.Host != "registry.npmjs.org" || req.URL.User != nil || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
		m.t.Error("unexpected registry target or credentials")
	}
	clone := req.Clone(req.Context())
	address := *req.URL
	address.Scheme, address.Host = m.endpoint.Scheme, m.endpoint.Host
	clone.URL = &address
	return m.base.RoundTrip(clone)
}

type launcherFixture struct {
	r            Resolver
	report       audit.Report
	config, home string
	selection    manage.Selection
}

func newLauncherFixture(t *testing.T, handler http.HandlerFunc) launcherFixture {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, data string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), mode); err != nil {
			t.Fatal(err)
		}
	}
	npm := filepath.Join(home, "npm")
	write(filepath.Join(npm, "npmrc"), "prefix="+filepath.Join(home, "global")+"\n", 0600)
	write(filepath.Join(npm, "package.json"), `{"name":"npm","version":"11.6.0"}`, 0600)
	write(filepath.Join(npm, "bin/npx-cli.js"), "throw new Error('must never execute launcher');", 0755)
	command := filepath.Join(home, "npx")
	if err := os.Symlink(filepath.Join(npm, "bin/npx-cli.js"), command); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, ".codex/config.toml")
	write(config, fmt.Sprintf(`# preserve all these bytes
[mcp_servers.example]
command = %q
args = ["-y", "@example/server@1.0.0", "SECRET_ARG"] # preserve comment
cwd = %q
[mcp_servers.example.env]
HOME = %q
NPM_CONFIG_USERCONFIG = %q
NPM_CONFIG_GLOBALCONFIG = %q
[mcp_servers.other]
command = "other"
args = ["SECRET_OTHER"]
`, command, home, home, filepath.Join(home, ".npmrc"), filepath.Join(home, "global.npmrc")), 0640)
	item := audit.Item{ID: "mcp", Name: "example", Kind: "mcp-server", Owner: "configuration", Source: "local-command", Path: url.PathEscape(config) + "#user-or-explicit-project#example"}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client := server.Client()
	endpoint, _ := url.Parse(server.URL)
	client.Transport = mcpRegistryTransport{client.Transport, endpoint, t}
	r := Resolver{Options: manage.Options{Home: home}, Timeout: time.Second, MCPHTTPClient: client, Runner: queryRunner(func(manage.Command) ([]byte, error) {
		t.Error("manager/launcher must not run")
		return nil, fmt.Errorf("unexpected command")
	})}
	return launcherFixture{r: r, report: audit.Report{Items: []audit.Item{item}}, config: config, home: home, selection: manage.Selection{Action: "update", IDs: []string{"mcp"}}}
}

func (f launcherFixture) plan(t *testing.T, selection manage.Selection) manage.Plan {
	t.Helper()
	targets, err := f.r.Resolve(context.Background(), f.report, selection)
	if err != nil {
		t.Fatal(err)
	}
	options := f.r.Options
	options.ResolvedTargets = targets
	plan, err := manage.NewPlan(f.report, selection, options)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func (f launcherFixture) selectCandidate(t *testing.T) manage.Plan {
	t.Helper()
	preview := f.plan(t, f.selection)
	item := preview.Items[0]
	if item.Status == "ready" || item.MCPDiscovery == nil || item.MCPDiscovery.Status != "available" {
		t.Fatal("selection bypass or missing candidate", item)
	}
	selection := f.selection
	selection.Special = true
	selection.MCPReference = item.MCPDiscovery.Candidate.Reference
	selection.MCPCandidate = item.MCPDiscovery.CandidateID
	plan := f.plan(t, selection)
	if plan.Items[0].Status != "ready" {
		t.Fatal("selected candidate not ready", plan.Items[0])
	}
	return plan
}
func (f launcherFixture) engine() manage.Engine {
	return manage.Engine{Options: f.r.Options, ResolveTargets: f.r.Resolve, StateDir: filepath.Join(f.home, "state"), Runner: f.r.Runner, Snapshot: func(context.Context) (audit.Report, error) { return f.report, nil }}
}
func releaseMetadata(w http.ResponseWriter, _ *http.Request) {
	fmt.Fprint(w, `{"name":"@example/server","version":"2.0.0","dist":{"integrity":"sha512-fixture"}}`)
}

func TestMCPCandidateSelectedPreviewConfirmApply(t *testing.T) {
	f := newLauncherFixture(t, releaseMetadata)
	original, _ := os.ReadFile(f.config)
	plan := f.selectCandidate(t)
	encoded, _ := json.Marshal(plan)
	if strings.Contains(string(encoded), "SECRET") {
		t.Fatal("raw argument leaked")
	}
	if plan.Items[0].FromVersion != "" || plan.Items[0].TargetVersion != "" || plan.Items[0].MCPDiscovery.ConfiguredVersion != "1.0.0" {
		t.Fatal("configured confused with installed")
	}
	if _, err := manage.Apply(context.Background(), plan, "wrong", f.engine()); err == nil {
		t.Fatal("confirmation bypass")
	}
	noResolver := f.engine()
	noResolver.ResolveTargets = nil
	if _, err := manage.Apply(context.Background(), plan, plan.PlanID, noResolver); err == nil {
		t.Fatal("missing fresh resolver accepted")
	}
	out, err := manage.Apply(context.Background(), plan, plan.PlanID, f.engine())
	if err != nil || len(out.Results) != 1 || out.Results[0].Status != "configured" || out.Results[0].AfterVersion != "" {
		t.Fatal(out, err)
	}
	result := out.Results[0]
	actual, _ := os.ReadFile(f.config)
	if string(actual) != strings.Replace(string(original), "@example/server@1.0.0", "@example/server@2.0.0", 1) {
		t.Fatal("unexpected byte edit")
	}
	backup, err := os.ReadFile(result.BackupPath)
	if err != nil || string(backup) != string(original) {
		t.Fatal("backup mismatch", err)
	}
	info, _ := os.Stat(f.config)
	if info.Mode().Perm() != 0640 {
		t.Fatal("permissions changed")
	}
	info, _ = os.Stat(result.BackupPath)
	if info.Mode().Perm() != 0600 {
		t.Fatal("backup permissions")
	}
	if _, err := manage.Apply(context.Background(), plan, plan.PlanID, f.engine()); err == nil {
		t.Fatal("replayed candidate")
	}
}

func TestMCPCandidateDriftRejectsWithoutEditing(t *testing.T) {
	for _, phase := range []string{"source", "private-source", "config", "metadata", "version-missing", "network", "candidate-selection"} {
		t.Run(phase, func(t *testing.T) {
			changed := false
			f := newLauncherFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if changed && phase == "metadata" {
					fmt.Fprint(w, `{"name":"@example/server","version":"2.0.0","dist":{"integrity":"sha512-changed"}}`)
					return
				}
				if changed && phase == "version-missing" {
					http.NotFound(w, r)
					return
				}
				if changed && phase == "network" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				releaseMetadata(w, r)
			})
			plan := f.selectCandidate(t)
			changed = true
			switch phase {
			case "source":
				if err := os.WriteFile(filepath.Join(f.home, ".npmrc"), []byte("# newly created source file\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "private-source":
				if err := os.WriteFile(filepath.Join(f.home, ".npmrc"), []byte("@example:registry=https://private.invalid/\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "config":
				file, _ := os.OpenFile(f.config, os.O_APPEND|os.O_WRONLY, 0)
				_, _ = file.WriteString("# changed\n")
				_ = file.Close()
			case "candidate-selection":
				selection := plan.Selection
				selection.MCPReference = "@example/server@3.0.0"
				if next := f.plan(t, selection); next.Items[0].Status == "ready" {
					t.Fatal("candidate mismatch accepted")
				}
				return
			}
			before, _ := os.ReadFile(f.config)
			if _, err := manage.Apply(context.Background(), plan, plan.PlanID, f.engine()); err == nil {
				t.Fatal("drift accepted")
			}
			after, _ := os.ReadFile(f.config)
			if string(before) != string(after) {
				t.Fatal("drift changed config")
			}
		})
	}
}

func TestMCPCandidateQueryStatesAndExactSelection(t *testing.T) {
	for _, tc := range []struct {
		name, metadata, status string
		httpStatus             int
	}{
		{"offline", "", "unknown", 503}, {"missing", "", "unknown", 404},
		{"invalid", "not-json", "unknown", 200}, {"wrong-package", `{"name":"other","version":"2.0.0"}`, "unknown", 200},
		{"range", `{"name":"@example/server","version":"^2.0.0"}`, "manual", 200},
		{"deprecated", `{"name":"@example/server","version":"2.0.0","deprecated":"use other"}`, "manual", 200},
		{"matching-config", `{"name":"@example/server","version":"1.0.0"}`, "unchanged", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLauncherFixture(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.httpStatus); fmt.Fprint(w, tc.metadata) })
			p := f.plan(t, f.selection)
			d := p.Items[0].MCPDiscovery
			if p.Items[0].Status == "ready" || d.Status != tc.status || d.Candidate != nil {
				t.Fatal(p.Items[0])
			}
		})
	}
	latestChanged := false
	f := newLauncherFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if latestChanged && strings.HasSuffix(r.URL.Path, "/latest") {
			fmt.Fprint(w, `{"name":"@example/server","version":"3.0.0"}`)
			return
		}
		releaseMetadata(w, r)
	})
	plan := f.selectCandidate(t)
	latestChanged = true
	if out, err := manage.Apply(context.Background(), plan, plan.PlanID, f.engine()); err != nil || out.Results[0].Status != "configured" {
		t.Fatal("selected exact candidate silently followed latest", out, err)
	}
}

func TestMCPCandidateManualSourcesNeverQuery(t *testing.T) {
	for _, form := range []string{"private", "unknown-cwd", "unknown-env", "bunx", "all"} {
		t.Run(form, func(t *testing.T) {
			f := newLauncherFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unproven source queried registry") })
			data, _ := os.ReadFile(f.config)
			switch form {
			case "private":
				if err := os.WriteFile(filepath.Join(f.home, ".npmrc"), []byte("registry=https://private.invalid/\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unknown-cwd":
				data = []byte(strings.Replace(string(data), "cwd = ", "unused_cwd = ", 1))
			case "unknown-env":
				data = []byte(strings.Replace(string(data), "HOME = ", "NODE_OPTIONS = \"--require private\"\nHOME = ", 1))
			case "bunx":
				data = []byte(strings.Replace(string(data), "command = ", "command = \"bunx\" #", 1))
			case "all":
				f.selection.All = true
				f.selection.IDs = nil
			}
			if err := os.WriteFile(f.config, data, 0640); err != nil {
				t.Fatal(err)
			}
			p := f.plan(t, f.selection)
			if p.Items[0].Status == "ready" {
				t.Fatal("unproven ready")
			}
		})
	}
}

func TestMCPCandidateSourceChangeDuringQueryCannotBecomeReady(t *testing.T) {
	var sourceFile string
	f := newLauncherFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if err := os.WriteFile(sourceFile, []byte("registry=https://private.invalid/\n"), 0600); err != nil {
			t.Error(err)
		}
		releaseMetadata(w, r)
	})
	sourceFile = filepath.Join(f.home, ".npmrc")
	p := f.plan(t, f.selection)
	if p.Items[0].Status == "ready" || p.Items[0].MCPDiscovery.Candidate != nil || p.Items[0].MCPDiscovery.Reason != "mcp_launcher_source_changed" {
		t.Fatal("source drift during query accepted", p.Items[0])
	}
}

func TestMCPCandidateRejectsNewLookupShadowAfterSelection(t *testing.T) {
	for _, path := range []string{
		"node_modules/@example/server/package.json",
		"global/lib/node_modules/alias/package.json",
		".npm/_npx/fixture/node_modules/@example/server/package.json",
	} {
		t.Run(path, func(t *testing.T) {
			f := newLauncherFixture(t, releaseMetadata)
			plan := f.selectCandidate(t)
			shadow := filepath.Join(f.home, path)
			if err := os.MkdirAll(filepath.Dir(shadow), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(shadow, []byte(`{"name":"@example/server","version":"2.0.0","_resolved":"file:../private"}`), 0600); err != nil {
				t.Fatal(err)
			}
			original, _ := os.ReadFile(f.config)
			next := f.plan(t, plan.Selection)
			if next.Items[0].Status == "ready" {
				t.Fatal("new same-version lookup shadow reached ready")
			}
			if _, err := manage.Apply(context.Background(), plan, plan.PlanID, f.engine()); err == nil {
				t.Fatal("new same-version lookup shadow applied")
			}
			actual, _ := os.ReadFile(f.config)
			if string(original) != string(actual) {
				t.Fatal("config changed after lookup drift")
			}
		})
	}
}
