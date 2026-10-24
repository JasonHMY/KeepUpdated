package audit

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLimitedBufferCommandCapture(t *testing.T) {
	const limit = 8 << 20
	for _, tc := range []struct {
		name           string
		stdout, stderr int
	}{
		{"normal", 23, 17},
		{"exact-per-stream-limit", limit, limit},
		{"stdout-over-limit", limit + 1, 17},
		{"stderr-over-limit", 23, limit + 1},
		{"both-over-limit", limit + 1, limit + 1},
		{"drain-after-overflow", 3 * limit, 3 * limit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// Real pipes exercise os/exec's io.Copy path, including ReaderFrom dispatch.
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c",
				`/usr/bin/head -c "$1" /dev/zero && /usr/bin/head -c "$2" /dev/zero >&2`,
				"capture", strconv.Itoa(tc.stdout), strconv.Itoa(tc.stderr))
			cmd.WaitDelay = time.Second
			var stdout, stderr limitedBuffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("command did not drain and exit successfully: %v (context: %v)", err, ctx.Err())
			}
			for _, stream := range []struct {
				name string
				buf  *limitedBuffer
				size int
			}{{"stdout", &stdout, tc.stdout}, {"stderr", &stderr, tc.stderr}} {
				got := stream.buf.Bytes()
				if stream.buf.overflow != (stream.size > limit) || len(got) > limit {
					t.Fatalf("%s: size=%d captured=%d overflow=%v", stream.name, stream.size, len(got), stream.buf.overflow)
				}
				if stream.size <= limit && !bytes.Equal(got, make([]byte, stream.size)) {
					t.Fatalf("%s: normal output changed (captured %d bytes)", stream.name, len(got))
				}
			}
		})
	}
}

func TestCommandRunnerOutputLimits(t *testing.T) {
	for _, runner := range []string{"scan", "skill-git"} {
		t.Run(runner, func(t *testing.T) {
			for _, size := range []int{17, 8 << 20, (8 << 20) + 1} {
				t.Run(strconv.Itoa(size), func(t *testing.T) {
					dir := t.TempDir()
					want := bytes.Repeat([]byte("x"), size)
					var got []byte
					var err error
					code := "output_limit"
					if runner == "scan" {
						file := filepath.Join(dir, "output")
						if err := os.WriteFile(file, want, 0600); err != nil {
							t.Fatal(err)
						}
						s := New(Config{Home: dir, Timeout: 10 * time.Second})
						got, err = s.run("/bin/cat", file)
					} else {
						ctx := context.Background()
						if _, err := runSkillGit(ctx, dir, []string{"init", "--bare", "--template="}); err != nil {
							t.Fatal(err)
						}
						file := filepath.Join(dir, "output")
						if err := os.WriteFile(file, want, 0600); err != nil {
							t.Fatal(err)
						}
						var object []byte
						object, err = runSkillGit(ctx, dir, []string{"hash-object", "-w", "--", file})
						if err != nil {
							t.Fatal(err)
						}
						got, err = runSkillGit(ctx, dir, []string{"cat-file", "blob", strings.TrimSpace(string(object))})
						code = "skill_git_output_limit"
					}
					if size > 8<<20 {
						if got != nil || err == nil || err.Error() != code {
							t.Fatalf("oversized output: captured=%d error=%v, want nil output and %s", len(got), err, code)
						}
					} else if err != nil || !bytes.Equal(got, want) {
						t.Fatalf("normal output changed: captured=%d error=%v", len(got), err)
					}
				})
			}
		})
	}
}

func TestScannerCommandExitStatusAndDiscardedStderr(t *testing.T) {
	s := New(Config{Home: t.TempDir(), Timeout: 10 * time.Second})
	out, err := s.run("/bin/sh", "-c", `/usr/bin/head -c 8388609 /dev/zero >&2; printf partial; exit 7`)
	var exit *exec.ExitError
	if string(out) != "partial" || !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("command result changed: output=%q error=%v", out, err)
	}
}

func TestScannerCommandCancellationAfterOverflow(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(Config{Home: dir, Context: ctx, Timeout: 10 * time.Second})
	done := make(chan error, 1)
	go func() {
		_, err := s.run("/bin/sh", "-c",
			`/usr/bin/head -c 8388609 /dev/zero && printf ready > "$1" && exec /bin/sleep 60`, "capture", ready)
		done <- err
	}()
	until := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(until) {
			cancel()
			<-done
			t.Fatal("command could not finish writing oversized output")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost precedence over overflow: %v", err)
	}
}
