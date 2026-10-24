package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommandHelpDoesNotStartOperations(t *testing.T) {
	if os.Getenv("KEEPUPDATED_HELP_TEST_CHILD") == "1" {
		for n, arg := range os.Args {
			if arg == "--" {
				os.Exit(run(os.Args[n+1:], os.Stdout, os.Stderr))
			}
		}
		os.Exit(99)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	executableJSON, _ := json.Marshal(executable)
	profile := filepath.Join(root, "help.sb")
	if err := os.WriteFile(profile, []byte(`(version 1)
(allow default)
(deny network*)
(deny file-read* (subpath "/Applications") (subpath "/opt") (subpath "/usr/local") (subpath "/Users") (subpath "/Library") (subpath "/System/Applications"))
(allow file-read* (literal `+string(executableJSON)+`))
(deny file-write*)
(allow file-write* (literal "/dev/null"))
(deny process-exec)
(allow process-exec (literal `+string(executableJSON)+`))
`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home", "tmp", "state"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range []string{"mcp", "special", "self-update"} {
		for _, help := range []string{"--help", "-h"} {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-f", profile, executable, "-test.run=^TestCommandHelpDoesNotStartOperations$", "--", command, help)
			cmd.Dir = filepath.Join(root, "home")
			cmd.Env = []string{"HOME=" + cmd.Dir, "TMPDIR=" + filepath.Join(root, "tmp"), "XDG_STATE_HOME=" + filepath.Join(root, "state"), "PATH=" + filepath.Join(root, "empty-bin"), "KEEPUPDATED_HELP_TEST_CHILD=1"}
			var out, errOut bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errOut
			err := cmd.Run()
			cancel()
			if err != nil || out.Len() != 0 || !strings.Contains(errOut.String(), "Usage:") || !strings.Contains(errOut.String(), "--help") {
				t.Fatal(command, help, err, out.String(), errOut.String())
			}
		}
	}
	for _, name := range []string{"home", "tmp", "state"} {
		entries, err := os.ReadDir(filepath.Join(root, name))
		if err != nil || len(entries) != 0 {
			t.Fatal("help changed isolated state", name, entries, err)
		}
	}
}
