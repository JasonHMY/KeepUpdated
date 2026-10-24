package manage

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/configedit"
)

// ResolutionSelection expands presentation aliases for candidate lookup only;
// the original Selection remains the user-confirmed plan scope.
func ResolutionSelection(report audit.Report, s Selection) Selection {
	if s.All || s.Action != "update" || s.MCPReference != "" {
		return s
	}
	ids := append([]string(nil), s.IDs...)
	for _, i := range report.Items {
		if i.Kind == "mcp-server" && contains(s.IDs, i.ID) {
			for _, candidate := range report.Items {
				if contains(i.Related, candidate.ID) && supported(candidate) && !contains(ids, candidate.ID) {
					ids = append(ids, candidate.ID)
				}
			}
		}
	}
	sort.Strings(ids)
	s.IDs = ids
	return s
}

func mcpPlan(i audit.Item, p PlanItem, report audit.Report, o Options, s Selection) PlanItem {
	p.Reason = "mcp_update_requires_verified_installation_owner"
	if discovery := o.ResolvedTargets[i.ID].MCPDiscovery; discovery != nil && s.Action == "update" {
		copy := *discovery
		// Observation belongs to the original preview, not the stable candidate ID.
		copy.ObservedAt = o.Now.UTC().Format(time.RFC3339Nano)
		p.MCPDiscovery = &copy
	}
	if s.MCPReference != "" {
		return mcpReferencePlan(i, p, s)
	}
	if strings.HasPrefix(i.Update.Code, "launcher_") {
		p.Reason = i.Update.Code
	}
	if s.Action != "uninstall" {
		var owners []audit.Item
		for _, candidate := range report.Items {
			if contains(i.Related, candidate.ID) && supported(candidate) {
				owners = append(owners, candidate)
			}
		}
		if len(owners) == 1 {
			boundConfig, err := configedit.Plan(i)
			if err != nil {
				p.Reason = "mcp_configuration_binding_unavailable"
				return p
			}
			owner := owners[0]
			if !audit.MCPCommandOwnerMatches(i, owner, report, boundConfig.SHA256, audit.Config{Home: o.Home, BrewRoots: o.BrewRoots, ManagerRoots: o.ManagerRoots, ManagerBins: o.ManagerBins}) {
				p.Reason = "mcp_update_requires_verified_installation_owner"
				return p
			}
			linked := planItem(owner, report, o, s)
			pinReview := linked.Status == "ready" || linked.Status == "manual" && linked.Reason == "pipx_requirement_constraints_require_review"
			if pinReview && owner.Owner == "pipx" && mcpPipxPinned(linked.Binding.Scope) {
				linked.Status, linked.Reason = "manual", "mcp_pipx_pinned_requires_review"
			}
			linked.ID, linked.Name, linked.Kind = i.ID, i.Name, i.Kind
			linked.MCP = &boundConfig
			linked.AffectedIDs = append(linked.AffectedIDs, owner.ID)
			for _, other := range report.Items {
				if other.Kind == "mcp-server" && other.ID != i.ID && contains(other.Related, owner.ID) {
					linked.AffectedIDs = append(linked.AffectedIDs, other.ID)
				}
			}
			if linked.Status == "ready" {
				linked.Reason = "mcp_updates_shared_installation_owner"
			}
			return linked
		}
		if p.MCPDiscovery != nil {
			p.Reason = p.MCPDiscovery.Reason
		}
		if len(owners) > 1 {
			p.Reason = "mcp_installation_owner_ambiguous"
		}
		return p
	}
	if s.Cleanup {
		p.Status, p.Reason = "blocked", "mcp_shared_package_and_credentials_cleanup_not_supported"
		return p
	}
	target, err := configedit.Plan(i)
	if err != nil {
		p.Status, p.Reason = "manual", "mcp_configuration_format_requires_manual_edit"
		return p
	}
	if err = ownedPath(target.ConfigPath, false); err != nil {
		p.Status, p.Reason = "manual", "mcp_configuration_permission_requires_review"
		return p
	}
	if strings.Contains(target.ConfigPath, "/.openclaw/") && !s.Special {
		p.Status, p.Reason = "manual", "mcp_configuration_may_reload_client_confirm_separately"
		return p
	}
	p.Provider = "mcp-config"
	p.TargetVersion = ""
	p.Binding.Path = target.ConfigPath
	p.Binding.RealPath = target.RealPath
	p.Binding.Scope = target.Scope
	p.Binding.Identity = target.ServerKey
	p.Binding.Fingerprint = target.SHA256
	p.MCP = &target
	p.AffectedPaths = []string{target.ConfigPath}
	p.Status, p.Reason = "ready", "remove_selected_configuration_definition_only"
	return p
}

func mcpReferencePlan(i audit.Item, p PlanItem, s Selection) PlanItem {
	if s.MCPCandidate != "" {
		discovery := p.MCPDiscovery
		if discovery == nil || discovery.Status != "available" || discovery.Candidate == nil || discovery.Candidate.ID() != s.MCPCandidate || discovery.CandidateID != s.MCPCandidate || discovery.Candidate.Reference != s.MCPReference {
			p.Status, p.Reason = "manual", "mcp_launcher_candidate_changed_or_unavailable"
			return p
		}
	}
	if s.MCPCandidate == "" {
		p.MCPDiscovery = nil
	}
	target, err := configedit.PlanReference(i, s.MCPReference)
	if err != nil {
		p.Status, p.Reason = "manual", "mcp_package_reference_migration_unavailable"
		return p
	}
	if err = ownedPath(target.ConfigPath, false); err != nil {
		p.Status, p.Reason = "manual", "mcp_configuration_permission_requires_review"
		return p
	}
	if s.MCPCandidate != "" && (p.MCPDiscovery.Candidate.ConfigSHA256 != target.SHA256 || p.MCPDiscovery.Candidate.ConstraintResult != "exact_same_package") {
		p.Status, p.Reason = "manual", "mcp_launcher_candidate_changed_or_unavailable"
		return p
	}
	if s.MCPCandidate != "" {
		target.LauncherSourceIdentity = p.MCPDiscovery.Candidate.SourceIdentity
	}
	p.Provider = "mcp-config"
	p.FromVersion, p.TargetVersion = "", ""
	p.Binding.Path, p.Binding.RealPath, p.Binding.Scope = target.ConfigPath, target.RealPath, target.Scope
	p.Binding.Identity, p.Binding.Fingerprint = target.ServerKey, target.SHA256
	p.MCP = &target
	p.AffectedPaths = []string{target.ConfigPath}
	p.SpecialApproved = true
	p.Status, p.Reason = "ready", "mcp_package_reference_change_only_not_installation"
	return p
}

func mcpReferenceVerified(p PlanItem, report audit.Report, expectedHash string) bool {
	if expectedHash == "" {
		return false
	}
	for _, item := range report.Items {
		if item.ID == p.ID && item.Kind == "mcp-server" {
			target, err := configedit.Plan(item)
			return err == nil && target.SHA256 == expectedHash && target.RealPath == p.MCP.RealPath && target.FileMode == p.MCP.FileMode
		}
	}
	return false
}

func mcpPipxPinned(scope string) bool {
	var receipt struct {
		Main struct{ Pinned bool } `json:"main_package"`
	}
	data, err := readMetadata(filepath.Join(scope, "pipx_metadata.json"))
	return err != nil || json.Unmarshal(data, &receipt) != nil || receipt.Main.Pinned
}
