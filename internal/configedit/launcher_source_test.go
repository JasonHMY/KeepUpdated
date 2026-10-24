package configedit

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func launcherSourceFixture(t *testing.T) (audit.Item, string, string) {
	t.Helper()
	home := t.TempDir()
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	npm := filepath.Join(home, "npm")
	write(filepath.Join(npm, "npmrc"), "prefix="+filepath.Join(home, "global")+"\n")
	write(filepath.Join(npm, "package.json"), `{"name":"npm","version":"11.6.0"}`)
	write(filepath.Join(npm, "bin/npx-cli.js"), "// controlled launcher; must never run")
	if err := os.Chmod(filepath.Join(npm, "bin/npx-cli.js"), 0755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "npx")
	if err := os.Symlink(filepath.Join(npm, "bin/npx-cli.js"), bin); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".codex/config.toml")
	config := fmt.Sprintf(`# keep this comment
[mcp_servers.example]
command = %q
args = ["-y", "@example/server@1.0.0", "PRIVATE_ARGUMENT"] # keep argument
cwd = %q
env_vars = []
[mcp_servers.example.env]
HOME = %q
NPM_CONFIG_USERCONFIG = %q
NPM_CONFIG_GLOBALCONFIG = %q
[mcp_servers.other]
command = "other"
args = ["PRIVATE_OTHER"]
`, bin, home, home, filepath.Join(home, ".npmrc"), filepath.Join(home, "global.npmrc"))
	write(path, config)
	item := audit.Item{ID: "server", Kind: "mcp-server", Name: "example", Owner: "configuration", Path: url.PathEscape(path) + "#user-or-explicit-project#example"}
	return item, path, home
}

func TestLauncherSourceBoundDefaultAndNoPrivateValues(t *testing.T) {
	item, _, home := launcherSourceFixture(t)
	source, err := NPMLauncherSource(item)
	if err != nil || source.Identity == "" || source.Reference.Package != "@example/server" {
		t.Fatal(source, err)
	}
	encoded, _ := json.Marshal(source)
	if strings.Contains(string(encoded), "PRIVATE") || strings.Contains(string(encoded), home) {
		t.Fatal("raw configuration leaked", string(encoded))
	}
	again, err := NPMLauncherSource(item)
	if err != nil || again.Identity != source.Identity {
		t.Fatal("unstable source", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".npmrc"), []byte("registry=https://registry.npmjs.org/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := NPMLauncherSource(item)
	if err != nil || changed.Identity == source.Identity {
		t.Fatal("config not bound", err)
	}
}

func TestLauncherSourceRejectsUnknownContextBeforeNetwork(t *testing.T) {
	cases := []struct{ name, before, after string }{
		{"missing cwd", "cwd = ", "unused_cwd = "},
		{"relative cwd", "cwd = ", "cwd = \"relative\" #"},
		{"inherited env", "env_vars = []", `env_vars = ["NPM_CONFIG_REGISTRY"]`},
		{"unknown env", "HOME = ", "NPM_CONFIG_REGISTRY = \"https://private.invalid/\"\nHOME = "},
		{"missing home", "HOME = ", "OTHER = "},
		{"case alias", "NPM_CONFIG_USERCONFIG", "npm_config_userconfig"},
		{"range", "@example/server@1.0.0", "@example/server@^1.0.0"},
		{"inline env", "env_vars = []", "env = { HOME = \"/unknown\" }"},
		{"bunx", "command = ", "command = \"bunx\" #"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item, path, _ := launcherSourceFixture(t)
			data, _ := os.ReadFile(path)
			if err := os.WriteFile(path, []byte(strings.Replace(string(data), tc.before, tc.after, 1)), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := NPMLauncherSource(item); err == nil {
				t.Fatal("unknown source accepted")
			}
		})
	}
	for _, config := range []string{"registry=https://private.invalid/\n", "@example:registry=https://private.invalid/\n", "registry=${REGISTRY}\n", "tag=next\n", "prefix=/unknown\n", "//registry.npmjs.org/:_authToken=PRIVATE_TOKEN\n"} {
		t.Run(config, func(t *testing.T) {
			item, _, home := launcherSourceFixture(t)
			if err := os.WriteFile(filepath.Join(home, ".npmrc"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := NPMLauncherSource(item); err == nil {
				t.Fatal("source override accepted")
			}
		})
	}
}

func TestLauncherSourceResolvesWorkingDirectoryAndApplyRechecks(t *testing.T) {
	item, path, home := launcherSourceFixture(t)
	actual := filepath.Join(home, "real-project")
	if err := os.MkdirAll(actual, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "project-link")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), fmt.Sprintf("cwd = %q", home), fmt.Sprintf("cwd = %q", link), 1)), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := NPMLauncherSource(item)
	if err != nil {
		t.Fatal(err)
	}
	target, err := PlanReference(item, "@example/server@2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	target.LauncherSourceIdentity = source.Identity
	before, _ := os.ReadFile(path)
	if err := os.WriteFile(filepath.Join(actual, ".npmrc"), []byte("registry=https://private.invalid/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NPMLauncherSource(item); err == nil {
		t.Fatal("real cwd source ignored")
	}
	result, err := Apply(target)
	if err == nil || result.Reason != "mcp_launcher_source_changed" || result.Changed {
		t.Fatal(result, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("edited after source change")
	}
}

func TestLauncherSourceRejectsOtherClientAndDuplicateEnvTable(t *testing.T) {
	item, path, home := launcherSourceFixture(t)
	data, _ := os.ReadFile(path)
	other := filepath.Join(home, "config.toml")
	if err := os.WriteFile(other, data, 0600); err != nil {
		t.Fatal(err)
	}
	moved := item
	moved.Path = url.PathEscape(other) + "#user-or-explicit-project#example"
	if _, err := NPMLauncherSource(moved); err == nil {
		t.Fatal("unknown client inheritance accepted")
	}
	if err := os.WriteFile(path, append(data, []byte("\n[mcp_servers.example.env]\nPATH = '/usr/bin'\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NPMLauncherSource(item); err == nil {
		t.Fatal("duplicate TOML table accepted")
	}
}
