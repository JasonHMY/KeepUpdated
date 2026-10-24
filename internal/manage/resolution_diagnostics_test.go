package manage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandFailureRedactsStderrAndPreservesStdout(t *testing.T) {
	for _, tc := range []struct{ text, code string }{
		{"EACCES /Users/private SECRET", "owner_permission_denied"},
		{"ENOTFOUND registry.invalid SECRET", "owner_network_unavailable"},
		{"unrecognized SECRET --token", "owner_query_failed"},
	} {
		path := filepath.Join(t.TempDir(), "manager")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '{\"partial\":true}'\nprintf '%s' '"+tc.text+"' >&2\nexit 1\n"), 0755); err != nil {
			t.Fatal(err)
		}
		out, err := (ExecRunner{}).Run(context.Background(), Command{Path: path, Env: []string{"PATH=/usr/bin:/bin"}})
		var failure CommandFailure
		if !errors.As(err, &failure) || failure.Code != tc.code || string(out) != `{"partial":true}` || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("%q %v", out, err)
		}
	}
}

func TestResolutionFailureCannotBePassedAsSuccessfulCandidate(t *testing.T) {
	f := setup(t)
	i := f.npm("fixture", "example")
	f.o.ResolvedTargets[i.ID] = ResolvedTarget{ResolutionError: "PRIVATE /Users/example SECRET", Version: i.Version, OriginVerified: true}
	p := f.plan(Selection{Action: "update", All: true})
	if p.Items[0].Reason != "owner_resolution_failed" || p.Items[0].Status != "blocked" {
		t.Fatal(p.Items)
	}
	data, _ := json.Marshal(p)
	if strings.Contains(string(data), "SECRET") {
		t.Fatal("diagnostic not sanitized")
	}
}

func TestSuccessfulManagerWarningsDoNotBecomeQueryFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '[]'\nprintf '%s' 'WARNING: A newer pip release is available; SECRET' >&2\n"), 0755); err != nil {
		t.Fatal(err)
	}
	out, err := (ExecRunner{}).Run(context.Background(), Command{Path: path, Env: []string{"PATH=/usr/bin:/bin"}})
	if err != nil || string(out) != "[]" {
		t.Fatalf("ordinary warning changed command success: %q %v", out, err)
	}
}
