package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVerifierNumericVersionsAndDiagnostics(t *testing.T) {
	checks, err := filepath.Abs("verify-checks.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		actual, expected string
		ok               bool
	}{
		{"26.2.0", "26.2", true}, {"26.2", "26.2.0", true}, {"13.0.0", "13.0", true},
		{"26.2.1", "26.2", false}, {"27.0", "26.2", false}, {"13.1", "13.0", false}, {"MACOS", "26.2", false}, {"", "26.2", false},
	} {
		t.Run(tc.actual+"-"+tc.expected, func(t *testing.T) {
			command := exec.Command("/bin/bash", "-c", `set -euo pipefail; source "$1"; verify_version "companion SDK" "$2" "$3"`, "fixture", checks, tc.actual, tc.expected)
			output, err := command.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Fatalf("version comparison result: %v: %s", err, output)
			}
			if !tc.ok && (!strings.Contains(string(output), "companion SDK") || !strings.Contains(string(output), "expected "+tc.expected)) {
				t.Fatalf("failure omitted compared field or expected value: %s", output)
			}
		})
	}
	command := exec.Command("/bin/bash", "-c", `source "$1"; verify_equal "signed companion SHA-256" actual expected`, "fixture", checks)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "signed companion SHA-256 (expected expected; got actual)") {
		t.Fatalf("hash mismatch lacks diagnostic: %v: %s", err, output)
	}
}

func TestSignedPayloadManifestBindsExactBytesAndSource(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires package verifier's macOS plutil/stat")
	}
	checks, err := filepath.Abs("verify-checks.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"none", "companion-bytes", "cli-bytes", "companion-hash", "source", "metadata-binding", "version", "dirty", "missing", "symlink"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			app := filepath.Join(dir, "KeepUpdatedAgent.app")
			resources := filepath.Join(app, "Contents", "Resources")
			macos := filepath.Join(app, "Contents", "MacOS")
			for _, path := range []string{resources, macos} {
				if err := os.MkdirAll(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			cli, companion := []byte("signed CLI fixture"), []byte("signed companion fixture")
			if err := os.WriteFile(filepath.Join(resources, "keepupdated"), cli, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(macos, "KeepUpdatedAuthorization"), companion, 0755); err != nil {
				t.Fatal(err)
			}
			metadata := map[string]any{"version": "0.1.2", "source_commit": "fixture-commit", "source_tree_sha256": "fixture-digest", "working_tree_dirty": false, "target": "darwin/arm64", "cli_sha256": fmt.Sprintf("%x", sha256.Sum256(cli))}
			payload := map[string]any{}
			for key, value := range metadata {
				payload[key] = value
			}
			payload["schema"] = "keepupdated/agent-payload/v1"
			payload["companion_sha256"] = fmt.Sprintf("%x", sha256.Sum256(companion))
			switch fault {
			case "companion-bytes":
				if err := os.WriteFile(filepath.Join(macos, "KeepUpdatedAuthorization"), []byte("different signed bytes"), 0755); err != nil {
					t.Fatal(err)
				}
			case "cli-bytes":
				if err := os.WriteFile(filepath.Join(resources, "keepupdated"), []byte("different signed bytes"), 0755); err != nil {
					t.Fatal(err)
				}
			case "companion-hash":
				payload["companion_sha256"] = "different-hash"
			case "source":
				payload["source_commit"] = "other-source"
			case "metadata-binding":
				metadata["source_tree_sha256"] = "other-digest"
			case "version":
				payload["version"] = "9.9.9"
			case "dirty":
				payload["working_tree_dirty"] = true
			}
			manifest := filepath.Join(dir, "KeepUpdatedAgent_PAYLOAD.json")
			info := filepath.Join(resources, "AGENT_BUILDINFO.json")
			for path, value := range map[string]any{manifest: payload, info: metadata} {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, data, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "missing" || fault == "symlink" {
				if err := os.Rename(manifest, manifest+".original"); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "symlink" {
				if err := os.Symlink(manifest+".original", manifest); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("/bin/bash", "-c", `set -euo pipefail; source "$1"; verify_payload "$2" "$3" "$4" fixture-commit fixture-digest`, "fixture", checks, manifest, app, info)
			output, err := command.CombinedOutput()
			if fault == "none" && err != nil {
				t.Fatalf("valid payload rejected: %v: %s", err, output)
			}
			if fault != "none" && (err == nil || !strings.Contains(string(output), "Agent verification failed")) {
				t.Fatalf("payload mismatch not diagnosed: %v: %s", err, output)
			}
		})
	}
}
