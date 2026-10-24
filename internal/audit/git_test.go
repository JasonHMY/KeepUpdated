package audit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkedSkillGitDirtyAndNoHooks(t *testing.T) {
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		t.Skip("git unavailable")
	}
	s := fixture(t)
	root := filepath.Join(s.Config.Home, "source")
	write(t, filepath.Join(root, "example/SKILL.md"), "---\nname: example\n---\n")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("/usr/bin/git", append([]string{"-C", root}, args...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + s.Config.Home, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v: %s", err, b)
		}
	}
	git("init", "--initial-branch=main")
	git("add", "example/SKILL.md")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Fixture")
	write(t, filepath.Join(root, "example/SKILL.md"), "---\nname: example\n---\nlocal edit\n")
	marker := filepath.Join(root, "hook-was-run")
	write(t, filepath.Join(root, "bad-hook"), "#!/bin/sh\ntouch '"+marker+"'\n")
	os.Chmod(filepath.Join(root, "bad-hook"), 0755)
	git("config", "core.fsmonitor", filepath.Join(root, "bad-hook"))
	git("config", "remote.origin.url", "https://user:SECRET_NEVER_OUTPUT@example.invalid/private.git")
	link(t, filepath.Join(root, "example"), filepath.Join(s.Config.Home, ".codex/skills/example"))
	s.skills()
	if len(s.Report.Items) != 1 {
		t.Fatal(s.Report.Items)
	}
	i := s.Report.Items[0]
	if i.Source != "git" || !strings.Contains(strings.Join(i.Evidence, "\n"), "git-local-modifications") {
		t.Fatal(i)
	}
	if exists(marker) {
		t.Fatal("configured filesystem monitor was executed")
	}
	if strings.Contains(strings.Join(i.Evidence, "\n"), "SECRET_NEVER_OUTPUT") {
		t.Fatal("remote credential escaped")
	}
}
