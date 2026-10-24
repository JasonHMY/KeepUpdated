package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommandOutputErrors(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}, {"version"}, {"version", "--format", "json"}, {"results"}} {
		if code := run(args, brokenWriter{}, io.Discard); code != 2 {
			t.Fatalf("stdout %v: exit %d", args, code)
		}
	}
	for _, args := range [][]string{
		{}, {"unknown"}, {"help", "extra"}, {"version", "--format", "invalid"},
		{"scan", "--help"}, {"check", "--help"}, {"plan", "--help"}, {"apply", "--help"},
		{"results", "--help"}, {"version", "--help"}, {"special", "prepare", "--help"},
		{"self-update", "check", "--help"}, {"mcp", "--invalid"},
	} {
		if code := run(args, io.Discard, brokenWriter{}); code != 2 {
			t.Fatalf("stderr %v: exit %d", args, code)
		}
	}
}

func TestCLIClosedOutput(t *testing.T) {
	if os.Getenv("KEEPUPDATED_OUTPUT_TEST_CHILD") == "1" {
		for n, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{os.Args[0]}, os.Args[n+1:]...)
				main()
			}
		}
		os.Exit(99)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"home", "tmp", "state"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		args []string
		json bool
	}{
		{"help", []string{"--help"}, false},
		{"version-text", []string{"version"}, false},
		{"version-json", []string{"version", "--format", "json"}, true},
		{"results", []string{"results"}, true},
		{"stderr-help", []string{"scan", "--help"}, false},
		{"apply-help", []string{"apply", "--help"}, false},
		{"invalid", []string{"unknown"}, false},
	} {
		for _, closed := range []string{"none", "stdout", "stderr", "both"} {
			t.Run(tc.name+"/"+closed, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, executable, append([]string{"-test.run=^TestCLIClosedOutput$", "--"}, tc.args...)...)
				cmd.Dir = filepath.Join(root, "home")
				cmd.Env = []string{"HOME=" + cmd.Dir, "TMPDIR=" + filepath.Join(root, "tmp"), "XDG_STATE_HOME=" + filepath.Join(root, "state"), "PATH=" + filepath.Join(root, "empty-bin"), "KEEPUPDATED_OUTPUT_TEST_CHILD=1"}
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				for stream, target := range map[string]*io.Writer{"stdout": &cmd.Stdout, "stderr": &cmd.Stderr} {
					if closed != stream && closed != "both" {
						continue
					}
					reader, writer, err := os.Pipe()
					if err != nil {
						t.Fatal(err)
					}
					reader.Close()
					defer writer.Close()
					*target = writer
				}
				code := 0
				if err := cmd.Run(); err != nil {
					if exit, ok := err.(*exec.ExitError); ok {
						code = exit.ExitCode()
					} else {
						t.Fatal(err)
					}
				}
				stderrCommand := tc.name == "stderr-help" || tc.name == "apply-help" || tc.name == "invalid"
				want := 0
				if tc.name == "invalid" || closed == "both" || stderrCommand && closed == "stderr" || !stderrCommand && closed == "stdout" {
					want = 2
				}
				if ctx.Err() != nil || code != want {
					t.Fatalf("exit %d, want %d; context=%v stdout=%q stderr=%q", code, want, ctx.Err(), stdout.String(), stderr.String())
				}
				if closed == "none" && tc.name != "invalid" {
					if stderrCommand {
						if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Usage:") {
							t.Fatal("help stream changed", stdout.String(), stderr.String())
						}
					} else if stderr.Len() != 0 || stdout.Len() == 0 || tc.json && !json.Valid(stdout.Bytes()) {
						t.Fatal("output stream changed", stdout.String(), stderr.String())
					}
				}
			})
		}
	}
	for _, dir := range []string{"home", "tmp", "state"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil || len(entries) != 0 {
			t.Fatal("output probe changed isolated state", dir, entries, err)
		}
	}
}
