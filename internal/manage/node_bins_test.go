package manage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

// From the published @zkochan/cmd-shim 7.0.0 archive pinned by pnpm 10.18.3.
const publishedPnpmShimHeader = "#!/bin/sh\n" +
	`basedir=$(dirname "$(echo "$0" | sed -e 's,\\,/,g')")` + "\n\n" +
	"case `uname` in\n" +
	"    *CYGWIN*|*MINGW*|*MSYS*)\n" +
	"        if command -v cygpath > /dev/null 2>&1; then\n" +
	"            basedir=`cygpath -w \"$basedir\"`\n" +
	"        fi\n" +
	"    ;;\n" +
	"esac\n\n"

func publishedPnpmShim(target, nodePath string, direct bool) string {
	script := publishedPnpmShimHeader
	if nodePath != "" {
		script += "if [ -z \"$NODE_PATH\" ]; then\n" +
			"  export NODE_PATH=\"" + nodePath + "\"\n" +
			"else\n" +
			"  export NODE_PATH=\"" + nodePath + ":$NODE_PATH\"\n" +
			"fi\n"
	}
	if direct {
		return script + "\"$basedir/" + target + "\"   \"$@\"\nexit $?\n"
	}
	return script + "if [ -x \"$basedir/node\" ]; then\n" +
		"  exec \"$basedir/node\"  \"$basedir/" + target + "\" \"$@\"\n" +
		"else\n" +
		"  exec node  \"$basedir/" + target + "\" \"$@\"\n" +
		"fi\n"
}

func TestPnpmPublishedShimReadback(t *testing.T) {
	for _, scenario := range []string{"node", "node-path", "direct", "direct-node-path", "mismatched-branches", "mismatched-node-path", "extra-code", "trailing-code", "comment-only-target", "missing", "directory", "symlink-wrapper", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			direct := strings.HasPrefix(scenario, "direct")
			payload := filepath.Join(f.home, "global/5/.pnpm/example@1.1.0/node_modules/example/extra.js")
			content := "#!/usr/bin/env node\nthrow new Error('must not execute');\n"
			if direct {
				payload = strings.TrimSuffix(payload, ".js")
				content = "SYNTHETIC_NATIVE_DO_NOT_EXECUTE"
			}
			entry := filepath.Join(f.home, "bin/example-extra")
			f.write(payload, content, 0755)
			rel, err := filepath.Rel(filepath.Dir(entry), payload)
			if err != nil {
				t.Fatal(err)
			}
			nodePath := ""
			if strings.Contains(scenario, "node-path") {
				nodePath = filepath.Join(f.home, "global/5/node_modules") + ":" + filepath.Join(f.home, "path with spaces/node_modules")
			}
			script := publishedPnpmShim(rel, nodePath, direct)
			want := scenario == "node" || scenario == "node-path" || scenario == "direct" || scenario == "direct-node-path"
			switch scenario {
			case "mismatched-branches":
				other := filepath.Join(filepath.Dir(payload), "other.js")
				f.write(other, "DO_NOT_EXECUTE", 0755)
				otherRel, err := filepath.Rel(filepath.Dir(entry), other)
				if err != nil {
					t.Fatal(err)
				}
				script = strings.Replace(script, "exec node  \"$basedir/"+rel, "exec node  \"$basedir/"+otherRel, 1)
			case "mismatched-node-path":
				script = strings.Replace(script, nodePath+":$NODE_PATH", nodePath+"/other:$NODE_PATH", 1)
			case "extra-code":
				script = strings.Replace(script, "if [ -x", "touch SHOULD_NOT_RUN\nif [ -x", 1)
			case "trailing-code":
				script += "touch SHOULD_NOT_RUN\n"
			case "comment-only-target":
				script = publishedPnpmShimHeader + "# \"$basedir/" + rel + "\"\nexit 0\n"
			case "oversized":
				script = publishedPnpmShim(rel, strings.Repeat("n", 16<<10), false)
			}
			switch scenario {
			case "missing":
			case "directory":
				if err := os.MkdirAll(entry, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink-wrapper":
				wrapper := filepath.Join(f.home, "other-wrapper")
				f.write(wrapper, script, 0755)
				if err := os.MkdirAll(filepath.Dir(entry), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(wrapper, entry); err != nil {
					t.Fatal(err)
				}
			default:
				f.write(entry, script, 0755)
			}
			if got := pnpmShimMatches(entry, payload); got != want {
				t.Fatalf("wrapper match = %v, want %v", got, want)
			}
		})
	}
}

func TestPnpmShimRejectsShellInterpolation(t *testing.T) {
	for _, value := range []string{"$HOME", "$(touch SHOULD_NOT_RUN)", "`touch SHOULD_NOT_RUN`", `escaped\path`} {
		for _, location := range []string{"target", "node-path"} {
			t.Run(location+"/"+value, func(t *testing.T) {
				f := setup(t)
				name, nodePath := "payload", ""
				if location == "target" {
					name = value
				} else {
					nodePath = filepath.Join(f.home, value, "node_modules")
				}
				payload := filepath.Join(f.home, name, "cli.js")
				entry := filepath.Join(f.home, "bin/example")
				f.write(payload, "DO_NOT_EXECUTE", 0755)
				rel, err := filepath.Rel(filepath.Dir(entry), payload)
				if err != nil {
					t.Fatal(err)
				}
				f.write(entry, publishedPnpmShim(rel, nodePath, false), 0755)
				if pnpmShimMatches(entry, payload) {
					t.Fatal("shell interpolation accepted as a static path")
				}
			})
		}
	}
}

func TestPnpmShimResolvesSymlinkedBinBeforeParentTraversal(t *testing.T) {
	f := setup(t)
	physicalBin := filepath.Join(f.home, "physical/bin")
	logicalBin := filepath.Join(f.home, "logical/bin")
	physicalPayload := filepath.Join(f.home, "physical/payload/cli.js")
	lexicalPayload := filepath.Join(f.home, "logical/payload/cli.js")
	f.write(physicalPayload, "CURRENT_PAYLOAD_DO_NOT_EXECUTE", 0755)
	f.write(lexicalPayload, "OTHER_PAYLOAD_DO_NOT_EXECUTE", 0755)
	if err := os.MkdirAll(physicalBin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physicalBin, logicalBin); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(logicalBin, "example")
	f.write(entry, publishedPnpmShim("../payload/cli.js", "", false), 0755)
	for _, tc := range []struct {
		name, payload string
		want          bool
	}{
		{"physical-target", physicalPayload, true},
		{"lexical-target", lexicalPayload, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pnpmShimMatches(entry, tc.payload); got != tc.want {
				t.Fatalf("symlinked bin match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPnpmShimRejectsRewrittenBaseDirectory(t *testing.T) {
	for _, name := range []string{`custom\qbin`, "control\nbin", "control\tbin", "control\x7fbin"} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			payload := filepath.Join(f.home, "package/cli.js")
			entry := filepath.Join(f.home, name, "example")
			f.write(payload, "DO_NOT_EXECUTE", 0755)
			rel, err := filepath.Rel(filepath.Dir(entry), payload)
			if err != nil {
				t.Fatal(err)
			}
			f.write(entry, publishedPnpmShim(rel, "", false), 0755)
			if pnpmShimMatches(entry, payload) {
				t.Fatal("unverified shell basedir reported as the bound directory")
			}
		})
	}
}

func TestNodeBinMetadataReadback(t *testing.T) {
	for _, tc := range []struct {
		name, packageName, fields string
		want                      bool
	}{
		{"absent", "example", "", true},
		{"null", "example", `,"bin":null`, true},
		{"empty-object", "example", `,"bin":{}`, true},
		{"string", "example", `,"bin":"bin/cli.js"`, true},
		{"scoped-string", "@scope/example", `,"bin":"bin/cli.js"`, true},
		{"object", "example", `,"bin":{"example":"bin/cli.js"}`, true},
		{"empty-string", "example", `,"bin":""`, false},
		{"number", "example", `,"bin":12`, false},
		{"boolean", "example", `,"bin":true`, false},
		{"array", "example", `,"bin":["bin/cli.js"]`, false},
		{"null-target", "example", `,"bin":{"example":null}`, false},
		{"number-target", "example", `,"bin":{"example":12}`, false},
		{"empty-name", "example", `,"bin":{"":"bin/cli.js"}`, false},
		{"traversal-name", "example", `,"bin":{"../example":"bin/cli.js"}`, false},
		{"absolute-target", "example", `,"bin":{"example":"/bin/sh"}`, false},
		{"traversal-target", "example", `,"bin":{"example":"../outside.js"}`, false},
		{"directories-bin", "example", `,"directories":{"bin":"bin"}`, false},
		{"null-with-directories-bin", "example", `,"bin":null,"directories":{"bin":"bin"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			i := f.npm("example", tc.packageName)
			p := f.plan(Selection{Action: "update", IDs: []string{i.ID}}).Items[0]
			if p.Status != "ready" {
				t.Fatal(p)
			}
			payload := filepath.Join(i.Path, "bin/cli.js")
			f.write(payload, "DO_NOT_EXECUTE", 0755)
			if err := os.Symlink(payload, filepath.Join(p.Binding.ResolvedTarget.Node.Bin, "example")); err != nil {
				t.Fatal(err)
			}
			if tc.name == "absent" || tc.name == "null" || tc.name == "empty-object" {
				p.Binding.ResolvedTarget.Node.Bin = filepath.Join(f.home, "missing-bin")
			}
			f.write(filepath.Join(i.Path, "package.json"), fmt.Sprintf(`{"name":%q,"version":"1.0.0"%s}`, i.Name, tc.fields), 0644)
			if got := installationVerified(i, p, audit.Report{}); got != tc.want {
				t.Fatalf("installation verified = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNodeBinReadbackBindsCurrentPayloadAndPackageRoot(t *testing.T) {
	for _, owner := range []string{"npm", "bun"} {
		for _, scenario := range []string{"valid", "internal-payload-symlink", "external-payload-symlink", "swapped-package-root", "wrong-command-target", "missing-command", "nonexecutable-command", "nonregular-command", "copied-wrapper"} {
			t.Run(owner+"/"+scenario, func(t *testing.T) {
				f := setup(t)
				scope := filepath.Join(f.home, "global")
				root := filepath.Join(scope, "node_modules")
				if owner == "npm" {
					root = filepath.Join(scope, "lib/node_modules")
				}
				packagePath := filepath.Join(root, "@scope/example")
				payload := filepath.Join(packagePath, "bin/cli.js")
				bin := filepath.Join(f.home, "custom bin")
				entry := filepath.Join(bin, "example")
				f.write(filepath.Join(packagePath, "package.json"), `{"name":"@scope/example","version":"1.1.0","bin":"bin/cli.js"}`, 0644)
				f.write(payload, "DO_NOT_EXECUTE", 0755)
				if err := os.MkdirAll(bin, 0755); err != nil {
					t.Fatal(err)
				}
				i := audit.Item{Kind: "node-package", Owner: owner, Name: "@scope/example", Identity: "@scope/example", Version: "1.1.0", Path: packagePath, RealPath: packagePath}
				p := PlanItem{Binding: Binding{Path: packagePath, RealPath: packagePath, Scope: scope, ResolvedTarget: &ResolvedTarget{Node: &NodePolicy{Bin: bin}}}}
				want := scenario == "valid" || scenario == "internal-payload-symlink"
				switch scenario {
				case "internal-payload-symlink", "external-payload-symlink":
					target := filepath.Join(packagePath, "actual-cli.js")
					if scenario == "external-payload-symlink" {
						target = filepath.Join(f.home, "external-cli.js")
					}
					if err := os.Rename(payload, target); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, payload); err != nil {
						t.Fatal(err)
					}
				case "swapped-package-root":
					external := filepath.Join(f.home, "external-package")
					if err := os.Rename(packagePath, external); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(external, packagePath); err != nil {
						t.Fatal(err)
					}
				case "wrong-command-target":
					payload = filepath.Join(packagePath, "other-cli.js")
					f.write(payload, "DO_NOT_EXECUTE", 0755)
				}
				switch scenario {
				case "missing-command":
				case "nonexecutable-command":
					if err := os.Chmod(payload, 0644); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(payload, entry); err != nil {
						t.Fatal(err)
					}
				case "nonregular-command":
					if err := os.Mkdir(entry, 0755); err != nil {
						t.Fatal(err)
					}
				case "copied-wrapper":
					rel, err := filepath.Rel(bin, payload)
					if err != nil {
						t.Fatal(err)
					}
					f.write(entry, publishedPnpmShim(rel, "", false), 0755)
				default:
					if err := os.Symlink(payload, entry); err != nil {
						t.Fatal(err)
					}
				}
				if got := nodeBinsVerified(i, p, map[string]string{"example": "bin/cli.js"}); got != want {
					t.Fatalf("bin readback = %v, want %v", got, want)
				}
			})
		}
	}
}
