package audit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillRootsSkipBackupsAndEmbeddedExamples(t *testing.T) {
	h := t.TempDir()
	root := filepath.Join(h, ".agents/skills")
	paths := []string{".writer.bak-20260102030405/SKILL.md", "backup-helper/SKILL.md", ".backup-helper/SKILL.md", ".writer.bak-not-a-date/SKILL.md", "category/nested/SKILL.md", "writer/SKILL.md", "writer/upstream/SKILL.md", "writer/examples/demo/SKILL.md"}
	for _, p := range paths {
		write(t, filepath.Join(root, p), "---\nname: example\n---\n")
	}
	write(t, filepath.Join(root, ".writer.bak-scope-cutover-20260102/SKILL.md"), "---\nname: example\n---\n")
	write(t, filepath.Join(h, ".codex/skills/.system/core/SKILL.md"), "---\nname: core\n---\n")
	alias := filepath.Join(h, ".claude/skills/alias")
	if e := os.MkdirAll(filepath.Dir(alias), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(filepath.Join(root, "writer"), alias); e != nil {
		t.Fatal(e)
	}
	s := New(Config{Home: h})
	s.skills()
	if len(s.Report.Items) != 7 {
		t.Fatalf("expected 7 real entries, got %+v", s.Report.Items)
	}
	seen := map[string]bool{}
	for _, i := range s.Report.Items {
		seen[i.Path] = true
	}
	for _, p := range []string{paths[0], paths[6], paths[7]} {
		if seen[filepath.Join(root, p)] {
			t.Fatal("embedded/backup entry counted", p)
		}
	}
	// An explicitly configured nested root remains an independent installation.
	s = New(Config{Home: h, SkillRoots: []string{filepath.Join(root, "writer/upstream")}})
	s.skills()
	if len(s.Report.Items) != 8 {
		t.Fatal(s.Report.Items)
	}
}

func TestPluginCacheRegistrationDoesNotGuessCodexVersions(t *testing.T) {
	h := t.TempDir()
	for _, p := range []string{".claude/plugins/cache/market/example/1/skills/a/SKILL.md", ".claude/plugins/cache/market/example/2/skills/a/SKILL.md", ".codex/plugins/cache/market/example/1/skills/a/SKILL.md", ".codex/plugins/cache/market/example/2/skills/a/SKILL.md"} {
		write(t, filepath.Join(h, p), "---\nname: a\n---\n")
	}
	write(t, filepath.Join(h, ".claude/plugins/installed_plugins.json"), `{"version":2,"plugins":{}}`)
	s := New(Config{Home: h})
	s.skills()
	for _, i := range s.Report.Items {
		if i.PluginCache == nil {
			t.Fatal(i)
		}
		want := "unverified"
		if i.PluginCache.Client == "claude" {
			want = "unregistered"
		}
		if i.PluginCache.Registration != want || i.Plugin != nil {
			t.Fatal(i)
		}
	}
	for _, data := range []string{`{"version":3,"plugins":{}}`, `{`, `{"version":2}`, `{"version":2,"plugins":{"bad":[]}}`} {
		write(t, filepath.Join(h, ".claude/plugins/installed_plugins.json"), data)
		s = New(Config{Home: h})
		s.skills()
		for _, i := range s.Report.Items {
			if i.PluginCache.Registration != "unverified" {
				t.Fatal("invalid registry implied absence", i)
			}
		}
	}
}
