package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

var brewRuntimeName = regexp.MustCompile(`^[a-z0-9][a-z0-9@+._-]*$`)

func (r Resolver) runtimeDependents(ctx context.Context, i audit.Item, b manage.Binding) ([]string, error) {
	data, err := r.query(ctx, i.Owner, b, "--prefix")
	if err != nil {
		return nil, errors.New("runtime manager prefix unavailable")
	}
	prefix, err := filepath.EvalSymlinks(strings.TrimSpace(string(data)))
	expected, expectedErr := filepath.EvalSymlinks(b.Scope)
	if err != nil || expectedErr != nil || prefix != expected {
		return nil, errors.New("runtime manager prefix changed")
	}
	data, err = r.query(ctx, i.Owner, b, "uses", "--installed", "--recursive", "--include-build", "--include-optional", i.Name)
	if err != nil {
		return nil, errors.New("runtime reverse dependencies unavailable")
	}
	names := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		name := strings.TrimPrefix(strings.TrimSpace(line), "homebrew/core/")
		if name == "" {
			continue
		}
		if strings.Contains(name, "/") || !brewRuntimeName.MatchString(name) {
			return nil, errors.New("runtime dependent source unverified")
		}
		names[name] = true
	}
	result := []string{}
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func (r Resolver) runtimeDependency(ctx context.Context, name string, b manage.Binding) manage.RuntimeDependency {
	state := manage.RuntimeDependency{Name: name}
	data, err := r.query(ctx, "homebrew", b, "info", "--json=v2", "--formula", name)
	var info struct {
		Formulae []struct {
			Name, Tap string
			Versions  struct{ Stable string }
			Revision  int
			Outdated  *bool
			Installed []struct{ Version string }
			LinkedKeg string `json:"linked_keg"`
		}
	}
	if err != nil || json.Unmarshal(data, &info) != nil || len(info.Formulae) != 1 {
		return state
	}
	formula := info.Formulae[0]
	if formula.Name != name || formula.Tap != "homebrew/core" {
		return state
	}
	state.TargetVersion = formula.Versions.Stable
	if formula.Revision > 0 {
		state.TargetVersion += "_" + strconv.Itoa(formula.Revision)
	}
	state.CurrentVersion = formula.LinkedKeg
	if state.CurrentVersion == "" && len(formula.Installed) == 1 {
		state.CurrentVersion = formula.Installed[0].Version
	}
	if formula.Outdated == nil || *formula.Outdated || state.TargetVersion == "" {
		return state
	}
	for _, installed := range formula.Installed {
		if installed.Version == state.TargetVersion {
			// The plan also requires inventory's active keg to match this exact version.
			state.CurrentVersion = installed.Version
			state.Unchanged = true
		}
	}
	return state
}
