package resolve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/configedit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
	"github.com/JasonHMY/KeepUpdated/internal/mcpref"
)

func (r Resolver) mcpCandidates(ctx context.Context, report audit.Report, selection manage.Selection) (map[string]manage.ResolvedTarget, error) {
	out := map[string]manage.ResolvedTarget{}
	// An all-update never queries or implicitly migrates launcher references.
	if selection.Action != "update" || selection.All || selection.MCPReference != "" && selection.MCPCandidate == "" {
		return out, nil
	}
	for _, item := range report.Items {
		if item.Kind != "mcp-server" || !hasID(selection.IDs, item.ID) {
			continue
		}
		discovery := r.mcpCandidate(ctx, item, selection.MCPReference)
		out[item.ID] = manage.ResolvedTarget{MCPDiscovery: &discovery, OriginVerified: discovery.Candidate != nil}
	}
	return out, ctx.Err()
}

func hasID(ids []string, id string) bool {
	for _, value := range ids {
		if value == id {
			return true
		}
	}
	return false
}

func (r Resolver) mcpCandidate(ctx context.Context, item audit.Item, replacement string) mcpref.Discovery {
	source, err := configedit.NPMLauncherSource(item)
	discovery := mcpref.Discovery{Status: "manual", Reason: "mcp_launcher_source_manual", ConfiguredReference: source.Reference.Value, ConfiguredVersion: source.Reference.Version}
	if err != nil {
		// Only our fixed reason codes are public; never include paths/config values.
		switch err.Error() {
		case "mcp_launcher_environment_unknown", "mcp_launcher_cwd_unknown", "mcp_launcher_constraints_unsupported", "mcp_launcher_config_changed", "mcp_launcher_lookup_requires_review":
			discovery.Reason = err.Error()
		}
		return discovery
	}
	requested := "latest"
	if replacement != "" {
		if _, err = source.Reference.Replacement(replacement); err != nil {
			discovery.Reason = "mcp_launcher_constraints_unsupported"
			return discovery
		}
		requested = strings.TrimPrefix(replacement, source.Reference.Package+"@")
	}
	discovery.Status, discovery.Reason = "unknown", "mcp_launcher_candidate_query_failed"
	metadata, err := r.npmLauncherMetadata(ctx, source.Reference.Package, requested)
	if err != nil {
		return discovery
	}
	currentSource, sourceErr := configedit.NPMLauncherSource(item)
	if sourceErr != nil || currentSource.Identity != source.Identity {
		discovery.Status, discovery.Reason = "manual", "mcp_launcher_source_changed"
		return discovery
	}
	var release struct {
		Name, Version string
		Deprecated    json.RawMessage
	}
	if json.Unmarshal(metadata, &release) != nil || release.Name != source.Reference.Package || release.Version == "" || requested != "latest" && release.Version != requested {
		return discovery
	}
	reference := release.Name + "@" + release.Version
	// Same version is registry evidence only, never installed/current success.
	if reference == source.Reference.Value {
		discovery.Status, discovery.Reason = "unchanged", "mcp_launcher_configured_matches_registry"
		return discovery
	}
	if _, err := source.Reference.Replacement(reference); err != nil || len(release.Deprecated) > 0 && string(release.Deprecated) != `""` && string(release.Deprecated) != "null" {
		discovery.Status, discovery.Reason = "manual", "mcp_launcher_constraints_unsupported"
		return discovery
	}
	var canonical any
	if json.Unmarshal(metadata, &canonical) != nil {
		return discovery
	}
	stable, _ := json.Marshal(canonical)
	hash := sha256.Sum256(stable)
	candidate := &mcpref.Candidate{Ecosystem: "npm", Package: release.Name, Version: release.Version, Reference: reference, SourceIdentity: source.Identity, ConfigSHA256: source.ConfigSHA256, MetadataSHA256: hex.EncodeToString(hash[:]), ConstraintResult: "exact_same_package"}
	discovery.Status, discovery.Reason = "available", "mcp_launcher_candidate_requires_selection"
	discovery.Candidate, discovery.CandidateID = candidate, candidate.ID()
	return discovery
}

func (r Resolver) npmLauncherMetadata(ctx context.Context, name, version string) ([]byte, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := http.Client{Timeout: timeout}
	if r.MCPHTTPClient != nil {
		client = *r.MCPHTTPClient
	}
	// No credentials, redirects, custom registry URLs or launcher execution.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("registry redirect refused") }
	client.Jar = nil
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mcpref.DefaultNPMRegistry+url.PathEscape(name)+"/"+url.PathEscape(version), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("registry metadata unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return nil, errors.New("registry metadata unreadable")
	}
	return data, nil
}
