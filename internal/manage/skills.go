package manage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func skillPlan(i audit.Item, p PlanItem, o Options, s Selection) PlanItem {
	if s.Action == "update" && i.SkillManager != nil {
		p.Status, p.Reason = "blocked", i.SkillManager.Code
		return p
	}
	if i.Owner == "system" {
		p.Reason = "system_skill_update_client"
		return p
	}
	if i.Source == "plugin-cache" || strings.Contains(i.Path, "/plugins/cache/") || i.Plugin != nil {
		return pluginSkillPlan(i, p, o, s)
	}
	p.Status, p.Reason = "blocked", "skill_entry_outside_user_scope"
	if !filepath.IsAbs(i.Path) || filepath.Base(i.Path) != "SKILL.md" || filepath.Clean(i.Path) != i.Path {
		return p
	}
	if s.Action == "update" && i.Source == "local" {
		return copiedSkillPlan(i, p, o)
	}
	artifact := filepath.Dir(i.Path)
	allowed := false
	for _, root := range o.SkillRoots {
		if !filepath.IsAbs(root) || !under(root, o.Home) || root == o.Home {
			continue
		}
		rootReal, re := filepath.EvalSymlinks(root)
		homeReal, he := filepath.EvalSymlinks(o.Home)
		rel, _ := filepath.Rel(o.Home, root)
		if filepath.Dir(artifact) == root && re == nil && he == nil && rootReal == filepath.Join(homeReal, rel) {
			allowed = true
			break
		}
	}
	if !allowed {
		return p
	}
	if s.Action == "update" {
		return gitSkillPlan(i, p, o, artifact)
	}
	st, err := os.Lstat(artifact)
	if err != nil {
		p.Reason = "skill_entry_unavailable"
		return p
	}
	p.Provider = "user-skill"
	p.TargetVersion = ""
	p.Binding.Scope = artifact
	p.AffectedPaths = []string{artifact}
	rp, err := filepath.EvalSymlinks(i.Path)
	if err != nil {
		p.Reason = "skill_entry_unavailable"
		return p
	}
	p.Binding.RealPath = rp
	if st.Mode()&os.ModeSymlink != 0 {
		if s.Cleanup {
			p.Reason = "shared_skill_source_not_cleaned"
			return p
		}
		// Remove the discovered link only; the shared source may belong to another Agent.
		target, err := os.Readlink(artifact)
		if err != nil {
			p.Reason = "skill_link_unreadable"
			return p
		}
		if err = ownedLink(artifact); err != nil {
			p.Reason = "skill_link_not_owned"
			return p
		}
		p.Binding.Fingerprint = hashText(artifact + "\x00" + target)
		p.Status, p.Reason = "ready", "user_skill_link_only"
		return p
	}
	if i.Source == "git" && !contains(i.Evidence, "git-worktree-clean") {
		p.Reason = "skill_local_modifications_or_git_state_unknown"
		return p
	}
	for _, e := range i.Evidence {
		if e == "git-local-modifications" || e == "git-dirty-unknown" {
			p.Reason = "skill_local_modifications_or_git_state_unknown"
			return p
		}
	}
	if err = ownedPath(artifact, false); err != nil {
		p.Reason = "skill_directory_not_owned"
		return p
	}
	if err = checkTree(artifact); err != nil {
		p.Reason = "skill_directory_contains_shared_or_unreadable_paths"
		return p
	}
	digest, err := cleanupHash([]string{artifact})
	if err != nil {
		p.Reason = "skill_fingerprint_failed"
		return p
	}
	p.Binding.Fingerprint = digest
	p.Status, p.Reason = "ready", "independent_user_skill_folder_to_trash"
	return p
}

func ownedLink(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != uint32(os.Getuid()) || st.Mode()&os.ModeSymlink == 0 {
		return errors.New("not user link")
	}
	return nil
}

func hashText(text string) string { h := sha256.Sum256([]byte(text)); return hex.EncodeToString(h[:]) }

func moveSkill(path, home, id string, checkpoints ...func(string) error) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink == 0 {
		return moveCleanup([]string{path}, home, id, checkpoints...)
	}
	if err = ownedLink(path); err != nil {
		return err
	}
	trash := filepath.Join(home, ".Trash")
	if err = os.MkdirAll(trash, 0700); err != nil {
		return err
	}
	st, err = os.Lstat(trash)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("trash unavailable")
	}
	if err = ownedPath(trash, false); err != nil {
		return errors.New("trash not owned")
	}
	container, err := os.MkdirTemp(trash, "KeepUpdated-"+id[:12]+"-")
	if err != nil {
		return err
	}
	destination := filepath.Join(container, filepath.Base(path))
	for _, checkpoint := range checkpoints {
		if err := checkpoint(destination); err != nil {
			return err
		}
	}
	return os.Rename(path, destination)
}
