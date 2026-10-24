//go:build darwin

package manage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

func recoveryFixture(t *testing.T) (*selfFixture, SelfUpdatePlan) {
	t.Helper()
	f := selfSetup(t)
	p, e := f.u.prepare(context.Background(), f.check(), f.instance.PID)
	if e != nil {
		t.Fatal(e)
	}
	return f, p
}
func inspectFixture(f *selfFixture, p SelfUpdatePlan) SelfUpdateInspection {
	return f.u.inspect(context.Background(), filepath.Join(p.Directory, "plan.json"), p.ID, p.Check.Path)
}
func recoverySwap(t *testing.T, p SelfUpdatePlan, witness bool) {
	t.Helper()
	backup := filepath.Join(p.Directory, "original.app")
	if e := os.Rename(p.Staged, backup); e != nil {
		t.Fatal(e)
	}
	if e := exchangeApplications(p.Check.Path, backup); e != nil {
		t.Fatal(e)
	}
	if witness {
		if e := writeSelfJSON(filepath.Join(p.Directory, "exchanged.json"), SelfUpdateExchange{p.ID, p.Check.Path, p.TargetIdentity, p.CandidateIdentity}); e != nil {
			t.Fatal(e)
		}
	}
}
func recoveryFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	e := filepath.Walk(root, func(path string, st os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if st.Mode().IsRegular() {
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			result[path] = string(data)
		} else {
			result[path] = st.Mode().String()
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return result
}
func TestSelfUpdateInspectionInterruptionsAreReadOnly(t *testing.T) {
	for _, scenario := range []string{"before", "after", "restored", "relocated", "missing-original", "edited", "identity", "forged-record", "forged-witness", "wrong-op", "wrong-target", "legacy", "expired", "signature", "symlink", "permissions"} {
		t.Run(scenario, func(t *testing.T) {
			f, p := recoveryFixture(t)
			want := "unknown"
			switch scenario {
			case "before":
				want = "not_replaced"
			case "after", "restored", "missing-original", "edited", "identity":
				recoverySwap(t, p, true)
				switch scenario {
				case "after":
					want = "candidate_installed"
				case "restored":
					if e := exchangeApplications(p.Check.Path, filepath.Join(p.Directory, "original.app")); e != nil {
						t.Fatal(e)
					}
					want = "original_restored"
				case "missing-original":
					if e := os.Rename(filepath.Join(p.Directory, "original.app"), filepath.Join(p.Directory, "preserved.app")); e != nil {
						t.Fatal(e)
					}
				case "edited":
					if e := os.WriteFile(filepath.Join(p.Check.Path, "edited"), []byte("edited"), 0600); e != nil {
						t.Fatal(e)
					}
				case "identity":
					// Identical bytes in a different directory cannot impersonate the saved tree.
					backup := filepath.Join(p.Directory, "original.app")
					if e := os.Rename(backup, backup+".saved"); e != nil {
						t.Fatal(e)
					}
					if e := os.Mkdir(backup, 0700); e != nil {
						t.Fatal(e)
					}
					entries, e := os.ReadDir(backup + ".saved")
					if e != nil {
						t.Fatal(e)
					}
					for _, entry := range entries {
						if e := os.Rename(filepath.Join(backup+".saved", entry.Name()), filepath.Join(backup, entry.Name())); e != nil {
							t.Fatal(e)
						}
					}
				}
			case "relocated":
				if e := os.Rename(p.Staged, filepath.Join(p.Directory, "original.app")); e != nil {
					t.Fatal(e)
				}
			case "forged-record":
				if e := writeSelfJSON(filepath.Join(p.Directory, "status.json"), SelfUpdateState{Schema: SelfUpdateSchema, ID: "other", Status: "reopened"}); e != nil {
					t.Fatal(e)
				}
			case "forged-witness":
				if e := writeSelfJSON(filepath.Join(p.Directory, "exchanged.json"), SelfUpdateExchange{p.ID, p.Check.Path, p.TargetIdentity, p.CandidateIdentity}); e != nil {
					t.Fatal(e)
				}
			case "legacy", "expired":
				if scenario == "legacy" {
					p.RecoveryVersion = 0
				} else {
					p.Expires = time.Now().Add(-time.Hour)
					want = "not_replaced"
				}
				p.ID = selfPlanID(p)
				if e := writeSelfJSON(filepath.Join(p.Directory, "plan.json"), p); e != nil {
					t.Fatal(e)
				}
				if e := writeSelfState(p, "prepared", "", ""); e != nil {
					t.Fatal(e)
				}
				if scenario == "expired" {
					if _, e := ReadSelfUpdatePlan(filepath.Join(p.Directory, "plan.json"), p.ID); e == nil {
						t.Fatal("expired execution accepted")
					}
					if _, e := StartSelfUpdate(context.Background(), filepath.Join(p.Directory, "plan.json"), p.ID); e == nil {
						t.Fatal("expired handoff accepted")
					}
					if e := FinishSelfUpdate(context.Background(), filepath.Join(p.Directory, "plan.json"), p.ID); e == nil {
						t.Fatal("expired finish accepted")
					}
				}
			case "signature":
				f.signatureFailure = true
			case "symlink":
				status := filepath.Join(p.Directory, "status.json")
				if e := os.Rename(status, status+".saved"); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(status+".saved", status); e != nil {
					t.Fatal(e)
				}
			case "permissions":
				if e := os.Chmod(filepath.Join(p.Directory, "status.json"), 0644); e != nil {
					t.Fatal(e)
				}
			}
			before := recoveryFiles(t, filepath.Dir(p.Directory))
			id, target := p.ID, p.Check.Path
			if scenario == "wrong-op" {
				id = "wrong"
			}
			if scenario == "wrong-target" {
				target = target + ".other"
			}
			got := f.u.inspect(context.Background(), filepath.Join(p.Directory, "plan.json"), id, target)
			if got.Status != want {
				t.Fatalf("got %+v want %s", got, want)
			}
			if got.ReopenStatus != "not_verified" {
				t.Fatal("installation inferred reopen", got)
			}
			if after := recoveryFiles(t, filepath.Dir(p.Directory)); !reflect.DeepEqual(before, after) {
				t.Fatal("inspection changed files")
			}
		})
	}
}
func TestSelfUpdateInspectionLocksAreObservedWithoutClaiming(t *testing.T) {
	for _, name := range []string{"worker", "target"} {
		t.Run(name, func(t *testing.T) {
			f, p := recoveryFixture(t)
			path := filepath.Join(p.Directory, "worker.lock")
			if name == "target" {
				path = selfUpdateTargetLockPath(p.Check.Path)
			}
			file, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
			if e != nil {
				t.Fatal(e)
			}
			defer file.Close()
			if e = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
				t.Fatal(e)
			}
			if name == "worker" {
				if e := writeSelfJSON(filepath.Join(p.Directory, "worker.json"), SelfUpdateState{Schema: SelfUpdateSchema, ID: p.ID, Target: p.Check.Path, Status: "claimed"}); e != nil {
					t.Fatal(e)
				}
			}
			before := recoveryFiles(t, filepath.Dir(p.Directory))
			got := inspectFixture(f, p)
			if got.Status != "busy" {
				t.Fatal(got)
			}
			if e = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); e != nil {
				t.Fatal(e)
			}
			got = inspectFixture(f, p)
			if got.Status != "not_replaced" {
				t.Fatal(got)
			}
			// A normal worker can still acquire its original lock after inspection.
			if e = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(before, recoveryFiles(t, filepath.Dir(p.Directory))) {
				t.Fatal("lock observation wrote files")
			}
		})
	}
}
func TestSelfUpdateInspectionRejectsChangesDuringVerification(t *testing.T) {
	f, p := recoveryFixture(t)
	runner := f.u.runner
	f.u.runner = runFunc(func(ctx context.Context, c Command) ([]byte, error) {
		if c.Path == "/usr/sbin/spctl" {
			if e := os.WriteFile(filepath.Join(p.Staged, "changed"), []byte("changed"), 0600); e != nil {
				return nil, e
			}
		}
		return runner.Run(ctx, c)
	})
	if got := inspectFixture(f, p); got.Status != "unknown" {
		t.Fatal(got)
	}
}
func TestSelfUpdateInspectionSeparatesSavedReopen(t *testing.T) {
	f, p := recoveryFixture(t)
	recoverySwap(t, p, false)
	if e := writeSelfResult(p, SelfUpdateState{Status: "reopened", Verification: "verified", ReopenStatus: "reopened"}); e != nil {
		t.Fatal(e)
	}
	if got := inspectFixture(f, p); got.Status != "candidate_installed" || got.ReopenStatus != "recorded_reopened" {
		t.Fatal(got)
	}
	// Status alone cannot establish a current installation.
	if e := os.WriteFile(filepath.Join(p.Check.Path, "changed"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	if got := inspectFixture(f, p); got.Status != "unknown" || got.ReopenStatus != "not_verified" {
		t.Fatal(got)
	}
}
func TestSelfUpdateFinishRecordsRestorationWitness(t *testing.T) {
	f, p := recoveryFixture(t)
	runner := f.u.runner
	f.u.runner = runFunc(func(ctx context.Context, c Command) ([]byte, error) {
		if c.Path == "/usr/sbin/spctl" && c.Args[len(c.Args)-1] == p.Check.Path {
			b, _ := os.ReadFile(filepath.Join(p.Check.Path, "Contents/Info.plist"))
			var info applicationInfo
			_ = json.Unmarshal(b, &info)
			if info.Build == p.TargetBuild {
				return nil, errors.New("controlled candidate failure")
			}
		}
		return runner.Run(ctx, c)
	})
	done := make(chan error, 1)
	go func() { done <- f.u.finish(context.Background(), p) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, e := os.Stat(filepath.Join(p.Directory, "ready.json")); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := inspectFixture(f, p); got.Status != "busy" {
		t.Fatal(got)
	}
	if e := writeSelfJSON(filepath.Join(p.Directory, "exit-approved.json"), map[string]string{"id": p.ID}); e != nil {
		t.Fatal(e)
	}
	f.running.Store(false)
	if e := <-done; e == nil {
		t.Fatal("fixture should restore")
	}
	f.u.runner = runner
	if got := inspectFixture(f, p); got.Status != "original_restored" || !got.WorkerStopped {
		t.Fatal(got)
	}
}

// The child intentionally exits with the status record untouched at each boundary.
func TestSelfUpdateInspectionInterruptedProcess(t *testing.T) {
	path := os.Getenv("KEEPUPDATED_INSPECTION_FIXTURE_PLAN")
	if path != "" {
		data, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var p SelfUpdatePlan
		if e = json.Unmarshal(data, &p); e != nil {
			t.Fatal(e)
		}
		switch os.Getenv("KEEPUPDATED_INSPECTION_FIXTURE_PHASE") {
		case "after":
			recoverySwap(t, p, true)
		case "restored":
			recoverySwap(t, p, true)
			if e = exchangeApplications(p.Check.Path, filepath.Join(p.Directory, "original.app")); e != nil {
				t.Fatal(e)
			}
		}
		os.Exit(0)
	}
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	for phase, want := range map[string]string{"before": "not_replaced", "after": "candidate_installed", "restored": "original_restored"} {
		t.Run(phase, func(t *testing.T) {
			f, p := recoveryFixture(t)
			child := exec.Command(binary, "-test.run=^TestSelfUpdateInspectionInterruptedProcess$")
			child.Env = append(os.Environ(), "KEEPUPDATED_INSPECTION_FIXTURE_PLAN="+filepath.Join(p.Directory, "plan.json"), "KEEPUPDATED_INSPECTION_FIXTURE_PHASE="+phase)
			if output, e := child.CombinedOutput(); e != nil {
				t.Fatalf("interruption fixture: %v %s", e, output)
			}
			before := recoveryFiles(t, filepath.Dir(p.Directory))
			if got := inspectFixture(f, p); got.Status != want {
				t.Fatal(got)
			}
			if !reflect.DeepEqual(before, recoveryFiles(t, filepath.Dir(p.Directory))) {
				t.Fatal("inspection changed interruption evidence")
			}
		})
	}
}
