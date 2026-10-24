package manage

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecRunnerCancellationStopsDelayedDescendants(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		for _, inheritedOutput := range []bool{false, true} {
			name := "cancel"
			if deadline {
				name = "deadline"
			}
			if inheritedOutput {
				name += "/inherited-output"
			} else {
				name += "/redirected-output"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				ctx, cancel := context.WithCancel(context.Background())
				deadlineAt := time.Now().Add(3 * time.Second)
				expected := context.Canceled
				if deadline {
					cancel()
					ctx, cancel = context.WithDeadline(context.Background(), deadlineAt)
					expected = context.DeadlineExceeded
				}
				defer cancel()
				redirect := " >/dev/null 2>&1"
				if inheritedOutput {
					redirect = ""
				}
				// Both generations ignore catchable termination signals. Only these fixture
				// processes can write the marker, 600 ms after the test arms them.
				script := `trap '' TERM INT HUP
(/bin/sh -c 'trap "" TERM INT HUP
echo ready > ready
while [ ! -f go ]; do /bin/sleep 0.01; done
echo armed > armed
/bin/sleep 0.6
echo survived > marker')` + redirect + ` &
wait`
				done := make(chan error, 1)
				go func() {
					_, err := (ExecRunner{}).Run(ctx, Command{Path: "/bin/sh", Args: []string{"-c", script}, Dir: dir, Env: []string{"PATH=/usr/bin:/bin"}})
					done <- err
				}()
				waitRunnerFile(t, filepath.Join(dir, "ready"))
				if deadline {
					time.Sleep(time.Until(deadlineAt.Add(-150 * time.Millisecond)))
				}
				if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				waitRunnerFile(t, filepath.Join(dir, "armed"))
				if !deadline {
					cancel()
				}
				select {
				case err := <-done:
					if !errors.Is(err, expected) {
						t.Fatalf("error = %v, want %v", err, expected)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("runner did not return after cancellation")
				}
				time.Sleep(800 * time.Millisecond)
				if _, err := os.Stat(filepath.Join(dir, "marker")); !os.IsNotExist(err) {
					t.Fatalf("descendant wrote after cancellation: %v", err)
				}
			})
		}
	}
}

func waitRunnerFile(t *testing.T, path string) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("fixture did not become ready")
}

func TestExecRunnerResults(t *testing.T) {
	t.Setenv("KEEPUPDATED_RUNNER_INHERITED", "must-not-leak")
	for _, tc := range []struct{ name, script, output, reason string }{
		{"success", `printf '%s' "$EXPLICIT:$KEEPUPDATED_RUNNER_INHERITED"`, "value:", ""},
		{"failure", `printf result; printf 'Permission denied PRIVATE' >&2; exit 7`, "result", "owner_permission_denied"},
		{"predicate", `exit 2`, "", "owner_query_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := (ExecRunner{}).Run(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", tc.script}, Env: []string{"PATH=/usr/bin:/bin", "EXPLICIT=value"}, Dir: t.TempDir()})
			if string(out) != tc.output {
				t.Fatalf("output = %q", out)
			}
			if tc.reason == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || queryFailureReason(err) != tc.reason || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("error = %v", err)
			}
			if tc.name == "predicate" && !gitPredicateExit(err, 2) {
				t.Fatalf("lost exit status: %v", err)
			}
		})
	}
}

func TestExecRunnerCancellationAfterLeaderExit(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (ExecRunner{}).Run(ctx, Command{Path: "/bin/sh", Args: []string{"-c", `echo $$ > leader.tmp; /bin/mv leader.tmp leader
(/bin/sleep 0.6; echo survived > marker) &
exit 0`}, Dir: dir, Env: []string{"PATH=/usr/bin:/bin"}})
		done <- err
	}()
	waitRunnerFile(t, filepath.Join(dir, "leader"))
	raw, err := os.ReadFile(filepath.Join(dir, "leader"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		t.Fatalf("invalid fixture PID: %q", raw)
	}
	until := time.Now().Add(3 * time.Second)
	for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		if time.Now().After(until) {
			t.Fatal("leader did not exit")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not return")
	}
	time.Sleep(800 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "marker")); !os.IsNotExist(err) {
		t.Fatalf("orphan wrote after cancellation: %v", err)
	}
	if err := syscall.Kill(-pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("group exit not confirmed: %v", err)
	}
}

func TestExecRunnerExitCancellationRaceLeavesOtherProcessesAlone(t *testing.T) {
	// This sibling deliberately shares the test caller's process group.
	dir := t.TempDir()
	sibling := exec.Command("/bin/sh", "-c", `/bin/sleep 0.5; echo survived > marker`)
	sibling.Dir = dir
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	defer sibling.Wait()
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); time.Sleep(time.Millisecond); cancel() }()
		_, err := (ExecRunner{}).Run(ctx, Command{Path: "/bin/sh", Args: []string{"-c", "exit 0"}, Env: []string{"PATH=/usr/bin:/bin"}})
		<-done
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
	waitRunnerFile(t, filepath.Join(dir, "marker"))
}

func TestExecRunnerCanceledBeforeStart(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (ExecRunner{}).Run(ctx, Command{Path: "/bin/sh", Args: []string{"-c", "echo unexpected > marker"}, Dir: dir, Env: []string{"PATH=/usr/bin:/bin"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); !os.IsNotExist(err) {
		t.Fatalf("canceled command started: %v", err)
	}
}

func TestExecRunnerOutputLimit(t *testing.T) {
	out, err := (ExecRunner{}).Run(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", "/usr/bin/head -c 1048577 /dev/zero"}, Env: []string{"PATH=/usr/bin:/bin"}})
	if out != nil || err == nil || err.Error() != "manager output exceeded limit" {
		t.Fatalf("output length=%d, error=%v", len(out), err)
	}
}

func TestExecRunnerInheritedPipeWaitLimitStopsGroup(t *testing.T) {
	for _, code := range []int{0, 7} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			dir := t.TempDir()
			_, err := (ExecRunner{}).Run(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", `(/bin/sleep 3; echo survived > marker) &
exit ` + strconv.Itoa(code)}, Dir: dir, Env: []string{"PATH=/usr/bin:/bin"}})
			if err == nil || queryFailureReason(err) != "owner_query_failed" {
				t.Fatalf("pipe wait must not report success: %v", err)
			}
			if code != 0 && !gitPredicateExit(err, code) {
				t.Fatalf("lost original exit status %d: %v", code, err)
			}
			time.Sleep(1200 * time.Millisecond)
			if _, err := os.Stat(filepath.Join(dir, "marker")); !os.IsNotExist(err) {
				t.Fatalf("descendant survived pipe wait limit: %v", err)
			}
		})
	}
}
