package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExtractedCandidatesRejectMixedBaselines(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("package readback uses macOS plutil and the Apple Silicon Skill")
	}
	script, err := filepath.Abs("verify-sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := os.ReadFile("../../skills/keepupdated/scripts/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	pluginWrapper, err := os.ReadFile("../../plugin/skills/keepupdated/scripts/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"matching", "binary-source", "source-digest", "dirty-source", "plugin-version", "bundle-build", "skill-content", "missing-results-schema", "metadata-source", "wrong-selected-source"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			cli, app, agent, plugin := filepath.Join(root, "cli"), filepath.Join(root, "KeepUpdated.app"), filepath.Join(root, "KeepUpdatedAgent.app"), filepath.Join(root, "plugin")
			pluginAgent := filepath.Join(plugin, "KeepUpdatedAgent.app")
			commit := strings.Repeat("a", 40)
			build := map[string]any{"version": "0.1.2", "commit": commit, "build_date": "2026-01-01T00:00:00Z", "go_version": "go-test", "target": "darwin/arm64"}
			version := map[string]any{"version": "0.1.2", "commit": commit, "build_date": build["build_date"], "go_version": build["go_version"], "os": "darwin", "arch": "arm64", "schema": "keepupdated/v1", "plan_schema": "keepupdated/plan/v1", "apply_schema": "keepupdated/apply/v1", "recent_operation_schema": "keepupdated/recent-operation/v1"}
			write := func(path string, data []byte, mode os.FileMode) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, mode); err != nil {
					t.Fatal(err)
				}
			}
			writeJSON := func(path string, value any) {
				t.Helper()
				b, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				write(path, b, 0644)
			}
			writeBinary := func(resources string) {
				t.Helper()
				b, err := json.Marshal(version)
				if err != nil {
					t.Fatal(err)
				}
				write(filepath.Join(resources, "keepupdated"), []byte(fmt.Sprintf("#!/bin/sh\n[ \"$1\" = version ] || exit 2\ncat <<'JSON'\n%s\nJSON\n", b)), 0755)
			}
			for _, resources := range []string{cli, filepath.Join(app, "Contents/Resources"), filepath.Join(agent, "Contents/Resources"), filepath.Join(pluginAgent, "Contents/Resources")} {
				writeJSON(filepath.Join(resources, "BUILDINFO.json"), build)
				writeBinary(resources)
				write(filepath.Join(resources, "skills/keepupdated/SKILL.md"), []byte("matching synthetic Skill"), 0644)
				write(filepath.Join(resources, "skills/keepupdated/scripts/run.sh"), wrapper, 0755)
			}
			for _, bundle := range []string{app, agent, pluginAgent} {
				plist := map[string]any{"CFBundleShortVersionString": "0.1.2", "CFBundleVersion": "10", "LSMinimumSystemVersion": "13.0"}
				if scenario == "bundle-build" && bundle == agent {
					plist["CFBundleVersion"] = "8"
				}
				writeJSON(filepath.Join(bundle, "Contents/Info.plist"), plist)
				metadata := map[string]any{"version": "0.1.2", "source_commit": commit, "source_tree_sha256": strings.Repeat("b", 64), "working_tree_dirty": false, "target": "darwin/arm64", "minimum_macos": "13.0"}
				name := "AGENT_BUILDINFO.json"
				if bundle == app {
					name = "DESKTOP_BUILDINFO.json"
				}
				if bundle == agent {
					if scenario == "source-digest" {
						metadata["source_tree_sha256"] = strings.Repeat("c", 64)
					}
					if scenario == "dirty-source" {
						metadata["working_tree_dirty"] = true
					}
				}
				writeJSON(filepath.Join(bundle, "Contents/Resources", name), metadata)
			}
			for _, name := range []string{"plugin.json", ".codex-plugin/plugin.json"} {
				v := "0.1.2"
				if scenario == "plugin-version" && name == "plugin.json" {
					v = "0.0.9"
				}
				writeJSON(filepath.Join(plugin, name), map[string]string{"name": "keepupdated", "version": v})
			}
			write(filepath.Join(plugin, "skills/keepupdated/SKILL.md"), []byte("matching synthetic Skill"), 0644)
			write(filepath.Join(plugin, "skills/keepupdated/scripts/run.sh"), pluginWrapper, 0755)
			resources := filepath.Join(agent, "Contents/Resources")
			switch scenario {
			case "binary-source":
				version["commit"] = strings.Repeat("c", 40)
				writeBinary(resources)
			case "metadata-source":
				build["commit"] = strings.Repeat("c", 40)
				writeJSON(filepath.Join(resources, "BUILDINFO.json"), build)
			case "missing-results-schema":
				delete(version, "recent_operation_schema")
				writeBinary(resources)
			case "skill-content":
				write(filepath.Join(resources, "skills/keepupdated/SKILL.md"), []byte("outdated Skill"), 0644)
			case "wrong-selected-source":
				commit = strings.Repeat("d", 40)
			}
			cmd := exec.Command("/bin/bash", script, cli, app, agent, plugin, commit)
			out, err := cmd.CombinedOutput()
			if scenario == "matching" && err != nil {
				t.Fatalf("matching candidates rejected: %v\n%s", err, out)
			}
			if scenario != "matching" && err == nil {
				t.Fatalf("mixed candidates accepted: %s\n%s", scenario, out)
			}
		})
	}
}
