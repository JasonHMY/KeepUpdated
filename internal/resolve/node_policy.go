package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

type nodeCandidate struct {
	Dist                                        struct{ Tarball string }
	Name, Version                               string
	Scripts, Dependencies, OptionalDependencies map[string]string
}

func (r Resolver) nodePolicy(ctx context.Context, item audit.Item, b manage.Binding) (*manage.NodePolicy, error) {
	data, err := r.query(ctx, item.Owner, b, "--version")
	if err != nil {
		return nil, err
	}
	v := strings.TrimSpace(string(data))
	layout := manage.NodeLayout(item.Owner, v)
	if layout == "" {
		return nil, manage.CommandFailure{Code: "node_manager_layout_unsupported"}
	}
	selector, err := audit.ReadNodeSelector(item.Owner, item.Name, item.Path, b.Scope)
	if err != nil {
		return nil, manage.CommandFailure{Code: "node_selector_requires_review"}
	}
	if !audit.NodeSelectorAllows(selector.Selector, item.Version) {
		return nil, manage.CommandFailure{Code: "node_selector_installation_mismatch"}
	}
	store := ""
	if item.Owner == "pnpm" {
		store, err = manage.PnpmVirtualStore(item, b.Scope)
		if err != nil {
			return nil, manage.CommandFailure{Code: "node_manager_layout_unsupported"}
		}
	}
	return &manage.NodePolicy{ManagerVersion: v, ManagerFingerprint: b.ManagerFingerprint, Layout: layout, Bin: manage.NodeBin(item.Owner, b, r.Options), Selection: selector, VirtualStore: store}, nil
}

func (r Resolver) nodeCandidate(ctx context.Context, item audit.Item, b manage.Binding, selector string) (nodeCandidate, error) {
	if selector == "" || selector == "*" {
		selector = "latest"
	}
	args := []string{"view", item.Name + "@" + selector, "name", "version", "scripts", "dependencies", "optionalDependencies", "--json"}
	if item.Owner == "bun" {
		args = []string{"info", item.Name + "@" + selector, "--json"}
	}
	data, err := r.query(ctx, item.Owner, b, args...)
	if err != nil {
		return nodeCandidate{}, err
	}
	var candidates []nodeCandidate
	var candidate nodeCandidate
	if json.Unmarshal(data, &candidate) == nil {
		candidates = []nodeCandidate{candidate}
	} else if json.Unmarshal(data, &candidates) != nil {
		return candidate, errors.New("owner candidate unavailable")
	}
	if len(candidates) == 0 {
		return candidate, errors.New("owner candidate unavailable")
	}
	for _, c := range candidates {
		if c.Name != item.Name || !audit.NodeSelectorAllows(selector, c.Version) {
			return nodeCandidate{}, manage.CommandFailure{Code: "node_selector_target_mismatch"}
		}
		if candidate.Version == "" {
			candidate = c
		} else if cmp, known := audit.CompareVersions(candidate.Version, c.Version); known && cmp < 0 {
			candidate = c
		}
	}
	return candidate, nil
}

func (r Resolver) permittedNodeCandidate(ctx context.Context, item audit.Item, b manage.Binding, policy *manage.NodePolicy) (nodeCandidate, error) {
	// Failure of a separate latest hint cannot authorize a fallback outside the range.
	candidate, err := r.nodeCandidate(ctx, item, b, policy.Selection.Selector)
	if err != nil {
		return candidate, err
	}
	if policy.Selection.Selector == "latest" || policy.Selection.Selector == "" || policy.Selection.Selector == "*" {
		policy.LatestVersion = candidate.Version
	} else {
		if latest, e := r.nodeCandidate(ctx, item, b, "latest"); e == nil {
			policy.LatestVersion = latest.Version
		}
	}
	if cmp, known := audit.CompareVersions(item.Version, candidate.Version); !known {
		return candidate, errors.New("version scheme needs owner review")
	} else if cmp >= 0 {
		candidate.Version = item.Version
	}
	return candidate, nil
}

func nodeScripts(candidate nodeCandidate) bool {
	if len(candidate.Dependencies) > 0 || len(candidate.OptionalDependencies) > 0 {
		return true
	}
	for _, name := range []string{"preinstall", "install", "postinstall", "prepare"} {
		if candidate.Scripts[name] != "" {
			return true
		}
	}
	return false
}

// pnpm can provision use-node-version before even a read-only command. Queries
// suppress that effect; a configured runtime still requires separate review.
func pnpmRuntimeSelection(paths []string) bool {
	for _, path := range paths {
		data, err := readMetadata(path)
		if err != nil {
			continue
		} // configDigest rejects unreadable files.
		for _, line := range strings.Split(string(data), "\n") {
			key, _, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			key = strings.ToLower(strings.Trim(strings.TrimSpace(key), "\"'"))
			key = strings.NewReplacer("-", "", "_", "", "[]", "").Replace(key)
			if key == "usenodeversion" {
				return true
			}
		}
	}
	return false
}
