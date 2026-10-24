package audit

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestNonRegularMetadataDoesNotBlock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := syscall.Mkfifo(p, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := readSmall(p); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted as metadata")
		}
	case <-time.After(time.Second):
		t.Fatal("metadata read blocked on FIFO")
	}
}
func TestMalformedFeedQueryIsRejected(t *testing.T) {
	for _, raw := range []string{"https://example.org/feed?token=SECRET;ignored=x", "https://example.org/feed?channel=stable&token=%zz", "https://0.0.0.1/feed", "https://[fec0::1]/feed"} {
		if publicURL(raw, true) {
			t.Fatal("unsafe feed accepted", raw)
		}
	}
}
func TestSkillEntryFileSymlinkPreserved(t *testing.T) {
	s := fixture(t)
	source := filepath.Join(s.Config.Home, "source/SKILL.md")
	write(t, source, "---\nname: example\n---\n")
	p := filepath.Join(s.Config.Home, ".agents/skills/example/SKILL.md")
	link(t, source, p)
	s.skills()
	if len(s.Report.Items) != 1 || s.Report.Items[0].Path != p || s.Report.Items[0].RealPath != real(source) {
		t.Fatal(s.Report.Items)
	}
}
func TestMCPIdentitySeparatorsCannotCollide(t *testing.T) {
	s := fixture(t)
	config := "fixture#config.json"
	v := map[string]any{"url": "https://example.org/mcp"}
	s.server(config, "/project#a", "b", v, "")
	s.server(config, "/project", "a#b", v, "")
	if len(s.Report.Items) != 2 {
		t.Fatal("scope/name collision lost server", s.Report.Items)
	}
}
func TestUnsupportedMCPMapsNeverReportParsedOK(t *testing.T) {
	s := fixture(t)
	write(t, filepath.Join(s.Config.Home, ".cursor/mcp.json"), `{"mcpServers":"invalid"}`)
	s.configs()
	if len(s.Report.Items) != 1 || s.Report.Items[0].Health.Code != "invalid_mcp_server_map" {
		t.Fatal(s.Report.Items)
	}
	servers, partial := tomlServers("[mcp_servers]\nfoo = { command = \"/bin/sh\" }\n")
	if !partial || servers["foo"] == nil {
		t.Fatal(servers, partial)
	}
	s = fixture(t)
	s.server("fixture.toml", "user", "foo", servers["foo"], "")
	if s.Report.Items[0].Health.Status != "unknown" {
		t.Fatal(s.Report.Items)
	}
	_, partial = tomlServers("mcp_servers = { foo = { command = \"sh\" } }")
	if !partial {
		t.Fatal("inline root silently accepted")
	}
}
func TestDirectoryMetadataIsRejected(t *testing.T) {
	if _, err := readSmall(t.TempDir()); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestMachOMetadataDoesNotBlockOnFIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fake-executable")
	if err := syscall.Mkfifo(p, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { architectures(p); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Mach-O read blocked on FIFO")
	}
}
func TestSkillDirectoryAliasesRetained(t *testing.T) {
	s := fixture(t)
	source := filepath.Join(s.Config.Home, "source/example")
	write(t, filepath.Join(source, "SKILL.md"), "---\nname: example\n---\n")
	for _, name := range []string{"first", "second"} {
		link(t, source, filepath.Join(s.Config.Home, ".agents/skills", name))
	}
	s.skills()
	if len(s.Report.Items) != 2 {
		t.Fatal("directory alias lost", s.Report.Items)
	}
}
func TestApplicationDirectoryAliasRetained(t *testing.T) {
	s := fixture(t)
	source := filepath.Join(s.Config.Home, "source-apps")
	write(t, filepath.Join(source, "Example.app/Contents/Info.plist"), `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>example.app</string></dict></plist>`)
	for _, name := range []string{"first", "second"} {
		link(t, source, filepath.Join(s.Config.AppRoots[0], name))
	}
	s.apps()
	if len(s.Report.Items) != 2 || s.Report.Items[0].Path == s.Report.Items[1].Path {
		t.Fatal("application directory alias lost", s.Report.Items)
	}
}

func TestRuntimeProbeRequiresExactBinaryName(t *testing.T) {
	s := fixture(t)
	root := s.Config.BrewRoots[0]
	write(t, filepath.Join(root, "Cellar/go/1.0/INSTALL_RECEIPT.json"), `{"source":{"tap":"homebrew/core"}}`)
	for _, relative := range []string{"bin/not-a-runtime", "bin/nested/go"} {
		p := filepath.Join(root, "Cellar/go/1.0", relative)
		write(t, p, "fixture")
		if s.trustedRuntime(real(p), "go") {
			t.Fatal("wrong binary trusted", relative)
		}
	}
	p := filepath.Join(root, "Cellar/go/1.0/bin/go")
	write(t, p, "fixture")
	if !s.trustedRuntime(real(p), "go") {
		t.Fatal("core runtime rejected")
	}
}
func TestBrokenApplicationDirectoryLinkDiagnosed(t *testing.T) {
	s := fixture(t)
	link(t, "/missing/synthetic-apps", filepath.Join(s.Config.AppRoots[0], "linked-folder"))
	s.apps()
	if len(s.Report.Diagnostics) != 1 {
		t.Fatal(s.Report.Diagnostics)
	}
}
func TestBrokenNodePackageLinkRetained(t *testing.T) {
	s := fixture(t)
	p := filepath.Join(s.Config.Home, "global/example")
	link(t, "/missing/synthetic-package", p)
	s.nodeRoot(filepath.Dir(p), "npm")
	if len(s.Report.Items) != 1 || s.Report.Items[0].Health.Status != "error" {
		t.Fatal(s.Report.Items)
	}
}
