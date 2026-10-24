package audit

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fallbackFixture struct {
	t                      *testing.T
	src                    SkillUpdateSource
	commit, root, selected string
	objects                map[string][]byte
	calls                  [][]string
	dirs                   []string
}

func fallbackTreeBytes(tree skillTree) []byte {
	var b []byte
	for _, x := range tree.Tree {
		mode := x.Mode
		if mode == "040000" {
			mode = "40000"
		}
		raw, _ := hex.DecodeString(x.SHA)
		b = append(b, []byte(mode+" "+x.Path+"\x00")...)
		b = append(b, raw...)
	}
	return b
}
func newFallbackFixture(t *testing.T) *fallbackFixture {
	t.Helper()
	s := fixture(t)
	s.Config.Home, _ = filepath.EvalSymlinks(s.Config.Home)
	p, snap := sourceSkill(t, s, "example")
	data := []byte("---\nname: example\n---\nupstream\n")
	blob := skillObject("blob", data)
	selected := updateTestTree([]skillTreeEntry{{Path: "SKILL.md", Type: "blob", Mode: "100644", SHA: blob}})
	skills := updateTestTree([]skillTreeEntry{{Path: "example", Type: "tree", Mode: "040000", SHA: selected.SHA}})
	root := updateTestTree([]skillTreeEntry{{Path: "skills", Type: "tree", Mode: "040000", SHA: skills.SHA}, {Path: "outside-link", Type: "blob", Mode: "120000", SHA: strings.Repeat("e", 40)}, {Path: "outside-module", Type: "commit", Mode: "160000", SHA: strings.Repeat("d", 40)}})
	rawCommit := []byte("tree " + root.SHA + "\nauthor Test <test@example.com> 0 +0000\ncommitter Test <test@example.com> 0 +0000\n\nfixture\n")
	commit := skillObject("commit", rawCommit)
	f := &fallbackFixture{t: t, src: SkillUpdateSource{Repo: "example/skills", Ref: "main", Entry: "skills/example/SKILL.md", Root: filepath.Dir(p), Baseline: snap.tree, HashKind: "tree", SourcePath: filepath.Join(s.Config.Home, ".agents/.skill-lock.json"), SourceName: "example", SourceKind: "tree", SourceDigest: strings.Repeat("a", 64)}, commit: commit, root: root.SHA, selected: selected.SHA, objects: map[string][]byte{blob: data, selected.SHA: fallbackTreeBytes(selected), skills.SHA: fallbackTreeBytes(skills), root.SHA: fallbackTreeBytes(root), commit: rawCommit}}
	return f
}
func (f *fallbackFixture) run(ctx context.Context, dir string, args []string) ([]byte, error) {
	f.t.Helper()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	cIndex := -1
	for n, a := range args {
		if a == "-C" {
			cIndex = n
			break
		}
	}
	if cIndex < 0 || args[cIndex+1] != dir {
		f.t.Fatal("missing fixed repository binding")
	}
	for _, required := range []string{"core.hooksPath=/dev/null", "credential.helper=", "http.followRedirects=false", "protocol.allow=never", "protocol.https.allow=always", "protocol.file.allow=never", "submodule.recurse=false"} {
		found := false
		for _, a := range args[:cIndex] {
			if a == required {
				found = true
			}
		}
		if !found {
			f.t.Fatal("missing safety option", required)
		}
	}
	a := args[cIndex+2:]
	f.calls = append(f.calls, append([]string(nil), a...))
	if len(f.dirs) == 0 || f.dirs[len(f.dirs)-1] != dir {
		f.dirs = append(f.dirs, dir)
	}
	switch a[0] {
	case "ls-remote":
		if a[3] != "https://github.com/example/skills.git" {
			f.t.Fatal("nonpublic remote", a)
		}
		return []byte(f.commit + "\trefs/heads/main\n"), nil
	case "init":
		if !strings.Contains(strings.Join(a, " "), "--bare --template=") {
			f.t.Fatal("unsafe init", a)
		}
		return nil, nil
	case "fetch":
		if a[len(a)-1] != f.commit {
			f.t.Fatal("fetch was not pinned", a)
		}
		return nil, nil
	case "rev-parse":
		return []byte(f.commit + "\n"), nil
	case "cat-file":
		b, ok := f.objects[a[2]]
		if !ok {
			f.t.Fatal("read outside selected objects", a)
		}
		return append([]byte(nil), b...), nil
	}
	f.t.Fatal("unexpected git command", a)
	return nil, nil
}
func (f *fallbackFixture) client(status int, remaining string) *http.Client {
	return &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "" {
			f.t.Fatal("unsafe API request")
		}
		resp := response(status, "")
		resp.Header = http.Header{}
		if remaining != "" {
			resp.Header.Set("X-RateLimit-Remaining", remaining)
		}
		return resp, nil
	})}
}
func TestSkillGitFallbackOnlyForConfirmedRateLimit(t *testing.T) {
	for _, c := range []struct {
		status    int
		remaining string
		fallback  bool
	}{{403, "0", true}, {429, "", true}, {403, "", false}, {403, "1", false}, {404, "", false}, {500, "", false}} {
		t.Run(fmt.Sprintf("%d-%s", c.status, c.remaining), func(t *testing.T) {
			f := newFallbackFixture(t)
			got, e := downloadSkillUpdateCachedWithGit(context.Background(), f.src, "", "", f.client(c.status, c.remaining), "", f.run)
			if c.fallback {
				if e != nil || got.Commit != f.commit || got.Tree != f.selected || len(got.Files) != 1 {
					t.Fatalf("fallback %#v %v", got, e)
				}
				if len(f.calls) == 0 {
					t.Fatal("Git not used")
				}
			} else if e == nil || len(f.calls) != 0 {
				t.Fatalf("unexpected fallback %v calls=%v", e, f.calls)
			}
			for _, dir := range f.dirs {
				if _, e := os.Stat(dir); !os.IsNotExist(e) {
					t.Fatal("temporary Git repository retained")
				}
			}
		})
	}
}
func TestSkillGitFallbackKeepsMidDownloadCommit(t *testing.T) {
	f := newFallbackFixture(t)
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/commits/") {
			b, _ := json.Marshal(map[string]any{"sha": f.commit, "commit": map[string]any{"tree": map[string]string{"sha": f.root}}})
			return response(200, string(b)), nil
		}
		resp := response(403, "")
		resp.Header = http.Header{"X-Ratelimit-Remaining": []string{"0"}}
		return resp, nil
	})}
	got, e := downloadSkillUpdateCachedWithGit(context.Background(), f.src, "", "", client, "", f.run)
	if e != nil || got.Commit != f.commit {
		t.Fatal(got, e)
	}
	for _, a := range f.calls {
		if a[0] == "ls-remote" {
			t.Fatal("fixed API commit was replaced by a fresh ref")
		}
	}
}
func TestSkillGitFallbackBatchSharesRepository(t *testing.T) {
	f := newFallbackFixture(t)
	session := newSkillUpdateDownloadSession(f.run)
	client := f.client(403, "0")
	for n := 0; n < 3; n++ {
		got, e := session.Download(context.Background(), f.src, "", "", client, "")
		if e != nil || got.Commit != f.commit {
			t.Fatal(got, e)
		}
	}
	fetches, refs := 0, 0
	for _, a := range f.calls {
		if a[0] == "fetch" {
			fetches++
		}
		if a[0] == "ls-remote" {
			refs++
		}
	}
	if fetches != 1 || refs != 1 {
		t.Fatal("batch repeated repository download", f.calls)
	}
	session.Close()
	for _, dir := range f.dirs {
		if _, e := os.Stat(dir); !os.IsNotExist(e) {
			t.Fatal("batch cleanup failed")
		}
	}
}
func TestSkillGitFallbackInitialCheckGrouped(t *testing.T) {
	f := newFallbackFixture(t)
	s := fixture(t)
	s.Config.Home = filepath.Dir(filepath.Dir(filepath.Dir(f.src.Root)))
	s.Config.Online = true
	s.feedClient = f.client(403, "0")
	for n := 0; n < 2; n++ {
		source := &skillSource{repo: f.src.Repo, ref: f.src.Ref, entry: f.src.Entry, root: f.src.Root, baseline: f.src.Baseline, hashKind: "tree"}
		i := item("skill", "example", filepath.Join(f.src.Root, "SKILL.md"), ".agents")
		i.ID += strconv.Itoa(n)
		i.Source = "local"
		i.skillSource = source
		s.Report.Items = append(s.Report.Items, i)
	}
	s.checkSkillSourcesWithGit(f.run)
	for _, i := range s.Report.Items {
		if i.Update.Code != "skill_update_available" || i.Update.Status != "available" {
			t.Fatal(i.Update)
		}
		if !strings.Contains(strings.Join(i.Evidence, " "), "skill-update:confirmed-preview-required") {
			t.Fatal("old transport wording", i.Evidence)
		}
	}
	fetches := 0
	for _, a := range f.calls {
		if a[0] == "fetch" {
			fetches++
		}
		if a[0] == "cat-file" && a[1] == "blob" {
			t.Fatal("initial check downloaded payload")
		}
	}
	if fetches != 1 {
		t.Fatal("initial grouped check fetched repeatedly")
	}
}
func TestSkillGitRefAmbiguityAndAnnotatedTag(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	if _, e := skillGitResolvedRef([]byte(a+"\trefs/heads/main\n"+b+"\trefs/tags/main\n"), []string{"refs/heads/main", "refs/tags/main"}); e == nil {
		t.Fatal("ambiguous ref accepted")
	}
	got, e := skillGitResolvedRef([]byte(a+"\trefs/tags/v1\n"+b+"\trefs/tags/v1^{}\n"), []string{"refs/tags/v1", "refs/tags/v1^{}"})
	if e != nil || got != b {
		t.Fatal(got, e)
	}
}
func TestSkillGitFallbackRejectsCorruptionAndSelectedLink(t *testing.T) {
	for _, mode := range []string{"commit", "tree", "blob", "link"} {
		t.Run(mode, func(t *testing.T) {
			f := newFallbackFixture(t)
			switch mode {
			case "commit":
				f.objects[f.commit] = []byte("corrupt")
			case "tree":
				f.objects[f.selected] = []byte("corrupt")
			case "blob":
				for sha, data := range f.objects {
					if bytes.HasPrefix(data, []byte("---")) {
						f.objects[sha] = []byte("corrupt")
					}
				}
			case "link":
				tree := updateTestTree([]skillTreeEntry{{Path: "SKILL.md", Type: "blob", Mode: "120000", SHA: strings.Repeat("e", 40)}})
				g, e := openSkillGit(context.Background(), f.src.Repo, f.src.Ref, "", f.run)
				if e != nil {
					t.Fatal(e)
				}
				g.trees[g.tree] = tree
				g.tree = tree.SHA
				g.trees[tree.SHA] = tree
				src := f.src
				src.Entry = "SKILL.md"
				_, e = g.snapshot(src, "", "", true)
				g.close()
				if e == nil {
					t.Fatal("selected symlink accepted")
				}
				return
			}
			if _, e := downloadSkillUpdateCachedWithGit(context.Background(), f.src, "", "", f.client(429, ""), "", f.run); e == nil {
				t.Fatal("corrupt Git object accepted")
			}
		})
	}
}
func TestSkillGitRepositorySizeBound(t *testing.T) {
	dir := t.TempDir()
	f, e := os.Create(filepath.Join(dir, "large.pack"))
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Truncate((64 << 20) + 1); e != nil {
		t.Fatal(e)
	}
	f.Close()
	if !skillGitRepositoryOversized(dir) {
		t.Fatal("oversized pack accepted")
	}
}
func TestSkillGitNativeCancellationKillsChildGroup(t *testing.T) {
	for _, tc := range []struct {
		name, startup string
		deadline      bool
	}{
		{name: "cancel"},
		{name: "slow-start", startup: "sleep 0.6; "},
		{name: "deadline", deadline: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			want := "skill_git_cancelled"
			if tc.deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
				want = "skill_git_timeout"
			}
			var runErr error
			done := make(chan struct{})
			go func() {
				// Rename publishes a complete PID, even if the reader runs during the write.
				_, runErr = runSkillGit(ctx, dir, []string{"-c", "alias.wait=!" + tc.startup + "sleep 30 & echo $! > child.pid.tmp; mv child.pid.tmp child.pid; wait", "wait"})
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("Git fixture did not stop during cleanup")
				}
			})

			ready := time.NewTimer(5 * time.Second)
			defer ready.Stop()
			poll := time.NewTicker(5 * time.Millisecond)
			defer poll.Stop()
			var pid int
			for {
				data, err := os.ReadFile(filepath.Join(dir, "child.pid"))
				if err == nil {
					pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
					if err != nil || pid <= 1 {
						t.Fatalf("invalid Git fixture PID: %q", data)
					}
					break
				}
				if !os.IsNotExist(err) {
					t.Fatal(err)
				}
				select {
				case <-done:
					t.Fatalf("Git fixture exited before publishing its child PID: %v", runErr)
				case <-ready.C:
					t.Fatal("Git fixture did not publish its child PID")
				case <-poll.C:
				}
			}
			pgid, err := syscall.Getpgid(pid)
			if err != nil || pgid <= 1 || pgid == syscall.Getpgrp() {
				t.Fatalf("Git child is not alive in an isolated process group: pgid=%d, error=%v", pgid, err)
			}
			t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

			wait := 2 * time.Second
			if tc.deadline {
				deadline, _ := ctx.Deadline()
				wait += time.Until(deadline)
			} else {
				cancel()
			}
			select {
			case <-done:
				if runErr == nil || runErr.Error() != want {
					t.Fatalf("error = %v, want %s", runErr, want)
				}
			case <-time.After(wait):
				t.Fatal("Git did not return within two seconds of cancellation")
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) && errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatalf("Git child process or group survived cancellation: pid=%d, pgid=%d", pid, pgid)
		})
	}
}
