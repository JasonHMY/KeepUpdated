package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Interrupted updates remain visible even if the original directory is temporarily absent.
func (s *Scanner) skillTransactions() {
	root := filepath.Join(s.Config.Home, "Library/Application Support/KeepUpdated/skill-transactions")
	if _, e := os.Lstat(root); os.IsNotExist(e) {
		return
	}
	private := func(p string, directory bool) bool {
		st, e := os.Lstat(p)
		if e != nil || st.Mode().Perm()&0077 != 0 || st.Mode()&os.ModeSymlink != 0 || directory != st.IsDir() || !directory && !st.Mode().IsRegular() {
			return false
		}
		ss, ok := st.Sys().(*syscall.Stat_t)
		return ok && ss.Uid == uint32(os.Getuid())
	}
	if !private(root, true) || real(root) != root {
		s.diag("skills", root, "skill_update_record_unreadable")
		return
	}
	d, e := os.Open(root)
	if e != nil {
		s.diag("skills", root, "skill_update_record_unreadable")
		return
	}
	defer d.Close()
	entries, e := d.ReadDir(129)
	if e != nil && e != io.EOF {
		s.diag("skills", root, "skill_update_record_unreadable")
		return
	}
	if len(entries) > 128 {
		s.diag("skills", root, "skill_update_record_unreadable")
		entries = entries[:128]
	}
	for _, entry := range entries {
		if !skillHex64.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		manifest := filepath.Join(dir, "transaction.json")
		if _, e := os.Lstat(manifest); os.IsNotExist(e) {
			continue
		}
		if !private(dir, true) || !private(manifest, false) {
			s.diag("skills", manifest, "skill_update_record_unreadable")
			continue
		}
		b, e := readSmall(manifest)
		// The saved keys use snake_case; paths are validated but never followed here.
		var tx struct {
			Schema     string `json:"schema"`
			Root       string `json:"root"`
			BackupPath string `json:"backup_path"`
			StagePath  string `json:"stage_path"`
			Phase      string `json:"phase"`
		}
		if e == nil {
			e = json.Unmarshal(b, &tx)
		}
		h := sha256.Sum256([]byte(tx.Root))
		container := filepath.Dir(tx.BackupPath)
		valid := e == nil && tx.Schema == "keepupdated/skill-transaction/v1" && tx.Phase != "" && filepath.IsAbs(tx.Root) && filepath.Clean(tx.Root) == tx.Root && within(tx.Root, s.Config.Home) && tx.Root != s.Config.Home && entry.Name() == hex.EncodeToString(h[:]) && filepath.Dir(container) == filepath.Dir(tx.Root) && strings.HasPrefix(filepath.Base(container), ".keepupdated-skill-") && tx.BackupPath == filepath.Join(container, "original") && tx.StagePath == filepath.Join(container, "updated")
		if valid {
			s.diag("skills", manifest, "skill_update_interrupted")
		} else {
			s.diag("skills", manifest, "skill_update_record_unreadable")
		}
	}
}
