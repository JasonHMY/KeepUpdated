package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func TestSelectionFilePreservesLargeExactSelection(t *testing.T) {
	ids := make([]string, 3000)
	items := make([]audit.Item, len(ids))
	for n := range ids {
		ids[n] = fmt.Sprintf("item-%04d", n)
		items[n] = audit.Item{ID: ids[n], Kind: "mcp-server", Name: ids[n], Owner: "mcp-config"}
	}
	data, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ids.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := readSelectionIDs(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, ids) {
		t.Fatal("selection changed during file transport")
	}
	report := audit.Report{Items: items}
	fromFile, err := manage.NewPlan(report, manage.Selection{Action: "update", IDs: loaded}, manage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fromArgs, err := manage.NewPlan(report, manage.Selection{Action: "update", IDs: ids}, manage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromFile.Selection, fromArgs.Selection) || !reflect.DeepEqual(fromFile.Items, fromArgs.Items) {
		t.Fatal("file transport changed plan selection or item guards")
	}
}

func TestSelectionFileRejectsInvalidInputsBeforeScanning(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "valid.json")
	if err := os.WriteFile(valid, []byte(`["one","two"]`), 0600); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"empty": `[]`, "null": `null`, "null-id": `[null]`, "empty-id": `[""]`,
		"whitespace-id": `[" \t\n"]`, "object": `{"ids":["one"]}`, "number": `[1]`, "trailing": `["one"] []`, "broken": `["one"`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name)
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readSelectionIDs(path); err == nil {
				t.Fatal("accepted invalid selection")
			}
			var out, errOut bytes.Buffer
			if code := runManage("plan", []string{"--action", "update", "--ids-file", path}, &out, &errOut); code != 2 || out.Len() != 0 || errOut.Len() == 0 {
				t.Fatalf("invalid selection: code=%d out=%q err=%q", code, out.String(), errOut.String())
			}
		})
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(valid, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(root, "large")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate((16 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, path := range []string{root, link, fifo, large, filepath.Join(root, "missing")} {
		if _, err := readSelectionIDs(path); err == nil {
			t.Fatalf("accepted invalid file %s", filepath.Base(path))
		}
	}
	for _, flags := range [][]string{{"--id", "other"}, {"--all"}} {
		var out, errOut bytes.Buffer
		args := append([]string{"--action", "update", "--ids-file", valid}, flags...)
		if code := runManage("plan", args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "cannot be combined") {
			t.Fatalf("ambiguous selection: code=%d err=%q", code, errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := runManage("plan", []string{"--action", "update", "--ids-file", valid, "--special", "--mcp-reference", "example@2.0.0"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "one explicit ID") {
		t.Fatalf("file bypassed single-item review: code=%d err=%q", code, errOut.String())
	}
}
