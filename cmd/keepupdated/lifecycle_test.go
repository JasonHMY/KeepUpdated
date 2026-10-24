package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func TestApplicationLifecycleCLIArguments(t *testing.T) {
	for _, args := range [][]string{
		{"plan", "--action", "update", "--all", "--quit-app", "app"},
		{"plan", "--action", "uninstall", "--id", "cask", "--quit-app", "app"},
		{"plan", "--action", "update", "--id", "cask", "--reopen-app", "app"},
		{"plan", "--action", "update", "--id", "cask", "--quit-app", "app", "--quit-app", "app"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 || out.Len() != 0 {
			t.Fatal(args, code, out.String(), errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"plan", "--help"}, &out, &errOut); code != 0 {
		t.Fatal(code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "quit-app") || !strings.Contains(errOut.String(), "reopen-app") {
		t.Fatal("lifecycle flags missing", errOut.String())
	}
}

func TestUpdateSuccessDoesNotHideReopenFailure(t *testing.T) {
	for _, status := range []string{"reopened", "not_requested", "failed", "verification_unknown", "skipped"} {
		result := manage.ApplyResult{Results: []manage.ItemResult{{Status: "updated", ApplicationLifecycle: []manage.ApplicationLifecycleResult{{ReopenStatus: status}}}}}
		want := 1
		if status == "reopened" || status == "not_requested" {
			want = 0
		}
		if code := applyExit(result); code != want {
			t.Fatal(status, code)
		}
	}
}
