package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

// Only the test build injects journal failures. The real CLI, scanner, planner,
// filesystem mutations and journal writer run in separate sandboxed processes.
func TestCLIApplyStorageAndOutputFailures(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("journal permission failure requires an unprivileged user")
	}
	build := t.TempDir()
	source, err := filepath.Abs("../../internal/manage/operation_checkpoint.go")
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	injected := strings.Replace(string(original), "package manage", "package manage\n\nimport \"os\"", 1)
	needle := "\tif err := saveRecentOperation(e.StateDir, result); err != nil {"
	if strings.Count(injected, needle) != 1 {
		t.Fatal("journal injection point changed")
	}
	injected = strings.Replace(injected, needle, `
	failure := os.Getenv("KEEPUPDATED_TEST_JOURNAL_FAILURE")
	first := result.Results[0]
	if failure == "initial" ||
		failure == "before_item" && first.Status == "verification_unknown" ||
		failure == "after_first" && first.Status == "uninstalled" ||
		failure == "final" && result.State == "finished" {
		if err := os.Chmod(e.StateDir, 0500); err != nil { return err }
	}
`+needle, 1)
	replacement := filepath.Join(build, "checkpoint.go")
	writeApplyFixture(t, replacement, injected)
	overlay, _ := json.Marshal(map[string]any{"Replace": map[string]string{source: replacement}})
	overlayPath := filepath.Join(build, "overlay.json")
	writeApplyFixture(t, overlayPath, string(overlay))
	binary := filepath.Join(build, "keepupdated")
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin/go"), "build", "-overlay", overlayPath, "-o", binary, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build CLI fixture: %v\n%s", err, out)
	}

	for _, failure := range []string{"initial", "before_item", "after_first", "final", "stdout", "stderr", "both"} {
		t.Run(failure, func(t *testing.T) {
			outputFailure := failure == "stdout" || failure == "stderr" || failure == "both"
			home, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(home, "skills")
			for _, name := range []string{"first", "later"} {
				writeApplyFixture(t, filepath.Join(root, name, "SKILL.md"), "---\nname: "+name+"\ndescription: Synthetic fixture\n---\n# Fixture\n")
			}
			homeJSON, _ := json.Marshal(home)
			binaryJSON, _ := json.Marshal(binary)
			profile := filepath.Join(home, "fixture.sb")
			writeApplyFixture(t, profile, `(version 1)
(allow default)
(deny network*)
(deny file-read* (subpath "/Applications") (subpath "/opt") (subpath "/usr/local") (subpath "/Users") (subpath "/Library"))
(allow file-read* (subpath `+string(homeJSON)+`) (literal `+string(binaryJSON)+`))
(deny file-write*)
(allow file-write* (subpath `+string(homeJSON)+`) (literal "/dev/null"))
(deny process-exec (literal "/bin/ps") (literal "/bin/launchctl"))
`)
			call := func(inject bool, args ...string) ([]byte, string, int) {
				t.Helper()
				cmd := exec.Command("/usr/bin/sandbox-exec", append([]string{"-f", profile, binary}, args...)...)
				cmd.Dir = home
				cmd.Env = []string{"HOME=" + home, "PATH=" + filepath.Join(home, "empty-bin"), "TMPDIR=" + home}
				var out, diagnostics bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &diagnostics
				if inject {
					cmd.Env = append(cmd.Env, "KEEPUPDATED_TEST_JOURNAL_FAILURE="+failure)
					for _, stream := range []string{"stdout", "stderr"} {
						if failure != stream && failure != "both" {
							continue
						}
						reader, writer, err := os.Pipe()
						if err != nil {
							t.Fatal(err)
						}
						reader.Close()
						defer writer.Close()
						if stream == "stdout" {
							cmd.Stdout = writer
						} else {
							cmd.Stderr = writer
						}
					}
				}
				code := 0
				if err := cmd.Run(); err != nil {
					if exit, ok := err.(*exec.ExitError); ok {
						code = exit.ExitCode()
					} else {
						t.Fatal(err)
					}
				}
				return out.Bytes(), diagnostics.String(), code
			}
			decode := func(data []byte, value any) {
				t.Helper()
				if err := json.Unmarshal(data, value); err != nil {
					t.Fatalf("decode %q: %v", data, err)
				}
			}
			data, diagnostics, code := call(false, "scan", "--skills-root", root)
			var report audit.Report
			decode(data, &report)
			ids := []string{}
			for _, item := range report.Items {
				if item.Kind == "skill" && strings.HasPrefix(item.Path, root+"/") {
					ids = append(ids, item.ID)
				} else if item.Kind != "os" {
					t.Fatalf("host installation escaped isolation: %s", item.Kind)
				}
			}
			if len(ids) != 2 {
				t.Fatalf("fixture scan: %d %s %s", code, diagnostics, data)
			}
			data, diagnostics, code = call(false, "plan", "--action", "uninstall", "--id", ids[0], "--id", ids[1], "--skills-root", root)
			var plan manage.Plan
			decode(data, &plan)
			if code != 0 || len(plan.Items) != 2 || plan.Items[0].Status != "ready" || plan.Items[1].Status != "ready" {
				t.Fatalf("preview: %d %s %s", code, diagnostics, data)
			}
			planPath := filepath.Join(home, "plan.json")
			writeApplyFixture(t, planPath, string(data))
			args := []string{"apply", "--plan", planPath, "--confirm", plan.PlanID, "--skills-root", root, "--progress"}
			data, diagnostics, code = call(false, "apply", "--plan", planPath)
			if code != 2 || len(data) != 0 {
				t.Fatalf("argument error: %d %s %s", code, diagnostics, data)
			}
			state := manage.ActionStateDir(home)
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatal("argument error created operation state", err)
			}
			data, diagnostics, code = call(true, args...)
			if err := os.Chmod(state, 0700); err != nil {
				t.Fatal(err)
			}
			wantCode := 1
			if failure == "initial" {
				wantCode = 2
			}
			if code != wantCode || (failure != "stderr" && failure != "both") && (!strings.Contains(diagnostics, plan.PlanID) || !strings.Contains(diagnostics, "do not retry this plan")) {
				t.Fatalf("failure %s: exit %d, stderr %s, stdout %s", failure, code, diagnostics, data)
			}
			if _, err := os.Stat(filepath.Join(state, plan.PlanID+".attempted")); err != nil {
				t.Fatal("lost attempt claim", err)
			}
			var result manage.ApplyResult
			if !outputFailure {
				decode(data, &result)
				if result.Schema != manage.ApplySchema || result.PlanID != plan.PlanID || result.State != "in_progress" || result.FinishedAt != "" || !reflect.DeepEqual(result.OriginalIDs, []string{plan.Items[0].ID, plan.Items[1].ID}) || len(result.Results) != 2 {
					t.Fatalf("lost operation identity/state: %+v", result)
				}
				if !strings.Contains(diagnostics, "result persistence is unconfirmed") {
					t.Fatal("missing persistence diagnostic", diagnostics)
				}
			} else if failure == "stdout" && (!strings.Contains(diagnostics, "could not write apply result") || strings.Contains(diagnostics, "persistence is unconfirmed")) {
				t.Fatal("stdout failure confused with journal failure", diagnostics)
			}
			if failure == "stderr" {
				decode(data, &result)
			}
			data, diagnostics, code = call(false, "results")
			var record manage.RecentOperation
			decode(data, &record)
			if code != 0 {
				t.Fatal("saved results unreadable", diagnostics)
			}
			if failure == "initial" {
				if record.Operation != nil {
					t.Fatal("initial failed write produced a record")
				}
			} else if record.Operation == nil || record.Operation.PlanID != plan.PlanID || !reflect.DeepEqual(record.Operation.OriginalIDs, []string{plan.Items[0].ID, plan.Items[1].ID}) {
				t.Fatalf("lost saved identity: %+v", record)
			}
			for i, item := range plan.Items {
				mutated := failure == "final" || outputFailure || failure == "after_first" && i == 0
				_, err := os.Stat(filepath.Join(root, item.Name, "SKILL.md"))
				if mutated != os.IsNotExist(err) || !mutated && err != nil {
					t.Fatalf("item %d mutation=%v: %v", i, mutated, err)
				}
				if !outputFailure {
					want := "operation_not_started"
					if mutated {
						want = "uninstalled"
					} else if failure == "before_item" && i == 0 {
						want = "verification_unknown"
					}
					r := result.Results[i]
					if r.ID != item.ID || (r.Status != want && r.Reason != want) || r.Executed != mutated {
						t.Fatalf("incorrect stdout item %d: %+v", i, r)
					}
				}
			}
			if failure == "after_first" && (record.Operation.State != "in_progress" || record.Operation.Results[0].Status != "verification_unknown" || record.Operation.Results[1].Reason != "operation_not_started") {
				t.Fatalf("saved checkpoint implied completion: %+v", record.Operation)
			}
			if failure == "final" && (record.Operation.State != "in_progress" || record.Operation.Results[1].Status != "uninstalled") {
				t.Fatalf("failed final save lost observations: %+v", record.Operation)
			}
			if outputFailure && (record.Operation.State != "finished" || record.Operation.Results[0].Status != "uninstalled" || record.Operation.Results[1].Status != "uninstalled") {
				t.Fatalf("output failure lost durable results: %+v", record.Operation)
			}
			if failure == "stderr" && !reflect.DeepEqual(record.Operation, &result) {
				t.Fatal("stderr failure changed stdout results", result, record.Operation)
			}
			before := string(data)
			if data, diagnostics, code = call(false, args...); code != 2 || len(data) != 0 {
				t.Fatalf("old plan replayed: %d %s %s", code, diagnostics, data)
			}
			data, diagnostics, code = call(false, "results")
			if code != 0 || string(data) != before {
				t.Fatal("replay changed saved results", diagnostics)
			}
		})
	}
}

func writeApplyFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
