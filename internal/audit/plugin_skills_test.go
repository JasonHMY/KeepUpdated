package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginSkillUsesRegisteredOwnerNotCacheDirectory(t *testing.T) {
	h, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(h, ".claude/plugins/cache/market/example/1.0.0")
	write(t, filepath.Join(root, "skills/demo/SKILL.md"), "---\nname: demo\n---\n")
	stale := filepath.Join(h, ".claude/plugins/cache/market/example/0.9.0/skills/demo/SKILL.md")
	write(t, stale, "---\nname: demo\n---\n")
	b, _ := json.Marshal(map[string]any{"version": 2, "plugins": map[string]any{"example@market": []ClaudePluginRecord{{Scope: "user", Version: "1.0.0", InstallPath: root, GitCommitSHA: strings.Repeat("a", 40)}}}})
	write(t, filepath.Join(h, ".claude/plugins/installed_plugins.json"), string(b))
	alias := filepath.Join(h, ".agents/skills/demo")
	if e = os.MkdirAll(filepath.Dir(alias), 0755); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(filepath.Join(root, "skills/demo"), alias); e != nil {
		t.Fatal(e)
	}
	s := New(Config{Home: h})
	s.skills()
	if len(s.Report.Items) != 3 {
		t.Fatal(s.Report.Items)
	}
	for _, i := range s.Report.Items {
		if i.Path == stale {
			if i.Plugin != nil || i.PluginCache == nil || i.PluginCache.Registration != "unregistered" {
				t.Fatal("stale cache labeled registered", i)
			}
			continue
		}
		if i.PluginCache == nil || i.PluginCache.Registration != "registered" || i.Plugin == nil || i.Plugin.ID != "example@market" || i.Version != "1.0.0" || i.Source != "plugin-cache" || i.Active || i.ActivityKnown {
			t.Fatal(i)
		}
	}
	if len(s.Report.Queries) != 0 {
		t.Fatal("scan invoked client", s.Report.Queries)
	}
}

func TestSystemAndCodexPluginSkillsNeverUseGitCache(t *testing.T) {
	h := t.TempDir()
	for _, root := range []string{".codex/skills/.system/demo", ".codex/plugins/cache/market/demo/1.0.0/skills/demo"} {
		write(t, filepath.Join(h, root, "SKILL.md"), "---\nname: demo\n---\n")
		write(t, filepath.Join(h, root, ".git/HEAD"), "ref: refs/heads/main\n")
	}
	s := New(Config{Home: h})
	s.skills()
	if len(s.Report.Items) != 2 {
		t.Fatal(s.Report.Items)
	}
	for _, i := range s.Report.Items {
		if i.Update.Code != "system_skill_update_client" && i.Update.Code != "codex_plugin_update_in_client" {
			t.Fatal(i)
		}
	}
	if len(s.Report.Queries) != 0 {
		t.Fatal("managed skill treated as repository", s.Report.Queries)
	}
}
