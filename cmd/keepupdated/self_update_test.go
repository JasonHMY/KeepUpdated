package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func TestSelfUpdateInspectHasSeparateReadOnlyArguments(t *testing.T) {
	if runtime.GOOS != "darwin" {
		return
	}
	root := t.TempDir()
	args := []string{"inspect", "--plan", filepath.Join(root, "missing.json"), "--operation", "saved-id", "--app", filepath.Join(root, "KeepUpdated.app")}
	var out, diagnostics bytes.Buffer
	if code := runSelfUpdate(args, &out, &diagnostics); code != 0 {
		t.Fatal(code, diagnostics.String())
	}
	var result manage.SelfUpdateInspection
	if e := json.Unmarshal(out.Bytes(), &result); e != nil || result.Status != "unknown" || result.ID != "saved-id" {
		t.Fatal(out.String(), e)
	}
	files, e := os.ReadDir(root)
	if e != nil || len(files) != 0 {
		t.Fatal("inspection wrote files", files, e)
	}
	for _, bad := range [][]string{
		{"inspect", "--plan", "missing", "--confirm", "id", "--app", "app"},
		{"inspect", "--plan", "missing", "--operation", "id"},
		{"handoff", "--plan", "missing", "--operation", "id"},
		{"finish", "--plan", "missing", "--confirm", "id", "--operation", "id"},
	} {
		if code := runSelfUpdate(bad, &out, &diagnostics); code != 2 {
			t.Fatal(bad, code)
		}
	}
}
