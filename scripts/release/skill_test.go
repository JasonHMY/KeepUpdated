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

	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func TestSkillVersionGuardAndArguments(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Skill wrapper requires macOS Apple Silicon and system plutil")
	}
	script, err := filepath.Abs("../../skills/keepupdated/scripts/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, version, schema, planSchema, applySchema string
		wantSuccess                                    bool
	}{
		{"matching", "0.1.2", "keepupdated/v1", manage.PlanSchema, manage.ApplySchema, true},
		{"previous-release", "0.1.1", "keepupdated/v1", manage.PlanSchema, manage.ApplySchema, false},
		{"legacy-readonly-previous-release", "0.1.0", "keepupdated/v1", "", "", false},
		{"legacy-readonly-same-version", "0.1.2", "keepupdated/v1", "", "", false},
		{"missing-apply-schema", "0.1.2", "keepupdated/v1", manage.PlanSchema, "", false},
		{"unknown-plan-schema", "0.1.2", "keepupdated/v1", "future/plan/v9", manage.ApplySchema, false},
		{"unknown-apply-schema", "0.1.2", "keepupdated/v1", manage.PlanSchema, "future/apply/v9", false},
		{"internal-candidate", "0.4.2", "keepupdated/v1", manage.PlanSchema, manage.ApplySchema, false},
		{"unknown-version", "9.9.9", "keepupdated/v1", manage.PlanSchema, manage.ApplySchema, false},
		{"development-version", "0.1.2-dev", "keepupdated/v1", manage.PlanSchema, manage.ApplySchema, false},
		{"unknown-schema", "0.1.2", "future/v9", manage.PlanSchema, manage.ApplySchema, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "synthetic binary")
			marker := filepath.Join(dir, "scan-called")
			metadata := map[string]string{"version": tc.version, "os": "darwin", "arch": "arm64", "schema": tc.schema}
			if tc.planSchema != "" {
				metadata["plan_schema"] = tc.planSchema
			}
			if tc.applySchema != "" {
				metadata["apply_schema"] = tc.applySchema
			}
			encoded, err := json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			stub := fmt.Sprintf("#!/bin/bash\nif [ \"$1\" = version ]; then\n  printf '%%s\\n' '%s'\nelse\n  printf '%%s\\n' \"$@\"\n  printf called > \"$SKILL_TEST_MARKER\"\nfi\n", encoded)
			if err := os.WriteFile(bin, []byte(stub), 0755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/bash", script, "scan", "--project", "/synthetic project")
			cmd.Env = append(os.Environ(), "KEEPUPDATED_BIN="+bin, "SKILL_TEST_MARKER="+marker)
			out, err := cmd.CombinedOutput()
			if tc.wantSuccess {
				if err != nil || string(out) != "scan\n--project\n/synthetic project\n" {
					t.Fatalf("arguments changed: %q, %v", out, err)
				}
				if _, err := os.Stat(marker); err != nil {
					t.Fatal("accepted binary was not called")
				}
			} else {
				if err == nil {
					t.Fatalf("accepted incompatible binary: %s", out)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("called scan despite incompatible metadata")
				}
			}
		})
	}
	for _, path := range []string{"/nonexistent/synthetic-keepupdated", "relative-keepupdated", ""} {
		cmd := exec.Command("/bin/bash", script, "scan")
		cmd.Env = append(os.Environ(), "KEEPUPDATED_BIN="+path)
		if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "keepupdated skill:") {
			t.Fatalf("invalid binary path accepted: %q, %v", out, err)
		}
	}
}

func TestSkillFindsPairedBinaryWithoutPathLookup(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Skill wrapper requires macOS Apple Silicon and system plutil")
	}
	root := t.TempDir()
	script := filepath.Join(root, "skills", "keepupdated", "scripts", "run.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../skills/keepupdated/scripts/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, data, 0644); err != nil {
		t.Fatal(err)
	}
	stub := fmt.Sprintf("#!/bin/bash\nif [ \"$1\" = version ]; then\n printf '%%s\\n' '{\"version\":\"0.1.2\",\"os\":\"darwin\",\"arch\":\"arm64\",\"schema\":\"keepupdated/v1\",\"plan_schema\":\"%s\",\"apply_schema\":\"%s\"}'\nelse\n printf 'paired binary\\n'\n exit 1\nfi\n", manage.PlanSchema, manage.ApplySchema)
	if err := os.WriteFile(filepath.Join(root, "keepupdated"), []byte(stub), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/bash", script, "check")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "KEEPUPDATED_BIN=") && !strings.HasPrefix(entry, "PATH=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "PATH=/nonexistent")
	out, err := cmd.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 1 || string(out) != "paired binary\n" {
		t.Fatalf("relative binary lookup or exit propagation failed: %q, %v", out, err)
	}
}

func TestSkillResultsPreservesResponseAndFailure(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Skill wrapper requires macOS Apple Silicon and system plutil")
	}
	script, err := filepath.Abs("../../skills/keepupdated/scripts/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, response, diagnostic string
		exit                       int
	}{
		{"unfinished", `{"schema":"keepupdated/recent-operation/v1","operation":{"state":"in_progress","results":[{"status":"verification_unknown"}]}}`, "", 0},
		{"absent", `{"schema":"keepupdated/recent-operation/v1","operation":null}`, "", 0},
		{"unreadable", "", "Saved operation results could not be read: controlled failure", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin, marker := filepath.Join(dir, "synthetic binary"), filepath.Join(dir, "arguments")
			stub := fmt.Sprintf(`#!/bin/bash
case "$1" in
  version) printf '%%s\n' '{"version":"0.1.2","os":"darwin","arch":"arm64","schema":"keepupdated/v1","plan_schema":"%s","apply_schema":"%s","recent_operation_schema":"%s"}' ;;
  results)
    printf '%%s\n' "$@" > "$SKILL_TEST_MARKER"
    printf '%%s' "$SKILL_TEST_RESPONSE"
    printf '%%s' "$SKILL_TEST_DIAGNOSTIC" >&2
    exit "$SKILL_TEST_EXIT" ;;
  *) exit 77 ;;
esac
`, manage.PlanSchema, manage.ApplySchema, manage.RecentOperationSchema)
			if err := os.WriteFile(bin, []byte(stub), 0755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/bash", script, "results", "--format", "json")
			cmd.Env = append(os.Environ(), "KEEPUPDATED_BIN="+bin, "SKILL_TEST_MARKER="+marker,
				"SKILL_TEST_RESPONSE="+tc.response, "SKILL_TEST_DIAGNOSTIC="+tc.diagnostic,
				fmt.Sprintf("SKILL_TEST_EXIT=%d", tc.exit))
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			if err != nil {
				exit, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if code != tc.exit || stdout.String() != tc.response || stderr.String() != tc.diagnostic {
				t.Fatalf("results changed: exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			args, err := os.ReadFile(marker)
			if err != nil || string(args) != "results\n--format\njson\n" {
				t.Fatalf("results arguments changed: %q, %v", args, err)
			}
		})
	}
}

func TestSkillResultsRejectsOlderSameVersionBinary(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Skill wrapper requires macOS Apple Silicon and system plutil")
	}
	script, err := filepath.Abs("../../skills/keepupdated/scripts/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{"", "future/recent-operation/v2"} {
		t.Run(schema, func(t *testing.T) {
			dir := t.TempDir()
			bin, marker := filepath.Join(dir, "keepupdated"), filepath.Join(dir, "called")
			fields := map[string]string{"version": "0.1.2", "os": "darwin", "arch": "arm64", "schema": "keepupdated/v1", "plan_schema": manage.PlanSchema, "apply_schema": manage.ApplySchema}
			if schema != "" {
				fields["recent_operation_schema"] = schema
			}
			metadata, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			stub := fmt.Sprintf("#!/bin/bash\nif [ \"$1\" = version ]; then printf '%%s\\n' '%s'; else touch \"$SKILL_TEST_MARKER\"; fi\n", metadata)
			if err := os.WriteFile(bin, []byte(stub), 0755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/bash", script, "results", "--format", "json")
			cmd.Env = append(os.Environ(), "KEEPUPDATED_BIN="+bin, "SKILL_TEST_MARKER="+marker)
			if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "keepupdated skill:") {
				t.Fatalf("accepted unsupported results: %s, %v", out, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("called results despite incompatible metadata")
			}
		})
	}
}
