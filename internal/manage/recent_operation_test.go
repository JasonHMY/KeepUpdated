package manage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func TestRecentOperationConcurrentReplacement(t *testing.T) {
	snapshots := []ApplyResult{
		{Schema: ApplySchema, PlanID: strings.Repeat("a", 64), Action: "update", State: "in_progress", StartedAt: "2026-10-19T01:00:00Z", OriginalIDs: []string{"first", "interrupted", "later"}, Results: []ItemResult{
			{ID: "first", Status: "updated"},
			{ID: "interrupted", Status: "verification_unknown", Reason: "operation_in_progress_or_interrupted"},
			{ID: "later", Status: "skipped", Reason: "operation_not_started"},
		}},
		{Schema: ApplySchema, PlanID: strings.Repeat("b", 64), Action: "uninstall", State: "finished", StartedAt: "2026-10-19T02:00:00Z", FinishedAt: "2026-10-19T02:01:00Z", OriginalIDs: []string{"other"}, Results: []ItemResult{
			{ID: "other", Status: "failed", Reason: "controlled_failure"},
		}},
	}
	const writes = 500
	if dir := os.Getenv("KEEPUPDATED_RECORD_REPLACE_FIXTURE"); dir != "" {
		for i := range writes {
			if err := saveRecentOperation(dir, snapshots[i%len(snapshots)]); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	f := setup(t)
	if err := operationDirectory(f.state, true); err != nil {
		t.Fatal(err)
	}
	if err := saveRecentOperation(f.state, snapshots[0]); err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	var ready sync.WaitGroup
	var reads, failures atomic.Int64
	var firstError atomic.Value
	stop := make(chan struct{})
	for range 16 {
		readers.Add(1)
		ready.Add(1)
		go func() {
			defer readers.Done()
			ready.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				record, err := ReadRecentOperation(f.state)
				reads.Add(1)
				if err == nil && (record.Schema != RecentOperationSchema || record.Operation == nil ||
					!reflect.DeepEqual(*record.Operation, snapshots[0]) && !reflect.DeepEqual(*record.Operation, snapshots[1])) {
					err = errors.New("missing or mixed operation snapshot")
				}
				if err != nil {
					failures.Add(1)
					firstError.CompareAndSwap(nil, err.Error())
				}
			}
		}()
	}
	ready.Wait()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecentOperationConcurrentReplacement$")
	child.Env = append(os.Environ(), "KEEPUPDATED_RECORD_REPLACE_FIXTURE="+f.state)
	output, err := child.CombinedOutput()
	close(stop)
	readers.Wait()
	if err != nil {
		t.Fatalf("writer: %s %v", output, err)
	}
	t.Logf("%d atomic replacements, %d reads, %d failures", writes, reads.Load(), failures.Load())
	if reads.Load() == 0 || failures.Load() != 0 {
		t.Fatalf("concurrent read failed: %v", firstError.Load())
	}
	record, err := ReadRecentOperation(f.state)
	if err != nil || record.Operation == nil || !reflect.DeepEqual(*record.Operation, snapshots[(writes-1)%len(snapshots)]) {
		t.Fatalf("final snapshot lost: %+v %v", record, err)
	}
	entries, err := os.ReadDir(f.state)
	if err != nil || len(entries) != 1 || entries[0].Name() != recentOperationFile {
		t.Fatalf("unexpected state files: %v %v", entries, err)
	}
}

func TestRecentOperationPersistsPartialResultsAndReplayGuard(t *testing.T) {
	f := setup(t)
	a, b := f.npm("original-a", "a"), f.npm("original-b", "b")
	p := f.plan(Selection{Action: "update", IDs: []string{a.ID, b.ID}})
	calls := 0
	runner := runFunc(func(context.Context, Command) ([]byte, error) {
		calls++
		record, err := ReadRecentOperation(f.state)
		if err != nil {
			t.Fatal(err)
		}
		if record.Operation.State != "in_progress" || record.Operation.Action != "update" || record.Operation.Results[calls-1].Status != "verification_unknown" {
			t.Fatalf("not recorded before mutation: %+v", record)
		}
		if calls == 1 {
			f.r.Items[0].Version = "1.1.0"
			f.write(filepath.Join(a.Path, "package.json"), `{"name":"a","version":"1.1.0"}`, 0644)
			return nil, nil
		}
		return nil, errors.New("controlled failure")
	})
	result, err := Apply(context.Background(), p, p.PlanID, f.engine(runner))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || result.Results[0].Status != "updated" || result.Results[1].Status != "failed" {
		t.Fatalf("unexpected result: %+v", result)
	}
	record, err := ReadRecentOperation(f.state)
	if err != nil || !reflect.DeepEqual(*record.Operation, result) {
		t.Fatalf("saved result mismatch: %+v %v", record, err)
	}
	if _, err := Apply(context.Background(), p, p.PlanID, f.engine(runner)); err == nil || calls != 2 {
		t.Fatal("old plan executed")
	}
	for _, path := range []string{f.state, filepath.Join(f.state, recentOperationFile)} {
		st, err := os.Lstat(path)
		if err != nil || st.Mode().Perm()&0077 != 0 {
			t.Fatal("not private", st, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(f.state, recentOperationFile))
	if strings.Contains(string(data), "binding") || strings.Contains(string(data), "selection") || strings.Contains(string(data), "consents") {
		t.Fatal("executable preview stored")
	}
}

// The child exits from the second manager call without running Apply's defers.
func TestRecentOperationAbruptProcessExit(t *testing.T) {
	if dir := os.Getenv("KEEPUPDATED_RECORD_CRASH_FIXTURE"); dir != "" {
		home := filepath.Dir(dir)
		f := &fixture{t: t, home: home, state: dir, r: audit.Report{Schema: audit.Schema, Items: []audit.Item{}}}
		f.o = Options{Home: home, BrewRoots: []string{filepath.Join(home, "brew")}, ManagerPaths: map[string]string{"npm": filepath.Join(home, ".npm-global/bin/npm")}, Now: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)}
		f.write(f.o.ManagerPaths["npm"], "fake manager\n", 0755)
		a, b, c := f.npm("first", "a"), f.npm("interrupted", "b"), f.npm("not-started", "c")
		p := f.plan(Selection{Action: "update", IDs: []string{a.ID, b.ID, c.ID}})
		calls := 0
		_, _ = Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
			calls++
			if calls == 1 {
				f.r.Items[0].Version = "1.1.0"
				f.write(filepath.Join(a.Path, "package.json"), `{"name":"a","version":"1.1.0"}`, 0644)
				return nil, nil
			}
			os.Exit(91)
			return nil, nil
		})))
		t.Fatal("child did not stop in manager")
	}
	f := setup(t)
	child := exec.Command(os.Args[0], "-test.run=^TestRecentOperationAbruptProcessExit$")
	child.Env = append(os.Environ(), "KEEPUPDATED_RECORD_CRASH_FIXTURE="+f.state)
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 91 {
		t.Fatalf("child: %s %v", output, err)
	}
	record, err := ReadRecentOperation(f.state)
	if err != nil {
		t.Fatal(err)
	}
	r := record.Operation
	if r == nil || r.State != "in_progress" || r.FinishedAt != "" || r.Results[0].Status != "updated" || r.Results[1].Status != "verification_unknown" || r.Results[2].Reason != "operation_not_started" {
		t.Fatalf("lost interruption: %+v", r)
	}
	before, _ := os.ReadFile(filepath.Join(f.state, recentOperationFile))
	_, err = ReadRecentOperation(f.state)
	after, _ := os.ReadFile(filepath.Join(f.state, recentOperationFile))
	if err != nil || string(before) != string(after) {
		t.Fatal("read modified record")
	}
}

func TestRecentOperationRejectsUnsafeAndDamagedRecords(t *testing.T) {
	for _, kind := range []string{"corrupt", "oversize", "version", "missing_schema", "unknown_field", "trailing", "mismatched_ids", "mode", "symlink", "hardlink", "directory", "fifo", "parent_symlink", "directory_mode"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			i := f.npm("original", "example")
			p := f.plan(Selection{Action: "update", IDs: []string{i.ID}})
			_, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) { return nil, errors.New("fixture") })))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.state, recentOperationFile)
			data, _ := os.ReadFile(path)
			switch kind {
			case "corrupt":
				err = os.WriteFile(path, []byte("{"), 0600)
			case "oversize":
				err = os.WriteFile(path, append(data, []byte(strings.Repeat(" ", recentOperationLimit+1-len(data)))...), 0600)
			case "version":
				err = os.WriteFile(path, []byte(strings.Replace(string(data), RecentOperationSchema, "keepupdated/recent-operation/v99", 1)), 0600)
			case "missing_schema":
				err = os.WriteFile(path, []byte(strings.Replace(string(data), `"schema":"`+RecentOperationSchema+`",`, "", 1)), 0600)
			case "unknown_field":
				err = os.WriteFile(path, append([]byte(`{"unexpected":true,`), data[1:]...), 0600)
			case "trailing":
				err = os.WriteFile(path, append(data, []byte("{}")...), 0600)
			case "mismatched_ids":
				var record RecentOperation
				_ = json.Unmarshal(data, &record)
				record.Operation.OriginalIDs = []string{"different"}
				data, _ = json.Marshal(record)
				err = os.WriteFile(path, data, 0600)
			case "mode":
				err = os.Chmod(path, 0644)
			case "directory_mode":
				err = os.Chmod(f.state, 0755)
			case "symlink", "hardlink", "directory", "fifo":
				target := filepath.Join(f.home, "preserved")
				f.write(target, "untouched", 0600)
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "symlink":
					err = os.Symlink(target, path)
				case "hardlink":
					err = os.Link(target, path)
				case "directory":
					err = os.Mkdir(path, 0700)
				case "fifo":
					err = syscall.Mkfifo(path, 0600)
				}
			case "parent_symlink":
				link := filepath.Join(f.home, "link")
				err = os.Symlink(f.home, link)
				f.state = filepath.Join(link, "state")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ReadRecentOperation(f.state); err == nil {
				t.Fatal("unsafe record accepted")
			}
			// A fresh plan must not replace a corrupt recovery record or execute a manager.
			f.o.Now = f.o.Now.Add(1)
			p = f.plan(Selection{Action: "update", IDs: []string{i.ID}})
			called := false
			_, err = Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) { called = true; return nil, nil })))
			if err == nil || called {
				t.Fatal("unsafe record permitted execution", err)
			}
			if kind == "symlink" || kind == "hardlink" {
				d, _ := os.ReadFile(filepath.Join(f.home, "preserved"))
				if string(d) != "untouched" {
					t.Fatal("external target changed")
				}
			}
		})
	}
}

func TestRecentOperationWriteFailureStopsLaterItems(t *testing.T) {
	f := setup(t)
	a, b := f.npm("first", "a"), f.npm("later", "b")
	p := f.plan(Selection{Action: "update", IDs: []string{a.ID, b.ID}})
	calls := 0
	_, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
		calls++
		// Make the actual record directory unwritable after one controlled mutation.
		if e := os.Chmod(f.state, 0500); e != nil {
			t.Fatal(e)
		}
		f.r.Items[0].Version = "1.1.0"
		return nil, nil
	})))
	if e := os.Chmod(f.state, 0700); e != nil {
		t.Fatal(e)
	}
	if err == nil || calls != 1 {
		t.Fatalf("continued without saving: %d %v", calls, err)
	}
	record, e := ReadRecentOperation(f.state)
	if e != nil || record.Operation.State != "in_progress" || record.Operation.Results[0].Status != "verification_unknown" || record.Operation.Results[1].Reason != "operation_not_started" {
		t.Fatalf("false final result: %+v %v", record, e)
	}
}

func TestRecentOperationInitialWriteFailurePreventsMutation(t *testing.T) {
	f := setup(t)
	i := f.npm("original", "a")
	p := f.plan(Selection{Action: "update", IDs: []string{i.ID}})
	engine := f.engine(runFunc(func(context.Context, Command) ([]byte, error) { t.Fatal("mutation ran"); return nil, nil }))
	engine.ResolveTargets = func(context.Context, audit.Report, Selection) (map[string]ResolvedTarget, error) {
		// The state lock is already held; fail the real file creation that follows.
		if err := os.Chmod(f.state, 0500); err != nil {
			t.Fatal(err)
		}
		return f.o.ResolvedTargets, nil
	}
	_, err := Apply(context.Background(), p, p.PlanID, engine)
	_ = os.Chmod(f.state, 0700)
	if err == nil {
		t.Fatal("write failure ignored")
	}
}

func TestRecentOperationAbsentReadCreatesNothing(t *testing.T) {
	f := setup(t)
	record, err := ReadRecentOperation(f.state)
	if err != nil || record.Operation != nil {
		t.Fatal(record, err)
	}
	if _, err = os.Lstat(f.state); !os.IsNotExist(err) {
		t.Fatal("read created directory", err)
	}
}

func TestRecentOperationPanicDoesNotFinalizeUnknownItem(t *testing.T) {
	f := setup(t)
	i := f.npm("original", "a")
	p := f.plan(Selection{Action: "update", IDs: []string{i.ID}})
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected controlled panic")
			}
		}()
		_, _ = Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) { panic("controlled crash") })))
	}()
	record, err := ReadRecentOperation(f.state)
	if err != nil || record.Operation.State != "in_progress" || record.Operation.FinishedAt != "" || record.Operation.Results[0].Status != "verification_unknown" {
		t.Fatal(record, err)
	}
}

func TestRecentOperationUninstallKeepsRecoveryLocation(t *testing.T) {
	f := setup(t)
	root := filepath.Join(f.home, ".codex/skills/example")
	f.write(filepath.Join(root, "SKILL.md"), "# Example\n", 0644)
	i := audit.Item{ID: "original-skill", Kind: "skill", Name: "Example", Path: filepath.Join(root, "SKILL.md"), RealPath: filepath.Join(root, "SKILL.md"), Owner: ".codex", Source: "local", Health: audit.Result{Status: "ok"}}
	f.r.Items = []audit.Item{i}
	p := f.plan(Selection{Action: "uninstall", IDs: []string{i.ID}})
	if p.Items[0].Status != "ready" {
		t.Fatal(p.Items)
	}
	e := f.engine(nil)
	e.Snapshot = func(context.Context) (audit.Report, error) {
		r := f.r
		if _, err := os.Lstat(root); os.IsNotExist(err) {
			r.Items = nil
		}
		return r, nil
	}
	result, err := Apply(context.Background(), p, p.PlanID, e)
	if err != nil || result.Results[0].Status != "uninstalled" || result.Results[0].BackupPath == "" {
		t.Fatal(result, err)
	}
	data, err := os.ReadFile(filepath.Join(result.Results[0].BackupPath, "SKILL.md"))
	if err != nil || string(data) != "# Example\n" {
		t.Fatal("original not retained", err)
	}
	record, err := ReadRecentOperation(f.state)
	if err != nil || record.Operation.Results[0].BackupPath != result.Results[0].BackupPath {
		t.Fatal(record, err)
	}
}

func TestRecentOperationReadDoesNotReleaseExecutionLock(t *testing.T) {
	f := setup(t)
	i := f.npm("original", "a")
	p := f.plan(Selection{Action: "update", IDs: []string{i.ID}})
	_, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
		if _, err := ReadRecentOperation(f.state); err != nil {
			t.Fatal(err)
		}
		_, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) { t.Fatal("competing manager ran"); return nil, nil })))
		if err == nil || !strings.Contains(err.Error(), "another action") {
			t.Fatal("read lost the lock", err)
		}
		return nil, errors.New("controlled failure")
	})))
	if err != nil {
		t.Fatal(err)
	}
}

func TestRecentOperationBackupIsSavedBeforeNativeManagerStarts(t *testing.T) {
	f, i := nativeCLIFixture(t)
	p := f.plan(nativeSelection(i.ID))
	_, err := Apply(context.Background(), p, p.PlanID, f.engine(runFunc(func(context.Context, Command) ([]byte, error) {
		record, err := ReadRecentOperation(f.state)
		if err != nil {
			t.Fatal(err)
		}
		item := record.Operation.Results[0]
		if item.ID != i.ID || item.Status != "verification_unknown" || item.BackupPath == "" {
			t.Fatal("no pre-mutation backup link", item)
		}
		original, err := os.ReadFile(filepath.Join(item.BackupPath, "uv"))
		if err != nil || string(original) != "uv 0.13.0" {
			t.Fatal("backup unavailable before manager", err)
		}
		return nil, errors.New("controlled failure")
	})))
	if err != nil {
		t.Fatal(err)
	}
}
