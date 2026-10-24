package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInterruptedSkillUpdateRemainsVisibleWithoutOriginalDirectory(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		s := fixture(t)
		s.Config.Home, _ = filepath.EvalSymlinks(s.Config.Home)
		root := filepath.Join(s.Config.Home, ".agents/skills/example")
		container := filepath.Join(filepath.Dir(root), ".keepupdated-skill-fixture")
		backup := filepath.Join(container, "original")
		write(t, filepath.Join(backup, "SKILL.md"), "---\nname: example\n---\nbackup\n")
		sum := sha256.Sum256([]byte(root))
		dir := filepath.Join(s.Config.Home, "Library/Application Support/KeepUpdated/skill-transactions", hex.EncodeToString(sum[:]))
		if e := os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
		data, _ := json.Marshal(map[string]string{"schema": "keepupdated/skill-transaction/v1", "root": root, "backup_path": backup, "stage_path": filepath.Join(container, "updated"), "phase": "backed_up"})
		if malformed {
			data = []byte("broken manifest")
		}
		manifest := filepath.Join(dir, "transaction.json")
		if e := os.WriteFile(manifest, data, 0600); e != nil {
			t.Fatal(e)
		}
		s.skills()
		s.skillTransactions()
		if len(s.Report.Items) != 0 {
			t.Fatal("private backup was exposed as an installed Skill", s.Report.Items)
		}
		want := "skill_update_interrupted"
		if malformed {
			want = "skill_update_record_unreadable"
		}
		found := false
		for _, d := range s.Report.Diagnostics {
			found = found || d.Code == want && d.Path == manifest
		}
		if !found {
			t.Fatal(s.Report.Diagnostics)
		}
		if _, e := os.Lstat(root); !os.IsNotExist(e) {
			t.Fatal("read-only scan restored or changed installation")
		}
		if b, e := os.ReadFile(manifest); e != nil || string(b) != string(data) {
			t.Fatal("scan changed recovery record", e)
		}
	}
}
