package audit

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, p, body string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(body), 0644); e != nil {
		t.Fatal(e)
	}
}
func link(t *testing.T, target, p string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0755)
	if e := os.Symlink(target, p); e != nil {
		t.Fatal(e)
	}
}
func fixture(t *testing.T) *Scanner {
	t.Helper()
	h := t.TempDir()
	return New(Config{Home: h, Path: filepath.Join(h, "bin"), BrewRoots: []string{filepath.Join(h, "brew")}, AppRoots: []string{filepath.Join(h, "Applications")}, Timeout: time.Second, Country: "US"})
}
func TestActiveKegNotOldest(t *testing.T) {
	s := fixture(t)
	root := s.Config.BrewRoots[0]
	for _, v := range []string{"24.14.0_1", "24.21.0_2", "24.22.0"} {
		write(t, filepath.Join(root, "Cellar/node@24", v, "INSTALL_RECEIPT.json"), `{"arch":"arm64","source":{"tap":"homebrew/core"}}`)
	}
	link(t, "../Cellar/node@24/24.22.0", filepath.Join(root, "opt/node@24"))
	s.brew()
	s.finish()
	if len(s.Report.Items) != 1 {
		t.Fatal(s.Report.Items)
	}
	i := s.Report.Items[0]
	if i.ActiveVersion != "24.22.0" || len(i.Versions) != 3 || i.Source != "homebrew/core" {
		t.Fatal(i)
	}
}
func TestVersionConstraints(t *testing.T) {
	for _, c := range []struct {
		a, b string
		cmp  int
		ok   bool
	}{{"1.2", "1.2.0", 0, true}, {"1.2_1", "1.2_2", -1, true}, {"2.0", "1.9", 1, true}, {"1.2-beta", "1.2", 0, false}, {"latest", "2", 0, false}, {"", "2", 0, false}, {"1,42", "1,43", 0, false}, {"99999999999999999999999", "2", 0, false}} {
		got, ok := compare(c.a, c.b)
		if got != c.cmp || ok != c.ok {
			t.Fatalf("%+v => %d %v", c, got, ok)
		}
	}
}
func TestCaskWithoutAppAndCustomTap(t *testing.T) {
	s := fixture(t)
	root := s.Config.BrewRoots[0]
	write(t, filepath.Join(root, "Caskroom/cloud-tools/2.1/bin/tool"), "fake")
	write(t, filepath.Join(root, "Caskroom/cloud-tools/.metadata/2.1/date/Casks/cloud-tools.json"), `{"token":"cloud-tools","version":"2.1","tap":"homebrew/cask","artifacts":[{"binary":["bin/tool"]}]}`)
	write(t, filepath.Join(root, "Cellar/widget/1.0/INSTALL_RECEIPT.json"), `{"source":{"tap":"example/tools"}}`)
	s.brew()
	if len(s.Report.Items) != 2 {
		t.Fatal(s.Report.Items)
	}
	for _, i := range s.Report.Items {
		if i.Kind == "cask" && i.Version != "2.1" {
			t.Fatal(i)
		}
		if i.Kind == "formula" && i.Update.Status != "unsupported" {
			t.Fatal(i)
		}
	}
}
func TestMCPDoesNotExecuteOrExposeSecrets(t *testing.T) {
	s := fixture(t)
	p := filepath.Join(s.Config.Home, ".cursor/mcp.json")
	write(t, p, `{"mcpServers":{"evil":{"command":"sh","args":["-c","touch SHOULD_NOT_EXIST"],"env":{"TOKEN":"SECRET_NEVER_OUTPUT"}},"remote":{"url":"https://user:SECRET_NEVER_OUTPUT@example.invalid/?token=SECRET_NEVER_OUTPUT","headers":{"Authorization":"SECRET_NEVER_OUTPUT"}}}}`)
	s.configs()
	if len(s.Report.Items) != 3 {
		t.Fatal(s.Report.Items)
	}
	if len(s.Report.Queries) != 0 {
		t.Fatal("configuration triggered execution", s.Report.Queries)
	}
	b, _ := json.Marshal(s.Report)
	if strings.Contains(string(b), "SECRET_NEVER_OUTPUT") || strings.Contains(string(b), "touch SHOULD") {
		t.Fatal("sensitive config escaped")
	}
}
func TestJSONCAndTOMLSubset(t *testing.T) {
	b := stripJSONComments([]byte("{\n// comment\n\"url\":\"https://example.invalid/a//b\",/*x*/\"v\":[1,],}"))
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		t.Fatal(string(b))
	}
	ss, partial := tomlServers("[mcp_servers.\"one\"]\ncommand = \"node\"\n[mcp_servers.one.env]\nTOKEN = \"secret\"\n[mcp_servers.two]\nurl = \"https://example.invalid\"\n")
	if partial || len(ss) != 2 {
		t.Fatal(ss, partial)
	}
	_, partial = tomlServers("[mcp_servers.a]\ncommand = [\"unsupported\"]")
	if !partial {
		t.Fatal("unsupported TOML hidden")
	}
}
func TestBrokenInterpreterRetainsPackages(t *testing.T) {
	s := fixture(t)
	p := filepath.Join(s.Config.Home, ".local/share/uv/tools/example")
	link(t, "/missing/synthetic-python", filepath.Join(p, "bin/python"))
	write(t, filepath.Join(p, "lib/python3.14/site-packages/example-1.0.dist-info/METADATA"), "Name: example\nVersion: 1.0\n\n")
	s.pythonEnv(p, "uv")
	if len(s.Report.Items) != 2 || s.Report.Items[0].Health.Code != "interpreter_missing_or_broken" {
		t.Fatal(s.Report.Items)
	}
}
func TestSymlinkSkillAndLocalChanges(t *testing.T) {
	s := fixture(t)
	target := filepath.Join(s.Config.Home, "source/example")
	write(t, filepath.Join(target, "SKILL.md"), "---\nname: example\ndescription: fixture\n---\n")
	link(t, target, filepath.Join(s.Config.Home, ".codex/skills/example"))
	s.skills()
	if len(s.Report.Items) != 1 {
		t.Fatalf("linked skill missing: %+v", s.Report)
	}
}
func TestSummaryPreservesEveryItem(t *testing.T) {
	s := fixture(t)
	for n, st := range []string{"unknown", "error", "available", "current", "unsupported"} {
		i := item("test", st, st, "fixture")
		i.Update = result(st, "test")
		if n == 1 {
			i.Health = result("error", "bad")
		}
		s.add(i)
	}
	before := len(s.Report.Items)
	s.check()
	s.finish()
	if len(s.Report.Items) != before {
		t.Fatal("items lost")
	}
	total := 0
	for k, v := range s.Report.Summary {
		if k != "total" {
			total += v
		}
	}
	if total != before {
		t.Fatal(s.Report.Summary)
	}
}

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestMacIdentityAndFailureIsolation(t *testing.T) {
	s := fixture(t)
	s.Config.Online = true
	s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		body := `{"results":[{"bundleId":"example.same","kind":"software","version":"99.0"}]}`
		status := 200
		if strings.Contains(r.URL.Path, "broken") {
			status = 404
			body = "missing"
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	i := item("application", "Same Name", "one", "app-store")
	i.Source = "app-store"
	i.Identity = "example.same"
	i.Version = "1.0"
	i.lookup = i.Identity
	s.add(i)
	j := item("python-package", "broken", "two", "pipx")
	j.Source = "pypi-public-candidate"
	j.lookup = "broken"
	s.add(j)
	s.check()
	if len(s.Report.Items) != 2 || s.Report.Items[0].Update.Code != "no_matching_mac_product" || s.Report.Items[1].Update.Code != "not_found" {
		t.Fatal(s.Report.Items)
	}
}
func TestRegistryOriginAndArchitectureStayUnknown(t *testing.T) {
	s := fixture(t)
	s.Config.Online = true
	s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		body := `{"name":"example","version":"2.0"}`
		if strings.Contains(r.URL.Host, "formulae") {
			body = `{"token":"example","version":"2.0","variations":{"intel":{"version":"1.0"}}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	for _, source := range []string{"npm-public-candidate", "homebrew/cask"} {
		i := item("package", "example", source, "fixture")
		i.Source = source
		i.lookup = "example"
		i.Identity = "example"
		i.Version = "1.0"
		s.add(i)
	}
	s.check()
	for _, i := range s.Report.Items {
		if i.Update.Status != "unknown" {
			t.Fatal(i)
		}
	}
}
func snapshot(t *testing.T, p string) map[string]string {
	t.Helper()
	m := map[string]string{}
	filepath.WalkDir(p, func(q string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			b, _ := os.ReadFile(q)
			m[q] = string(b)
		}
		return nil
	})
	return m
}
func TestStaticScanDoesNotModifyFixtures(t *testing.T) {
	s := fixture(t)
	write(t, filepath.Join(s.Config.Home, ".cursor/mcp.json"), `{"mcpServers":{}}`)
	write(t, filepath.Join(s.Config.Home, ".agents/skills/example/SKILL.md"), "---\nname: example\n---")
	before := snapshot(t, s.Config.Home)
	s.configs()
	s.skills()
	s.brew()
	s.packages()
	after := snapshot(t, s.Config.Home)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("fixture files changed")
	}
}

func TestCaskReceiptFallback(t *testing.T) {
	s := fixture(t)
	p := filepath.Join(s.Config.BrewRoots[0], "Caskroom/cloud-tools")
	write(t, filepath.Join(p, "2.1/tool"), "fixture")
	write(t, filepath.Join(p, ".metadata/2.1/date/Casks/cloud-tools.json"), "{}")
	write(t, filepath.Join(p, ".metadata/INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/cask","version":"2.1"},"arch":"arm64"}`)
	s.brew()
	if len(s.Report.Items) != 1 || s.Report.Items[0].Source != "homebrew/cask" || s.Report.Items[0].lookup != "cloud-tools" {
		t.Fatal(s.Report.Items)
	}
}
func TestNestedApplicationsAndProductIdentity(t *testing.T) {
	if _, e := os.Stat("/usr/bin/plutil"); e != nil {
		t.Skip("macOS plutil required")
	}
	s := fixture(t)
	root := s.Config.AppRoots[0]
	for _, p := range []string{filepath.Join(root, "Tools/Viewer.app"), filepath.Join(root, "Other/Viewer.app"), filepath.Join(root, "Tools/Viewer.app/Contents/Helpers/Helper.app")} {
		id := "example." + strings.ReplaceAll(strings.TrimPrefix(p, root), "/", ".")
		write(t, filepath.Join(p, "Contents/Info.plist"), `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>`+id+`</string><key>CFBundleShortVersionString</key><string>1.2.3</string></dict></plist>`)
	}
	s.apps()
	if len(s.Report.Items) != 3 {
		t.Fatal(s.Report.Items)
	}
	ids := map[string]bool{}
	components := 0
	for _, i := range s.Report.Items {
		ids[i.Identity] = true
		if i.Owner == "app-component" {
			components++
			if len(i.Related) != 1 {
				t.Fatal(i)
			}
		}
	}
	if len(ids) != 3 || components != 1 {
		t.Fatal(s.Report.Items)
	}
}
func TestUnknownAndNewerVersionsNeverCurrent(t *testing.T) {
	s := fixture(t)
	s.Config.Online = true
	s.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"name":"example","versions":{"stable":"2.0"}}`)), Header: http.Header{}}, nil
	})
	for _, v := range []string{"", "3.0", "1.0-beta", "1.0"} {
		i := item("formula", "example", v, "homebrew")
		i.Source = "homebrew/core"
		i.lookup = "example"
		i.ActiveVersion = v
		s.add(i)
	}
	s.check()
	for _, i := range s.Report.Items {
		want := "unknown"
		if i.ActiveVersion == "1.0" {
			want = "available"
		}
		if i.Update.Status != want {
			t.Fatal(i)
		}
	}
}

func TestNetworkTimeoutIsExplicit(t *testing.T) {
	s := fixture(t)
	s.client.Timeout = 10 * time.Millisecond
	s.client.Transport = transport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	var v any
	if got := s.get("https://example.invalid/fixture", &v); got != "timeout" {
		t.Fatal(got)
	}
}
func TestQueryTimeout(t *testing.T) {
	s := fixture(t)
	s.Config.Timeout = 10 * time.Millisecond
	if _, e := os.Stat("/bin/sleep"); e != nil {
		t.Skip("sleep unavailable")
	}
	start := time.Now()
	_, e := s.run("/bin/sleep", "1")
	if e == nil || time.Since(start) > 2*time.Second {
		t.Fatal("timeout not enforced", e)
	}
}

func TestUnownedRuntimeNeverProbed(t *testing.T) {
	s := fixture(t)
	p := filepath.Join(s.Config.Home, "bin/node")
	write(t, p, "#!/bin/sh\ntouch SHOULD_NOT_EXIST\n")
	os.Chmod(p, 0755)
	s.executables()
	if len(s.Report.Queries) != 0 {
		t.Fatal(s.Report.Queries)
	}
	if s.trustedRuntime(p, "node") {
		t.Fatal("unowned runtime trusted")
	}
}
func TestMCPNonExecutableIsNotHealthy(t *testing.T) {
	s := fixture(t)
	p := filepath.Join(s.Config.Home, "no-execute")
	write(t, p, "fixture")
	s.server("fixture.json", "user", "test", map[string]any{"command": p}, "")
	if s.Report.Items[0].Health.Code != "command_not_executable" {
		t.Fatal(s.Report.Items)
	}
}

func TestActiveReceiptFailureCannotInheritOldSource(t *testing.T) {
	s := fixture(t)
	root := s.Config.BrewRoots[0]
	write(t, filepath.Join(root, "Cellar/example/1.0/INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"}}`)
	write(t, filepath.Join(root, "Cellar/example/2.0/INSTALL_RECEIPT.json"), `invalid`)
	link(t, "../Cellar/example/2.0", filepath.Join(root, "opt/example"))
	s.brew()
	i := s.Report.Items[0]
	if i.ActiveVersion != "2.0" || i.Source != "unknown" || i.lookup != "" || i.Health.Code != "active_receipt_unreadable" {
		t.Fatal(i)
	}
}
func TestDisabledMCPIsNotRuntimeFailure(t *testing.T) {
	s := fixture(t)
	s.server("fixture", "user", "disabled", map[string]any{"command": "/missing/tool", "enabled": false}, "")
	if s.Report.Items[0].Health.Code != "disabled_not_started" || len(s.Report.Queries) != 0 {
		t.Fatal(s.Report.Items)
	}
}
