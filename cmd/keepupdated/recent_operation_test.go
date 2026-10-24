package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func TestResultsCommandIsReadOnlyAndReportsCorruption(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	dir := manage.ActionStateDir(home)
	var out, diagnostics bytes.Buffer
	if code := run([]string{"results"}, &out, &diagnostics); code != 0 {
		t.Fatal(code, diagnostics.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("read created action directory")
	}
	var record manage.RecentOperation
	if err = json.Unmarshal(out.Bytes(), &record); err != nil || record.Operation != nil {
		t.Fatal(out.String(), err)
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	record.Operation = &manage.ApplyResult{Schema: manage.ApplySchema, PlanID: strings.Repeat("a", 64), Action: "uninstall", State: "in_progress", OriginalIDs: []string{"original-id"}, StartedAt: "2026-10-19T01:00:00Z", Results: []manage.ItemResult{{ID: "original-id", Name: "Fixture", Status: "verification_unknown", Reason: "operation_in_progress_or_interrupted", BackupPath: "/private/fixture/backup"}}}
	data, _ := json.Marshal(record)
	path := filepath.Join(dir, "recent-operation.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	diagnostics.Reset()
	if code := run([]string{"results", "--format", "json"}, &out, &diagnostics); code != 0 {
		t.Fatal(code, diagnostics.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("original-id")) || !bytes.Contains(out.Bytes(), []byte("verification_unknown")) {
		t.Fatal(out.String())
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, data) {
		t.Fatal("read modified saved record")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("read created locks or plans", entries)
	}
	if err = os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	diagnostics.Reset()
	if code := run([]string{"results"}, &out, &diagnostics); code != 2 || out.Len() != 0 || !strings.Contains(diagnostics.String(), "could not be read") {
		t.Fatal(code, out.String(), diagnostics.String())
	}
	for _, args := range [][]string{{"results", "--confirm", "old"}, {"results", "--plan", "old"}, {"results", "--format", "text"}, {"results", "extra"}} {
		out.Reset()
		diagnostics.Reset()
		if code := run(args, &out, &diagnostics); code != 2 {
			t.Fatal("unexpected command accepted", args, code)
		}
	}
}
