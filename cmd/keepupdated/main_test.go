package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func TestHelpAndVersionDoNotScan(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"scan", "--help"}, {"check", "--help"}, {"version"}, {"version", "--format", "json"}} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 0 {
			t.Fatal(args, code, errOut.String())
		}
		if strings.Contains(out.String(), "\"items\"") {
			t.Fatal("help or version scanned host")
		}
	}
	var out, errOut bytes.Buffer
	run([]string{"version", "--format", "json"}, &out, &errOut)
	var m map[string]string
	if json.Unmarshal(out.Bytes(), &m) != nil || m["version"] != version || m["commit"] != commit || m["schema"] != audit.Schema || m["plan_schema"] != manage.PlanSchema || m["apply_schema"] != manage.ApplySchema || m["recent_operation_schema"] != manage.RecentOperationSchema {
		t.Fatal(out.String())
	}
}

func TestCommandHelp(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{"mcp", []string{"Usage: keepupdated mcp", "stdio", "default scan locations", "--help"}},
		{"special", []string{"special prepare --id ID", "--package PATH", "--app-root PATH", "special status", "special register-helper", "special operation-status ID"}},
		{"self-update", []string{"self-update check --app PATH", "self-update prepare --check PATH --pid PID", "self-update handoff --plan PATH --confirm ID", "self-update inspect --plan PATH --operation ID --app PATH", "downloads and stages", "after the consenting app exits"}},
	} {
		for _, help := range []string{"--help", "-h"} {
			t.Run(tc.command+"/"+help, func(t *testing.T) {
				var out, errOut bytes.Buffer
				if code := run([]string{tc.command, help}, &out, &errOut); code != 0 {
					t.Fatal(code, errOut.String())
				}
				if out.Len() != 0 {
					t.Fatal("help produced operation output", out.String())
				}
				for _, want := range tc.want {
					if !strings.Contains(errOut.String(), want) {
						t.Fatalf("missing %q in help: %s", want, errOut.String())
					}
				}
			})
		}
	}
}

func TestCommandHelpDoesNotAcceptInvalidArguments(t *testing.T) {
	for _, command := range []string{"mcp", "special", "self-update"} {
		for _, args := range [][]string{{"--invalid"}, {"extra"}, {"--help", "extra"}, {"-h", "extra"}, {"extra", "--help"}} {
			var out, errOut bytes.Buffer
			if code := run(append([]string{command}, args...), &out, &errOut); code != 2 || out.Len() != 0 {
				t.Fatal(command, args, code, out.String(), errOut.String())
			}
		}
	}
	for _, args := range [][]string{
		{"special"}, {"special", "prepare"}, {"special", "operation-status"},
		{"special", "status", "extra"}, {"special", "register-helper", "extra"},
		{"self-update"}, {"self-update", "check"}, {"self-update", "prepare"},
		{"self-update", "handoff"}, {"self-update", "inspect"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 || out.Len() != 0 {
			t.Fatal(args, code, out.String(), errOut.String())
		}
	}
}

func TestInvalidArgumentsCannotStartQueries(t *testing.T) {
	for _, args := range [][]string{{}, {"apply"}, {"scan", "--online"}, {"check", "--system-updates"}, {"check", "--timeout", "0s"}, {"check", "--format", "yaml"}, {"check", "--store-country", "A1"}, {"version", "extra"}, {"version", "--format", "table"}, {"scan", "--project", ""}} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 || out.Len() != 0 {
			t.Fatal(args, code, out.String())
		}
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }
func TestOutputFailureIsReported(t *testing.T) {
	r := audit.Report{Items: []audit.Item{{Name: "fixture"}}}
	for _, format := range []string{"json", "table"} {
		if renderReport(brokenWriter{}, format, r) == nil {
			t.Fatal(format)
		}
	}
}
func TestTableIncludesUnknownHealthAndDiagnostic(t *testing.T) {
	r := audit.Report{Items: []audit.Item{{Name: "fixture\x1b[2J", Update: audit.Result{Status: "unknown", Code: "offline"}, Health: audit.Result{Status: "error", Code: "metadata_unreadable"}}}, Summary: map[string]int{"unknown": 1}, Diagnostics: []audit.Diagnostic{{Adapter: "apps", Code: "walk_failed", Path: "/example/path"}}}
	var b bytes.Buffer
	if err := renderReport(&b, "table", r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"unknown: 1", "metadata_unreadable", "walk_failed"} {
		if !strings.Contains(b.String(), want) {
			t.Fatal(b.String())
		}
	}
	if strings.Contains(b.String(), "\x1b") {
		t.Fatal("terminal control character escaped quoting")
	}
}
func TestExitCodeDoesNotTreatUnknownAsLatest(t *testing.T) {
	r := audit.Report{Items: []audit.Item{{Update: audit.Result{Status: "unknown"}, Health: audit.Result{Status: "unknown"}}}}
	if reportExit(r) != 0 {
		t.Fatal("unknown is not command failure")
	}
	r.Items[0].Health.Status = "error"
	if reportExit(r) != 1 {
		t.Fatal("health error lost")
	}
}
