package manage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

// RuntimeReview describes consumers, not packages authorized for mutation.
type RuntimeReview struct {
	Owner          string            `json:"owner"`
	CurrentVersion string            `json:"current_version"`
	TargetVersion  string            `json:"target_version"`
	Compatibility  string            `json:"compatibility"`
	Consumers      []RuntimeConsumer `json:"consumers"`
	UnknownImpact  []string          `json:"unknown_impact"`
}

type RuntimeConsumer struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Path        string `json:"path,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// ReviewableRuntime deliberately excludes shared libraries and versioned formulae.
func ReviewableRuntime(i audit.Item) bool {
	return i.Kind == "formula" && i.Owner == "homebrew" && contains([]string{"node", "python", "python3", "go", "openjdk", "bun", "uv", "pipx", "npm", "pnpm"}, i.Name)
}

func runtimeReview(i audit.Item, p PlanItem, report audit.Report) *RuntimeReview {
	review := &RuntimeReview{Owner: p.Provider, CurrentVersion: i.Version, TargetVersion: p.TargetVersion, Compatibility: "same_major", Consumers: []RuntimeConsumer{}, UnknownImpact: []string{"Project scripts, external applications and undiscovered consumers cannot be fully determined.", "Installed consumers are listed for review; they are not authorized for update or rebuild."}}
	if strings.Split(i.Version, ".")[0] != strings.Split(p.TargetVersion, ".")[0] {
		review.Compatibility = "major_change"
	}
	if strings.HasPrefix(i.Version, "0.") && len(strings.Split(p.TargetVersion, ".")) > 1 && strings.Split(i.Version, ".")[1] != strings.Split(p.TargetVersion, ".")[1] {
		review.Compatibility = "pre_1_minor_change"
	}
	names := map[string]bool{}
	if p.Binding.ResolvedTarget != nil {
		for _, name := range p.Binding.ResolvedTarget.RuntimeDependents {
			names[filepath.Base(name)] = true
		}
	}
	for _, other := range report.Items {
		if other.ID == i.ID || i.Kind == "formula" && other.Kind == "executable" && under(other.RealPath, i.Path) {
			continue
		}
		related := contains(other.Related, i.ID)
		if i.Owner == "uv-standalone" {
			related = related || other.Owner == "uv"
		}
		if i.Owner == "homebrew" && under(other.Path, p.Binding.Scope) {
			related = related || names[other.Name]
			for _, dep := range other.Dependencies {
				related = related || filepath.Base(dep.Name) == i.Name
			}
			for _, evidence := range other.Evidence {
				related = related || evidence == "dependency:"+i.Name || evidence == "dependency:homebrew/core/"+i.Name
			}
		}
		if !related {
			continue
		}
		data, _ := json.Marshal(other)
		review.Consumers = append(review.Consumers, RuntimeConsumer{ID: other.ID, Name: other.Name, Version: other.Version, Path: other.Path, Fingerprint: hashText(string(data))})
		delete(names, other.Name)
	}
	for name := range names {
		review.Consumers = append(review.Consumers, RuntimeConsumer{Name: name})
	}
	sort.Slice(review.Consumers, func(a, b int) bool {
		return review.Consumers[a].Name+review.Consumers[a].ID < review.Consumers[b].Name+review.Consumers[b].ID
	})
	return review
}

func reviewedRuntime(i audit.Item, p *PlanItem, report audit.Report, s Selection) bool {
	p.RuntimeReview = runtimeReview(i, *p, report)
	p.RequiredConsents = []string{"runtime_update"}
	p.RequiresScripts = true
	p.Status, p.Reason = "manual", "shared_runtime_requires_separate_review"
	if s.Action != "update" || s.All || len(s.IDs) != 1 || !contains(s.Consents, "runtime_update") {
		return false
	}
	if i.Kind == "formula" && !ReviewableRuntime(i) {
		p.Reason = "runtime_or_version_pin_not_supported"
		return false
	}
	if i.Kind == "formula" {
		current := false
		for _, executable := range report.Items {
			current = current || executable.Kind == "executable" && runtimeExecutableName(i.Name, executable.Name) && executable.Active && executable.ActivityKnown && executable.Health.Status == "ok" && under(executable.RealPath, filepath.Join(i.Path, i.Version))
		}
		if !current {
			p.Reason = "runtime_not_current_installation"
			return false
		}
	}
	if !i.Active || !i.ActivityKnown {
		p.Reason = "runtime_not_current_installation"
		return false
	}
	if p.RuntimeReview.Compatibility != "same_major" {
		p.Reason = "runtime_breaking_version_requires_manual_review"
		return false
	}
	if i.Kind == "formula" && (p.Binding.ResolvedTarget == nil || !p.Binding.ResolvedTarget.RuntimeScopeVerified) {
		p.Reason = "runtime_dependency_scope_unverified"
		return false
	}
	if !s.Special {
		p.Reason = "lifecycle_scripts_require_separate_review"
		return false
	}
	p.SpecialApproved = true
	return true
}

func runtimeTreeHash(root string) (string, error) {
	h := sha256.New()
	count := 0
	err := filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		count++
		if count > 100000 {
			return errors.New("runtime tree too large")
		}
		st, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintln(h, file, st.Mode())
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(file)
			if err != nil {
				return err
			}
			fmt.Fprintln(h, target)
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		digest, err := fileHash(file)
		if err != nil {
			return err
		}
		fmt.Fprintln(h, digest)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

func runtimeVersionVerified(i audit.Item, p PlanItem, report audit.Report) bool {
	for _, executable := range report.Items {
		if executable.Kind == "executable" && runtimeExecutableName(i.Name, executable.Name) && executable.Active && executable.ActivityKnown && executable.Health.Status == "ok" && under(executable.RealPath, filepath.Join(i.Path, p.TargetVersion)) && (executable.Version == p.TargetVersion || executable.Version == strings.Split(p.TargetVersion, "_")[0]) {
			return true
		}
	}
	return false
}

func runtimeExecutableName(formula, executable string) bool {
	switch formula {
	case "python", "python3":
		return executable == "python" || executable == "python3"
	case "openjdk":
		return executable == "java"
	default:
		return executable == formula
	}
}

// RuntimeDependency is owner-query evidence for a protected dependency, not consent to update it.
type RuntimeDependency struct {
	Name           string `json:"name"`
	CurrentVersion string `json:"current_version,omitempty"`
	TargetVersion  string `json:"target_version,omitempty"`
	Unchanged      bool   `json:"unchanged"`
}

func ProtectedRuntime(name string) bool { return protectedRuntime(name) }

func protectedDependencyMayChange(p PlanItem, report audit.Report) bool {
	target := p.Binding.ResolvedTarget
	if target == nil {
		return false
	}
	for _, dependency := range target.AffectedPackages {
		name := filepath.Base(dependency)
		if !protectedRuntime(name) {
			continue
		}
		verified := false
		for _, state := range target.RuntimeDependencies {
			if state.Name != name || !state.Unchanged || state.CurrentVersion == "" || state.CurrentVersion != state.TargetVersion {
				continue
			}
			for _, installed := range report.Items {
				if installed.Kind == "formula" && installed.Owner == "homebrew" && installed.Name == name && installed.Source == "homebrew/core" && installed.Path == filepath.Join(p.Binding.Scope, "Cellar", name) && installed.Version == state.CurrentVersion && installed.ActiveVersion == state.CurrentVersion && installed.Health.Status == "ok" {
					verified = true
				}
			}
		}
		if !verified {
			return true
		}
	}
	return false
}
