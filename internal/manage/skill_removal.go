package manage

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

// A reference must be removed before its target; a preserved reference protects it.
func orderSkillRemovals(p *Plan, report audit.Report) {
	positions := map[string]int{}
	for n, item := range p.Items {
		positions[item.ID] = n
	}
	dependencies := make([][]int, len(p.Items))
	for n := range p.Items {
		target := &p.Items[n]
		if target.Status != "ready" || target.Provider != "user-skill" {
			continue
		}
		scope := skillEntryPath(target.Binding.Scope)
		for _, candidate := range report.Items {
			if candidate.Kind != "skill" || filepath.Base(candidate.Path) != "SKILL.md" || !filepath.IsAbs(candidate.Path) {
				continue
			}
			if candidate.Path == target.Binding.Path || !skillPathReferences(candidate.Path, scope) {
				continue
			}
			reference, selected := positions[candidate.ID]
			if selected && p.Items[reference].CanonicalID != "" {
				reference, selected = positions[p.Items[reference].CanonicalID]
			}
			if !selected || p.Items[reference].Status != "ready" || p.Items[reference].Provider != "user-skill" {
				target.Status, target.Reason = "blocked", "skill_referenced_by_preserved_installation"
				continue
			}
			dependencies[n] = append(dependencies[n], reference)
		}
	}
	ordered := make([]PlanItem, 0, len(p.Items))
	visited := make([]bool, len(p.Items))
	var visit func(int)
	visit = func(n int) {
		if visited[n] {
			return
		}
		visited[n] = true
		for _, reference := range dependencies[n] {
			visit(reference)
			if p.Items[reference].Status != "ready" {
				p.Items[n].Status, p.Items[n].Reason = "blocked", "skill_referenced_by_preserved_installation"
			}
		}
		ordered = append(ordered, p.Items[n])
	}
	for n := range p.Items {
		visit(n)
	}
	p.Items = ordered
}

// Resolve the parent only, retaining the entry whose link or directory will move.
func skillEntryPath(path string) string {
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return path
	}
	return filepath.Join(parent, filepath.Base(path))
}

// Inspect every hop, including intermediate directory links and relative targets.
func skillPathReferences(path, scope string) bool {
	entry, err := os.Lstat(scope)
	if err != nil {
		return false
	}
	remaining := strings.Split(path, string(filepath.Separator))
	current := string(filepath.Separator)
	links := 0
	for len(remaining) > 0 {
		part := remaining[0]
		remaining = remaining[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			current = filepath.Dir(current)
			continue
		}
		current = filepath.Join(current, part)
		st, err := os.Lstat(current)
		if err != nil {
			return false
		}
		// Lstat keeps a link distinct from its target while matching case aliases.
		if os.SameFile(st, entry) {
			return true
		}
		if st.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(current)
		links++
		if err != nil || links > 255 {
			return false
		}
		current = filepath.Dir(current)
		if filepath.IsAbs(target) {
			current = string(filepath.Separator)
		}
		remaining = append(strings.Split(target, string(filepath.Separator)), remaining...)
	}
	return false
}
