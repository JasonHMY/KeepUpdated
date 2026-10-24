package audit

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type customMCPFixture struct {
	s                       *Scanner
	owner, pkg, env, script string
	entry                   string
}

// PackageMCPFixtureReport exercises only static package/configuration readers.
func PackageMCPFixtureReport(config Config) Report {
	s := New(config)
	s.packages()
	s.configs()
	s.finish()
	return s.Report
}

func newCustomMCPFixture(t *testing.T, owner string) customMCPFixture {
	t.Helper()
	s := fixture(t)
	h, err := filepath.EvalSymlinks(s.Config.Home)
	if err != nil {
		t.Fatal(err)
	}
	s.Config.Home = h
	root, bin := filepath.Join(h, "custom", owner, "global"), filepath.Join(h, "custom", owner, "bin")
	s.Config.ManagerRoots = map[string]string{owner: root}
	s.Config.ManagerBins = map[string]string{owner: bin}
	s.Config.Path = bin
	f := customMCPFixture{s: s, owner: owner, entry: filepath.Join(bin, "example-mcp")}
	if owner == "bun" || owner == "pnpm" {
		if owner == "pnpm" {
			root = filepath.Join(root, "5")
		}
		f.pkg = filepath.Join(root, "node_modules/example-server")
		physical := f.pkg
		if owner == "pnpm" {
			physical = filepath.Join(root, ".pnpm/example-server@1.0.0/node_modules/example-server")
			link(t, physical, f.pkg)
		}
		write(t, filepath.Join(physical, "package.json"), `{"name":"example-server","version":"1.0.0","bin":{"example-mcp":"serve.js"}}`)
		f.script = filepath.Join(physical, "serve.js")
	} else {
		f.env = filepath.Join(root, "example-server")
		f.pkg = filepath.Join(f.env, "lib/python3.13/site-packages/example_server-1.0.0.dist-info")
		write(t, filepath.Join(f.pkg, "METADATA"), "Name: example-server\nVersion: 1.0.0\n")
		write(t, filepath.Join(f.pkg, "entry_points.txt"), "[console_scripts]\nexample-mcp = example:main\n")
		f.script = filepath.Join(f.env, "bin/example-mcp")
		if owner == "pipx" {
			write(t, filepath.Join(f.env, "pipx_metadata.json"), fmt.Sprintf(`{"main_package":{"package":"example-server","package_version":"1.0.0","apps":["example-mcp"],"app_paths":[{"__Path__":%q}],"package_or_url":"example-server"}}`, f.script))
		} else {
			write(t, filepath.Join(f.env, "uv-receipt.toml"), fmt.Sprintf("[tool]\nrequirements = [{ name = \"example-server\" }]\nentrypoints = [{ name = \"example-mcp\", install-path = %q, from = \"example-server\" }]\n", f.entry))
		}
	}
	payload := "#!/bin/sh\nexit 99\n"
	write(t, f.script, payload)
	if err := os.Chmod(f.script, 0755); err != nil {
		t.Fatal(err)
	}
	if f.env != "" {
		sum := sha256.Sum256([]byte(payload))
		write(t, filepath.Join(f.pkg, "RECORD"), fmt.Sprintf("../../../bin/example-mcp,sha256=%s,%d\n", base64.RawURLEncoding.EncodeToString(sum[:]), len(payload)))
	}
	link(t, f.script, f.entry)
	return f
}

func (f customMCPFixture) configurations(t *testing.T, command string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"example": map[string]any{"command": command}}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.s.Config.Home, ".cursor/mcp.json"), string(data))
}

func TestMCPCustomManagerPathsKeepExactOwner(t *testing.T) {
	for _, owner := range []string{"bun", "pnpm", "uv", "pipx"} {
		for _, commandKind := range []string{"bare", "absolute", "direct", "default-bin"} {
			t.Run(owner+"/"+commandKind, func(t *testing.T) {
				f := newCustomMCPFixture(t, owner)
				command := f.entry
				if commandKind == "default-bin" {
					bin := filepath.Join(f.s.Config.Home, ".local/bin")
					if owner == "bun" {
						bin = filepath.Join(f.s.Config.Home, ".bun/bin")
					}
					if owner == "pnpm" {
						bin = filepath.Dir(f.s.Config.ManagerRoots[owner])
					}
					command = filepath.Join(bin, "example-mcp")
					link(t, f.script, command)
					f.s.Config.ManagerBins = nil
					if owner == "uv" {
						receipt := filepath.Join(f.env, "uv-receipt.toml")
						data, err := os.ReadFile(receipt)
						if err != nil {
							t.Fatal(err)
						}
						write(t, receipt, strings.ReplaceAll(string(data), f.entry, command))
					}
				}
				if commandKind == "bare" {
					command = "example-mcp"
				} else if commandKind == "direct" {
					if owner == "pnpm" {
						command = filepath.Join(f.pkg, "serve.js")
					} else {
						command = f.script
					}
				}
				f.configurations(t, command)
				f.s.packages()
				f.s.configs()
				ownerID, servers := "", 0
				var ownerItem Item
				for _, item := range f.s.Report.Items {
					if item.Path == f.pkg && item.Owner == owner {
						ownerID = item.ID
						ownerItem = item
					}
				}
				for _, item := range f.s.Report.Items {
					if item.Kind != "mcp-server" {
						continue
					}
					servers++
					configData, err := os.ReadFile(filepath.Join(f.s.Config.Home, ".cursor/mcp.json"))
					if err != nil {
						t.Fatal(err)
					}
					digest := fmt.Sprintf("%x", sha256.Sum256(configData))
					if !MCPCommandOwnerMatches(item, ownerItem, f.s.Report, digest, f.s.Config) {
						t.Errorf("configured owner failed plan revalidation: %s/%s", owner, commandKind)
					}
					if ownerID == "" || len(item.Related) != 1 || item.Related[0] != ownerID || item.Update.Code != "use_installation_owner" {
						t.Errorf("configured %s owner lost: owner=%s server=%+v", owner, ownerID, item)
					}
				}
				if servers != 1 || len(f.s.Report.Queries) != 0 {
					t.Fatal("reader coverage or no-execution guarantee lost", servers, f.s.Report.Queries)
				}
			})
		}
	}
}

func TestMCPCustomManagerPathsRetainRefusals(t *testing.T) {
	for _, owner := range []string{"bun", "pnpm", "uv", "pipx"} {
		for _, scenario := range []string{"unknown-bin", "unknown-root", "ambiguous-owner", "modified-record", "physical-version"} {
			if scenario == "modified-record" && (owner == "bun" || owner == "pnpm") || scenario == "physical-version" && owner != "pnpm" {
				continue
			}
			t.Run(owner+"/"+scenario, func(t *testing.T) {
				f := newCustomMCPFixture(t, owner)
				command := f.entry
				switch scenario {
				case "unknown-bin":
					command = filepath.Join(f.s.Config.Home, "unconfigured/bin/example-mcp")
					link(t, f.script, command)
				case "unknown-root":
					delete(f.s.Config.ManagerRoots, owner)
				case "modified-record":
					write(t, f.script, "#!/bin/sh\n# locally modified\nexit 99\n")
				case "physical-version":
					command = f.script
				}
				f.configurations(t, command)
				f.s.packages()
				if scenario == "ambiguous-owner" {
					for _, item := range f.s.Report.Items {
						if item.Path == f.pkg {
							item.ID = "second-owner"
							f.s.Report.Items = append(f.s.Report.Items, item)
							break
						}
					}
				}
				f.s.configs()
				servers := 0
				for _, item := range f.s.Report.Items {
					if item.Kind != "mcp-server" {
						continue
					}
					servers++
					if len(item.Related) == 1 || item.Update.Code == "use_installation_owner" {
						t.Fatal("unverified command acquired unique owner", item)
					}
					if scenario == "ambiguous-owner" && len(item.Related) != 2 {
						t.Errorf("ambiguity was hidden: %+v", item)
					}
				}
				if servers != 1 || len(f.s.Report.Queries) != 0 {
					t.Fatal("reader coverage or no-execution guarantee lost", servers, f.s.Report.Queries)
				}
			})
		}
	}
}
