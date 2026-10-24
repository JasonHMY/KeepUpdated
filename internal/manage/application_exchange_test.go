package manage

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Exit from the child directly around the actual kernel exchange, bypassing
// defers as a process interruption would. Only parent-created temporary apps
// are touched; no installed application is started or terminated.
func TestApplicationExchangeInterruptionHelper(t *testing.T) {
	root := os.Getenv("KEEPUPDATED_SWAP_TEST_ROOT")
	if root == "" {
		return
	}
	phase := os.Getenv("KEEPUPDATED_SWAP_TEST_PHASE")
	target, stage, backup := filepath.Join(root, "Target.app"), filepath.Join(root, "Stage.app"), filepath.Join(root, "Backup.app")
	oldHash, e := applicationTreeHash(context.Background(), target)
	if e != nil {
		t.Fatal(e)
	}
	newHash, e := applicationTreeHash(context.Background(), stage)
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	exchange := func(a, b string) error {
		count++
		if phase == "before-swap" && count == 1 {
			os.Exit(0)
		}
		if e := exchangeApplications(a, b); e != nil {
			return e
		}
		if phase == "after-swap" && count == 1 || phase == "after-rollback" && count == 2 {
			os.Exit(0)
		}
		return nil
	}
	_, _, _ = replaceApplication(context.Background(), target, stage, backup, oldHash, newHash, func(string) error { return errors.New("controlled post-verification failure") }, exchange)
	t.Fatal("interruption boundary not reached")
}

func TestApplicationAtomicExchangeSurvivesProcessInterruption(t *testing.T) {
	for _, phase := range []string{"before-swap", "after-swap", "after-rollback"} {
		t.Run(phase, func(t *testing.T) {
			f := setup(t)
			f.write(filepath.Join(f.home, "Target.app/content"), "original", 0644)
			f.write(filepath.Join(f.home, "Stage.app/content"), "candidate", 0644)
			executable, e := os.Executable()
			if e != nil {
				t.Fatal(e)
			}
			cmd := exec.Command(executable, "-test.run=^TestApplicationExchangeInterruptionHelper$")
			cmd.Env = []string{"KEEPUPDATED_SWAP_TEST_ROOT=" + f.home, "KEEPUPDATED_SWAP_TEST_PHASE=" + phase}
			if b, e := cmd.CombinedOutput(); e != nil {
				t.Fatalf("child: %s %v", b, e)
			}
			installed, e := os.ReadFile(filepath.Join(f.home, "Target.app/content"))
			if e != nil {
				t.Fatal("application path missing after interruption", e)
			}
			retained, e := os.ReadFile(filepath.Join(f.home, "Backup.app/content"))
			if e != nil {
				t.Fatal("exchange partner missing", e)
			}
			if phase == "after-swap" {
				if string(installed) != "candidate" || string(retained) != "original" {
					t.Fatal(string(installed), string(retained))
				}
			} else if string(installed) != "original" || string(retained) != "candidate" {
				t.Fatal(string(installed), string(retained))
			}
		})
	}
}
