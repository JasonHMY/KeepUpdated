package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

type workerEnvironmentReceipt struct {
	StateHome string                  `json:"state_home"`
	Path      string                  `json:"path"`
	Source    audit.SkillUpdateSource `json:"source"`
	Error     string                  `json:"error,omitempty"`
	Unrelated bool                    `json:"unrelated_selector_inherited"`
}

// Only the test executable has this entry point. The real startWorker process
// launch is exercised without running Apply or exposing an approval bypass.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "mcp-worker" {
		if !validHandle(os.Args[2]) {
			os.Exit(2)
		}
		home, err := os.UserHomeDir()
		if err != nil {
			os.Exit(2)
		}
		receipt := workerEnvironmentReceipt{StateHome: os.Getenv("XDG_STATE_HOME"), Path: os.Getenv("PATH"), Unrelated: os.Getenv("KEEPUPDATED_UNRELATED_FIXTURE") != ""}
		receipt.Source, err = environmentSkillSource(home)
		if err != nil {
			receipt.Error = err.Error()
		}
		data, err := json.Marshal(receipt)
		if err != nil {
			os.Exit(2)
		}
		output := filepath.Join(home, os.Args[2]+".worker-environment.json")
		// Publish only the complete receipt before the parent observes its path.
		if err = os.WriteFile(output+".tmp", data, 0600); err != nil {
			os.Exit(2)
		}
		if err = os.Rename(output+".tmp", output); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func environmentSkillSource(home string) (audit.SkillUpdateSource, error) {
	// Scan only local metadata through Skill origin discovery. Cancellation after
	// that stage avoids system/service queries; online checking is never enabled.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := audit.Config{Home: home, Path: os.Getenv("PATH"), AppRoots: []string{}, BrewRoots: []string{}, ManagerRoots: map[string]string{}, ManagerBins: map[string]string{}, Timeout: 100 * time.Millisecond, Context: ctx}
	originStart := -1
	config.Progress = func(p audit.Progress) {
		if p.Phase == "scan" && p.Current == "skill-origins" {
			if originStart < 0 {
				originStart = p.Completed
			} else if p.Completed > originStart {
				cancel()
			}
		}
	}
	for _, item := range audit.New(config).Scan(false).Items {
		if item.Kind == "skill" && item.Path == filepath.Join(home, ".agents/skills/example/SKILL.md") {
			return audit.SkillSourceForUpdate(item)
		}
	}
	return audit.SkillUpdateSource{}, errors.New("synthetic Skill not discovered")
}

func TestStartWorkerPreservesXDGSkillReceiptBinding(t *testing.T) {
	home := canonicalTemp(t)
	stateHome := filepath.Join(home, "state")
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("KEEPUPDATED_UNRELATED_FIXTURE", "must-not-reach-worker")
	for _, key := range []string{"VOLTA_HOME", "PIPX_HOME", "UV_TOOL_DIR", "BUN_INSTALL_GLOBAL_DIR", "PNPM_HOME", "PIPX_BIN_DIR", "UV_TOOL_BIN_DIR", "BUN_INSTALL_BIN", "BUN_INSTALL"} {
		t.Setenv(key, "")
	}
	root := filepath.Join(home, ".agents/skills/example")
	writeTest(t, filepath.Join(root, "SKILL.md"), []byte("---\nname: example\n---\nSynthetic local fixture.\n"), 0644)
	snapshot, err := audit.ReadSkillUpdateSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(stateHome, "skills/.skill-lock.json")
	lock := map[string]any{"version": 3, "skills": map[string]any{"example": map[string]string{"source": "example/skills", "sourceType": "github", "sourceUrl": "https://github.com/example/skills.git", "skillPath": "skills/example/SKILL.md", "skillFolderHash": snapshot.Tree}}}
	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, lockPath, data, 0600)
	before, err := environmentSkillSource(home)
	if err != nil || before.SourcePath != lockPath || before.Baseline != snapshot.Tree {
		t.Fatalf("parent receipt binding: %+v %v", before, err)
	}
	// This is the exact previous failure mode, rather than merely checking an
	// environment string: without the selector the only source receipt disappears.
	t.Setenv("XDG_STATE_HOME", "")
	if _, err = environmentSkillSource(home); err == nil || err.Error() != "skill_source_unverified" {
		t.Fatalf("fixture remained source-bound without its selector: %v", err)
	}
	t.Setenv("XDG_STATE_HOME", stateHome)
	id, err := handle()
	if err != nil {
		t.Fatal(err)
	}
	if err = startWorker(id); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(home, id+".worker-environment.json")
	waitFor(t, "actual worker source receipt", func() bool { return fileExists(output) })
	data, err = os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var after workerEnvironmentReceipt
	if err = json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	if after.Error != "" || after.StateHome != stateHome || after.Path != bin || after.Unrelated || !reflect.DeepEqual(before, after.Source) {
		t.Fatalf("detached worker changed the source binding: parent=%+v child=%+v", before, after)
	}
	if err = audit.VerifySkillUpdateSource(after.Source, home); err != nil {
		t.Fatalf("worker source no longer verifies against the exact receipt: %v", err)
	}
	if _, err = os.Stat(filepath.Join(home, ".agents/.skill-lock.json")); !os.IsNotExist(err) {
		t.Fatalf("test accidentally used a fallback receipt: %v", err)
	}
}
