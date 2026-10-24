package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

// Run each command in a fresh process, with host installations hidden and writes
// confined to the fixture. This exercises CLI persistence rather than an in-memory runner.
func TestCLIIsolatedUninstallRecovery(t *testing.T) {
	if os.Getenv("KEEPUPDATED_CLI_ACCEPTANCE_CHILD") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Exit(run(os.Args[i+1:], os.Stdout, os.Stderr))
			}
		}
		os.Exit(99)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(home, "skills")
	skill := filepath.Join(root, "fixture", "SKILL.md")
	write(skill, "---\nname: fixture\ndescription: Synthetic acceptance fixture\n---\n# Fixture\n")
	profile := filepath.Join(home, "fixture.sb")
	homeJSON, _ := json.Marshal(home)
	executableJSON, _ := json.Marshal(os.Args[0])
	write(profile, `(version 1)
(allow default)
(deny network*)
(deny file-read* (subpath "/Applications") (subpath "/opt") (subpath "/usr/local") (subpath "/Users") (subpath "/Library"))
(allow file-read* (subpath `+string(homeJSON)+`) (literal `+string(executableJSON)+`))
(deny file-write*)
(allow file-write* (subpath `+string(homeJSON)+`) (literal "/dev/null"))
(deny process-exec (literal "/bin/ps") (literal "/bin/launchctl"))
`)
	call := func(args ...string) ([]byte, int) {
		t.Helper()
		cmd := exec.Command("/usr/bin/sandbox-exec", append([]string{"-f", profile, os.Args[0], "-test.run=^TestCLIIsolatedUninstallRecovery$", "--"}, args...)...)
		cmd.Dir = home
		cmd.Env = []string{"HOME=" + home, "PATH=" + filepath.Join(home, "empty-bin"), "TMPDIR=" + home, "KEEPUPDATED_CLI_ACCEPTANCE_CHILD=1"}
		var diagnostics strings.Builder
		cmd.Stderr = &diagnostics
		out, err := cmd.Output()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if code > 2 || code < 0 {
			t.Fatalf("command %v: exit %d: %s", args, code, diagnostics.String())
		}
		return out, code
	}
	decode := func(data []byte, value any) {
		t.Helper()
		if err := json.Unmarshal(data, value); err != nil {
			t.Fatalf("decode %q: %v", data, err)
		}
	}
	data, _ := call("check", "--skills-root", root)
	var report audit.Report
	decode(data, &report)
	id := ""
	for _, item := range report.Items {
		if item.Path == skill {
			id = item.ID
			if item.Update.Status == "current" {
				t.Fatal("offline unknown source reported as current")
			}
		} else if item.Kind != "os" {
			t.Fatalf("host installation escaped isolation: %s", item.Kind)
		}
	}
	if id == "" {
		t.Fatal("fixture missing")
	}
	preview := func() manage.Plan {
		t.Helper()
		data, code := call("plan", "--action", "uninstall", "--id", id, "--skills-root", root)
		var plan manage.Plan
		decode(data, &plan)
		if code != 0 || len(plan.Items) != 1 || plan.Items[0].Status != "ready" {
			t.Fatalf("preview: %d %s", code, data)
		}
		write(filepath.Join(home, "plan.json"), string(data))
		return plan
	}
	plan := preview()
	if _, code := call("apply", "--plan", filepath.Join(home, "plan.json"), "--skills-root", root); code != 2 {
		t.Fatal("missing confirmation accepted")
	}
	if data, err := os.ReadFile(skill); err != nil || !strings.Contains(string(data), "name: fixture") {
		t.Fatal("unconfirmed operation changed fixture", err)
	}
	if _, err := os.Stat(manage.ActionStateDir(home)); !os.IsNotExist(err) {
		t.Fatal("unconfirmed operation created action state", err)
	}
	write(skill, "# Changed after preview\n")
	if _, code := call("apply", "--plan", filepath.Join(home, "plan.json"), "--confirm", plan.PlanID, "--skills-root", root); code == 0 {
		t.Fatal("changed content accepted")
	}
	if _, err := os.Stat(skill); err != nil {
		t.Fatal("rejected operation removed fixture", err)
	}
	plan = preview()
	data, code := call("apply", "--plan", filepath.Join(home, "plan.json"), "--confirm", plan.PlanID, "--skills-root", root)
	var result manage.ApplyResult
	decode(data, &result)
	if code != 0 || len(result.Results) != 1 || result.Results[0].Status != "uninstalled" {
		t.Fatalf("apply: %d %s", code, data)
	}
	data, code = call("results", "--format", "json")
	var record manage.RecentOperation
	decode(data, &record)
	if code != 0 || record.Operation == nil || record.Operation.PlanID != plan.PlanID || record.Operation.Results[0].Status != "uninstalled" || record.Operation.Results[0].BackupPath == "" {
		t.Fatalf("recovery: %d %s", code, data)
	}
	if _, err := os.Stat(record.Operation.Results[0].BackupPath); err != nil {
		t.Fatal("recovery location missing", err)
	}
	if data, err := os.ReadFile(filepath.Join(record.Operation.Results[0].BackupPath, "SKILL.md")); err != nil || string(data) != "# Changed after preview\n" {
		t.Fatal("recovery content differs", err)
	}
	before := string(data)
	if _, code := call("apply", "--plan", filepath.Join(home, "plan.json"), "--confirm", plan.PlanID, "--skills-root", root); code == 0 {
		t.Fatal("old plan replayed")
	}
	data, code = call("results")
	if code != 0 || string(data) != before {
		t.Fatal("rejected replay changed saved result")
	}
}
