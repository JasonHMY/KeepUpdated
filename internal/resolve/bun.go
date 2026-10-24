package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func (r Resolver) bun(ctx context.Context, item audit.Item, binding manage.Binding) (manage.ResolvedTarget, error) {
	failed := manage.ResolvedTarget{}
	policy, err := r.nodePolicy(ctx, item, binding)
	if err != nil {
		return failed, err
	}
	bin, err := r.query(ctx, "bun", binding, "pm", "bin", "--global")
	if err != nil {
		return failed, err
	}
	if strings.TrimSpace(string(bin)) != policy.Bin {
		return failed, manage.CommandFailure{Code: "node_manager_layout_unsupported"}
	}
	// The selected direct entry must resolve to this exact owning installation.
	installed, err := filepath.EvalSymlinks(filepath.Join(binding.Scope, "node_modules", filepath.FromSlash(item.Name)))
	if err != nil || installed != binding.RealPath {
		return failed, errors.New("manager root differs from installation")
	}
	metadata, err := readMetadata(filepath.Join(item.Path, "package.json"))
	var local struct {
		Name, Version string
		Resolved      string `json:"_resolved"`
	}
	if err != nil || json.Unmarshal(metadata, &local) != nil || local.Name != item.Name || local.Version != item.Version {
		return failed, errors.New("installed metadata invalid")
	}
	candidate, err := r.permittedNodeCandidate(ctx, item, binding, policy)
	if err != nil {
		return failed, err
	}
	if local.Resolved != "" {
		origin, e1 := url.Parse(local.Resolved)
		candidateOrigin, e2 := url.Parse(candidate.Dist.Tarball)
		if e1 != nil || e2 != nil || origin.Scheme != "https" || candidateOrigin.Scheme != "https" || origin.Host == "" || origin.Host != candidateOrigin.Host || origin.User != nil || candidateOrigin.User != nil {
			return failed, errors.New("recorded source differs from current registry")
		}
	}
	digest, err := configDigest("bun:"+item.Name, []string{filepath.Join(r.Options.Home, ".bunfig.toml"), filepath.Join(r.Options.Home, ".config/.bunfig.toml"), filepath.Join(r.Options.Home, ".npmrc"), filepath.Join(binding.Scope, "bunfig.toml"), filepath.Join(binding.Scope, ".npmrc")})
	if err != nil {
		return failed, err
	}
	return manage.ResolvedTarget{Version: candidate.Version, Source: "manager-config:" + digest, OriginVerified: true, Reason: "configured_manager_source", Node: policy, HasLifecycleScripts: nodeScripts(candidate)}, nil
}
