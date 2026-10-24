package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

// The test build substitutes inventory and time only. The real CLI resolver,
// planner, output encoder, plan-file checks and apply path run in a sandbox.
func TestCLIPlanSerializedSize(t *testing.T) {
	const limit = 16 << 20
	build := t.TempDir()
	source, err := filepath.Abs("manage.go")
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	injected := string(original)
	start := strings.Index(injected, "\tsnapshot := func(ctx context.Context) (audit.Report, error) {")
	end := strings.Index(injected, "\n\tencoder := json.NewEncoder(out)")
	clock := "\tresolver := resolve.Resolver{Options: options, Timeout: c.Timeout}"
	if start < 0 || end < start || strings.Count(injected, clock) != 1 {
		t.Fatal("inventory/time injection points changed")
	}
	injected = injected[:start] + `	snapshot := func(ctx context.Context) (audit.Report, error) {
		var report audit.Report
		data, err := os.ReadFile(filepath.Join(c.Home, "report.json"))
		if err != nil { return report, err }
		err = json.Unmarshal(data, &report)
		return report, err
	}` + injected[end:]
	injected = strings.Replace(injected, clock, "\toptions.Now, _ = time.Parse(time.RFC3339Nano, os.Getenv(\"KEEPUPDATED_TEST_PLAN_TIME\"))\n"+clock, 1)
	replacement := filepath.Join(build, "manage.go")
	writeApplyFixture(t, replacement, injected)
	overlay, _ := json.Marshal(map[string]any{"Replace": map[string]string{source: replacement}})
	overlayPath := filepath.Join(build, "overlay.json")
	writeApplyFixture(t, overlayPath, string(overlay))
	binary := filepath.Join(build, "keepupdated")
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin/go"), "build", "-overlay", overlayPath, "-o", binary, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build CLI fixture: %v\n%s", err, out)
	}
	encode := func(plan manage.Plan) []byte {
		t.Helper()
		var out bytes.Buffer
		encoder := json.NewEncoder(&out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(plan); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	for _, tc := range []struct {
		name      string
		count     int
		wantBytes int
		oversize  bool
	}{
		{"small_escaped", 1, 0, false},
		{"below_limit", 1, limit - 1, false},
		{"at_limit", 1, limit, false},
		{"newline_over_limit", 1, limit + 1, true},
		{"many_manual_items", 9116, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			report := audit.Report{Schema: audit.Schema, Items: make([]audit.Item, tc.count)}
			for i := range report.Items {
				id := fmt.Sprintf("application-%06d", i)
				report.Items[i] = audit.Item{ID: id, Kind: "application", Name: strings.Repeat("A", 160) + fmt.Sprintf("Synthetic App %06d", i), Path: "/Users/synthetic/" + strings.Repeat("SyntheticApplicationDirectory/", 20) + id + ".app", Owner: "app-store", Source: "app-store", Version: "1.0.0"}
			}
			if tc.count == 1 {
				report.Items[0].Name = "Synthetic 世界 \"<>&\"\nApp"
				base, err := manage.NewPlan(report, manage.Selection{Action: "update", All: true}, manage.Options{Home: home, Now: now})
				if err != nil {
					t.Fatal(err)
				}
				if tc.wantBytes != 0 {
					// Padding only the binding keeps apply's saved result small.
					padding := strings.Repeat("x", tc.wantBytes-len(encode(base)))
					report.Items[0].Source += padding
					base.Items[0].Binding.Source += padding
					compact, err := json.Marshal(base)
					if err != nil || len(compact) >= limit || len(encode(base)) != tc.wantBytes {
						t.Fatalf("invalid boundary fixture: compact=%d err=%v", len(compact), err)
					}
				}
			}
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			writeApplyFixture(t, filepath.Join(home, "report.json"), string(data))
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
			call := func(args ...string) ([]byte, string, int) {
				t.Helper()
				cmd := exec.Command("/usr/bin/sandbox-exec", append([]string{"-f", profile, binary}, args...)...)
				cmd.Dir = home
				cmd.Env = []string{"HOME=" + home, "PATH=" + filepath.Join(home, "empty-bin"), "TMPDIR=" + home, "KEEPUPDATED_TEST_PLAN_TIME=" + now.Format(time.RFC3339Nano)}
				var out, diagnostics bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &diagnostics
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
			output, diagnostics, code := call("plan", "--action", "update", "--all")
			if tc.oversize {
				if code != 2 || len(output) != 0 || !strings.Contains(diagnostics, "select fewer items") || !strings.Contains(diagnostics, "fresh preview") {
					t.Fatalf("oversized plan: exit=%d stdout=%d stderr=%s", code, len(output), diagnostics)
				}
				return
			}
			if code != 0 || diagnostics != "" || len(output) > limit || tc.wantBytes != 0 && len(output) != tc.wantBytes {
				t.Fatalf("plan: exit=%d stdout=%d want=%d stderr=%s", code, len(output), tc.wantBytes, diagnostics)
			}
			plan, err := manage.DecodePlan(bytes.NewReader(output))
			if err != nil {
				t.Fatal(err)
			}
			if err := manage.ValidatePlan(plan, time.Now()); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(output, encode(plan)) {
				t.Fatal("CLI changed indentation, escaping or trailing newline")
			}
			planPath := filepath.Join(home, "plan.json")
			writeApplyFixture(t, planPath, string(output))
			resultJSON, diagnostics, code := call("apply", "--plan", planPath, "--confirm", plan.PlanID)
			var result manage.ApplyResult
			if err := json.Unmarshal(resultJSON, &result); err != nil {
				t.Fatalf("apply: exit=%d stderr=%s decode=%v", code, diagnostics, err)
			}
			if code != 1 || diagnostics != "" || result.PlanID != plan.PlanID || result.State != "finished" || !reflect.DeepEqual(result.OriginalIDs, []string{plan.Items[0].ID}) || len(result.Results) != 1 || result.Results[0].Status != "manual" || result.Results[0].Executed {
				t.Fatalf("original plan was not accepted as manual: exit=%d stderr=%s", code, diagnostics)
			}
			t.Logf("%d-byte original CLI output decoded and applied as manual", len(output))
		})
	}
}
