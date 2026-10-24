package manage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

var packageName = regexp.MustCompile(`^(?:@[a-z0-9._-]+/)?[a-z0-9][a-z0-9._-]*$`)
var simpleName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+@-]*$`)
var version = regexp.MustCompile(`^[0-9][A-Za-z0-9.,_+:-]*$`)

func normalizeOptions(o Options) Options {
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	if o.BrewRoots == nil {
		o.BrewRoots = []string{"/opt/homebrew", "/usr/local"}
	}
	if o.AppRoots == nil {
		o.AppRoots = []string{"/Applications", filepath.Join(o.Home, "Applications")}
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.TTL == 0 {
		o.TTL = 30 * time.Minute
	}
	if o.SkillRoots == nil {
		for _, p := range []string{".codex/skills", ".agents/skills", ".claude/skills", ".config/opencode/skills", ".config/hermes/skills", ".hermes/skills", ".openclaw/skills"} {
			o.SkillRoots = append(o.SkillRoots, filepath.Join(o.Home, p))
		}
	}
	return o
}

// NewPlan does no execution or network access. All selected items remain visible.
func NewPlan(report audit.Report, s Selection, options Options) (Plan, error) {
	o := normalizeOptions(options)
	if o.Context != nil && o.Context.Err() != nil {
		return Plan{}, o.Context.Err()
	}
	if err := ValidateLifecycleSelection(s); err != nil {
		return Plan{}, err
	}
	if err := validateLifecycleScope(report, s); err != nil {
		return Plan{}, err
	}
	s.QuitApps = sortedIDs(s.QuitApps)
	s.ReopenApps = sortedIDs(s.ReopenApps)
	if s.MCPReference != "" && (s.Action != "update" || s.All || len(s.IDs) != 1 || !s.Special || s.Cleanup) {
		return Plan{}, errors.New("MCP reference migration requires update, one explicit ID and special confirmation")
	}
	if s.MCPCandidate != "" && (s.MCPReference == "" || len(s.MCPCandidate) != 64) {
		return Plan{}, errors.New("MCP candidate selection requires an exact reference and candidate ID")
	}
	var consentErr error
	s.Consents, consentErr = NormalizeConsents(s.Consents)
	if consentErr != nil {
		return Plan{}, consentErr
	}
	if s.Action != "update" && s.Action != "uninstall" {
		return Plan{}, errors.New("action must be update or uninstall")
	}
	if s.All == (len(s.IDs) > 0) {
		return Plan{}, errors.New("select explicit IDs or all")
	}
	if s.CachedPlugins && s.Action != "update" {
		return Plan{}, errors.New("cached plugins applies only to update")
	}
	if s.Cleanup && s.Action != "uninstall" {
		return Plan{}, errors.New("cleanup applies only to uninstall")
	}
	if o.TTL <= 0 || o.TTL > 24*time.Hour {
		return Plan{}, errors.New("invalid plan lifetime")
	}
	if !filepath.IsAbs(o.Home) || filepath.Clean(o.Home) == "/" {
		return Plan{}, errors.New("invalid home directory")
	}
	s.IDs = append([]string(nil), s.IDs...)
	sort.Strings(s.IDs)
	selected := map[string]bool{}
	for _, id := range s.IDs {
		if id == "" || selected[id] {
			return Plan{}, errors.New("empty or duplicate selected ID")
		}
		selected[id] = true
	}
	items := append([]audit.Item(nil), report.Items...)
	sort.Slice(items, func(a, b int) bool { return items[a].ID < items[b].ID })
	report.Items = items
	p := Plan{Schema: PlanSchema, CreatedAt: o.Now.UTC().Format(time.RFC3339Nano), ExpiresAt: o.Now.Add(o.TTL).UTC().Format(time.RFC3339Nano), Action: s.Action, Selection: s, Items: []PlanItem{}}
	total := 0
	for _, item := range items {
		if s.All || selected[item.ID] {
			total++
		}
	}
	progress := audit.NewPhaseProgress(o.Context, o.Progress, "plan", &total)
	defer progress.Finish()
	seenID, seenTarget := map[string]bool{}, map[string]PlanItem{}
	for _, item := range items {
		if item.ID == "" || seenID[item.ID] {
			return Plan{}, errors.New("report has duplicate or empty IDs")
		}
		seenID[item.ID] = true
		if !s.All && !selected[item.ID] {
			continue
		}
		if o.Context != nil && o.Context.Err() != nil {
			return Plan{}, o.Context.Err()
		}
		progress.Begin(item.Name)
		delete(selected, item.ID)
		pi := planItem(item, report, o, s)
		if o.Context != nil && o.Context.Err() != nil {
			return Plan{}, o.Context.Err()
		}
		if pi.RequiresAdmin {
			pi.RequiredConsents = append(pi.RequiredConsents, ConsentPrivilegedInstall)
		}
		if pi.RequiresRestart {
			pi.RequiredConsents = append(pi.RequiredConsents, ConsentRestartSystem)
		}
		pi.RequiredConsents, consentErr = NormalizeConsents(pi.RequiredConsents)
		if consentErr != nil {
			return Plan{}, consentErr
		}
		if pi.Status == "ready" && !HasConsents(s, pi.RequiredConsents...) {
			pi.Status, pi.Reason = "blocked", "explicit_action_consent_required"
		}
		if pi.Status == "ready" && (s.All || len(s.IDs) != 1) && (pi.RequiresAdmin || pi.RequiresRestart) {
			pi.Status, pi.Reason = "blocked", "privileged_operation_requires_single_target"
		}
		key := pi.Provider + "\x00" + pi.Binding.RealPath + "\x00" + pi.Binding.Identity + "\x00" + pi.Binding.Scope
		if pi.Status == "ready" && pi.Binding.RealPath != "" {
			if canonical, found := seenTarget[key]; found {
				if sameDuplicateAction(canonical, pi, report) {
					pi.Status, pi.Reason = "skipped", "duplicate_installation_target"
					pi.CanonicalID = canonical.ID
					pi.SkillUpdate = nil
					pi.PluginUpdate = nil
				} else {
					pi.Status, pi.Reason = "blocked", "duplicate_installation_conflicting_evidence"
				}
			} else {
				seenTarget[key] = pi
			}
		}
		p.Items = append(p.Items, pi)
		progress.Done(pi.Status == "blocked" || pi.Status == "manual")
	}
	if len(selected) > 0 {
		return Plan{}, errors.New("selected ID is absent from current inventory")
	}
	if s.Action == "uninstall" {
		orderSkillRemovals(&p, report)
	}
	p.PlanID = planHash(p)
	// Match the CLI's indentation and include json.Encoder's trailing newline.
	if encoded, err := json.MarshalIndent(p, "", "  "); err != nil || len(encoded)+1 > 16<<20 {
		return Plan{}, errors.New("preview exceeds the 16 MiB plan size limit; select fewer items and generate a fresh preview")
	}
	return p, nil
}

// Rows are sorted by inventory ID before choosing an executable canonical action.
// Reference-only fields may differ; source, candidate, consent and effect evidence must agree.
func sameDuplicateAction(a, b PlanItem, report audit.Report) bool {
	if a.Binding.TargetID != b.Binding.TargetID {
		switch b.Provider {
		case "homebrew", "npm", "pnpm", "bun", "pipx", "uv":
			// Package fingerprints include the inventory ID. Rebind only that ID;
			// retain every hashed metadata field, file digest and optional suffix.
			found := false
			for _, item := range report.Items {
				if item.ID != b.Binding.TargetID {
					continue
				}
				digest, err := installationHash(item, b.Binding.Scope)
				parts := strings.SplitN(b.Binding.Fingerprint, "/", 2)
				if err != nil || digest != parts[0] {
					return false
				}
				item.ID = a.Binding.TargetID
				parts[0], err = installationHash(item, b.Binding.Scope)
				if err != nil {
					return false
				}
				b.Binding.Fingerprint = strings.Join(parts, "/")
				found = true
				break
			}
			if !found {
				return false
			}
		}
	}
	normalize := func(p PlanItem) PlanItem {
		p.ID, p.Name, p.Kind, p.Reason = "", "", "", ""
		p.AffectedIDs = nil
		p.Binding.TargetID, p.Binding.Path = "", ""
		if p.Provider != "mcp-config" {
			p.MCP = nil
		}
		switch p.Provider {
		case "copied-skill", "claude-plugin", "git-skill", "user-skill":
			p.Binding.Owner, p.Binding.PackageName, p.Binding.InstallationKind = "", "", ""
		}
		return p
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}

// ValidatePlan checks the immutable digest and lifetime before presenting approval.
// Apply repeats these checks and rebuilds the plan from fresh source state.
func ValidatePlan(p Plan, now time.Time) error {
	if p.Schema != PlanSchema || p.PlanID == "" || p.PlanID != planHash(p) {
		return errors.New("plan digest does not match")
	}
	created, ce := time.Parse(time.RFC3339Nano, p.CreatedAt)
	expires, ee := time.Parse(time.RFC3339Nano, p.ExpiresAt)
	if ce != nil || ee != nil || !expires.After(created) || expires.Sub(created) > 24*time.Hour || now.Before(created) || !now.Before(expires) {
		return errors.New("plan expired or invalid timestamps")
	}
	return nil
}

func planHash(p Plan) string {
	p.PlanID = ""
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func planItem(i audit.Item, report audit.Report, o Options, s Selection) PlanItem {
	p := PlanItem{ID: i.ID, Name: i.Name, Kind: i.Kind, Provider: i.Owner, FromVersion: i.Version, TargetVersion: i.Latest, Status: "manual", Reason: "installation_owner_not_supported", Cleanup: s.Cleanup, AffectedPaths: []string{}, AffectedIDs: []string{}, Binding: Binding{TargetID: i.ID, PackageName: i.Name, InstallationKind: i.Kind, Path: i.Path, Owner: i.Owner, Identity: i.Identity, Source: i.Source, Channel: i.Channel}}
	if voltaOwned(i, o) {
		p.Provider, p.Binding.Owner, p.Reason = "volta", "volta", "volta_owner_manual"
		return p
	}
	if s.MCPReference != "" && i.Kind != "mcp-server" {
		p.Status, p.Reason = "blocked", "mcp_reference_requires_server_selection"
		return p
	}
	if s.Action == "update" && i.Kind == "application" && i.Owner == "app-store" {
		return appStorePlan(i, p, report, o, s)
	}
	if s.PrivilegedRequest != nil {
		return privilegedPlan(i, p, o, s)
	}
	if i.Owner == "uv-standalone" {
		return nativeCLIPlan(i, p, report, o, s)
	}
	if i.Kind == "skill" {
		p = skillPlan(i, p, o, s)
		if p.Provider == "claude-plugin" && p.PluginUpdate != nil {
			for _, related := range report.Items {
				if related.ID != i.ID && related.Plugin != nil && related.Plugin.Root == p.PluginUpdate.Owner.Root {
					p.AffectedIDs = append(p.AffectedIDs, related.ID)
				}
			}
		}
		if p.Provider == "copied-skill" && p.Binding.Scope != "" {
			for _, related := range report.Items {
				if related.Kind != "skill" || related.ID == i.ID {
					continue
				}
				realPath, err := filepath.EvalSymlinks(related.Path)
				if err == nil && realPath == filepath.Join(p.Binding.Scope, "SKILL.md") {
					p.AffectedIDs = append(p.AffectedIDs, related.ID)
				}
			}
		}
		return p
	}
	if i.Kind == "mcp-server" {
		return mcpPlan(i, p, report, o, s)
	}
	if i.Kind == "application" && i.Owner == "homebrew" {
		p.Reason = "use_related_cask_target"
		if target := o.ResolvedTargets[i.ID]; target.ResolutionError != "" {
			p.Reason, p.TargetVersion = ResolutionFailureReason(target.ResolutionError), ""
		}
		p.AffectedIDs = append(p.AffectedIDs, i.Related...)
		return p
	}
	if i.Kind == "application" {
		return applicationPlan(i, p, report, o, s)
	}
	if !supported(i) {
		return p
	}
	p.Status, p.Reason = "blocked", "installation_binding_unverified"
	if !filepath.IsAbs(i.Path) || filepath.Clean(i.Path) != i.Path || i.Path == "/" || i.Path == o.Home {
		return p
	}
	rp, err := filepath.EvalSymlinks(i.Path)
	if err != nil {
		p.Reason = "installation_path_unavailable"
		return p
	}
	p.Binding.RealPath = rp
	p.Binding.WasActive = i.Active
	if i.RealPath != "" && i.RealPath != rp {
		p.Reason = "installation_path_changed"
		return p
	}
	if err = ownedPath(i.Path, false); err != nil {
		p.RequiresAdmin = true
		p.Status, p.Reason = "manual", "installation_requires_owner_permission"
		return p
	}
	if i.Health.Status == "error" {
		p.Reason = "installation_health_error"
		return p
	}
	for _, e := range i.Evidence {
		if strings.Contains(e, "dirty") || strings.Contains(e, "local modification") {
			p.Reason = "local_modifications_present"
			return p
		}
	}
	if resolved, ok := o.ResolvedTargets[i.ID]; ok && resolved.ResolutionError != "" {
		p.Reason = ResolutionFailureReason(resolved.ResolutionError)
		if i.Kind == "python-package" {
			p.Status = "manual"
		}
		if i.Kind == "node-package" && s.Action == "update" && (strings.HasPrefix(p.Reason, "node_") || p.Reason == "volta_owner_manual" || p.Reason == "owner_layout_unsupported" || p.Reason == "owner_receipt_unsupported") {
			p.Status = "manual"
		}
		p.TargetVersion = ""
		return p
	}
	manager, scope, reason := route(i, report, o, s.Action)
	if reason != "" {
		p.Reason = reason
		if i.Kind == "python-package" {
			p.Status = "manual"
		}
		return p
	}
	if audit.VoltaRoot(manager, o.Home, o.ManagerRoots) != "" {
		p.Status, p.Reason = "manual", "volta_owner_manual"
		return p
	}
	p.Binding.ManagerPath, p.Binding.Scope = manager, scope
	if i.Owner == "pnpm" || i.Owner == "bun" || i.Owner == "pipx" || i.Owner == "uv" {
		p.Binding.ManagerEnvFingerprint = hashText(strings.Join(ManagerEnvironment(i.Owner, p.Binding, o), "\x00"))
	}
	if s.Action == "update" {
		if _, ok := o.ResolvedTargets[i.ID]; !ok {
			p.Status, p.Reason = "manual", "owner_native_update_candidate_required"
			return p
		}
	}
	if resolved, ok := o.ResolvedTargets[i.ID]; ok {
		resolved.AffectedPackages = append([]string(nil), resolved.AffectedPackages...)
		sort.Strings(resolved.AffectedPackages)
		if resolved.Provider != i.Owner || resolved.Scope != scope || resolved.ManagerPath != manager || !resolved.OriginVerified || resolved.Source == "" {
			p.Reason = "owner_native_source_unverified"
			return p
		}
		if resolved.Node != nil {
			node := *resolved.Node
			resolved.Node = &node
			p.Binding.NodeVirtualStore = node.VirtualStore
		}
		if resolved.Python != nil {
			policy := *resolved.Python
			policy.Entrypoints = append([]PythonEntrypoint(nil), policy.Entrypoints...)
			resolved.Python = &policy
		}
		p.Binding.ResolvedTarget = &resolved
		if i.Kind == "python-package" {
			p.Binding.ManagerFingerprint, _ = fileHash(manager)
			if !validPythonPolicy(i, p.Binding, o, resolved.Python, s.Action) {
				p.Status, p.Reason = "manual", "python_policy_requires_new_preview"
				return p
			}
			p.Binding.Python = resolved.Python
			if s.Action == "update" {
				if e := PythonTargetAllowed(i, p.Binding, o, resolved.Version); e != nil {
					p.Status, p.Reason = "manual", e.Error()
					return p
				}
			}
		}
		if s.Action == "update" {
			if i.Kind == "node-package" && !validNodePolicy(i, p.Binding, resolved, o) {
				p.Status, p.Reason, p.TargetVersion = "manual", "node_policy_requires_new_preview", ""
				return p
			}
			p.TargetVersion = resolved.Version
			if p.TargetVersion == i.Version {
				p.Status, p.Reason = "skipped", "already_current"
				return p
			}
			if resolved.HasLifecycleScripts {
				p.RequiresScripts = true
				if !s.Special || (i.Owner != "npm" && i.Owner != "pnpm" && i.Owner != "bun") {
					p.Status, p.Reason = "manual", "lifecycle_scripts_require_separate_review"
					return p
				}
				p.SpecialApproved = true
			}
		}
	}
	if err = ownedPath(manager, true); err != nil {
		p.Reason = "manager_not_trusted_or_executable"
		return p
	}
	p.Binding.ManagerFingerprint, err = fileHash(manager)
	if err != nil {
		p.Reason = "manager_fingerprint_failed"
		return p
	}
	p.Binding.Fingerprint, err = installationHash(i, scope)
	if err != nil {
		p.Reason = "installation_fingerprint_failed"
		return p
	}
	if i.Kind == "python-package" {
		p.Binding.PythonRequirementFingerprint, err = pythonRequirementHash(i, scope)
		if err != nil {
			p.Reason = "python_requirement_unverified"
			return p
		}
		if p.Binding.Python == nil {
			p.Status, p.Reason = "manual", "python_policy_requires_new_preview"
			return p
		}
		p.Binding.PythonEntryFingerprint, err = pythonEntrypointHash(i, scope, p.Binding.Python.Entrypoints, p.Binding.Python.ManagerVersion)
		if err != nil {
			p.Status, p.Reason = "manual", "python_entrypoints_require_review"
			return p
		}

	}
	if i.Kind == "cask" && !caskSafe(i) {
		p.Status, p.Reason = "manual", "cask_installer_or_uninstall_effects_require_review"
		return p
	}
	if i.Kind == "cask" && s.Action == "update" && p.Binding.ResolvedTarget == nil {
		p.Status, p.Reason = "manual", "cask_candidate_effects_unverified"
		return p
	}
	p.AffectedPaths = append(p.AffectedPaths, i.Path)
	if i.Kind == "python-package" {
		p.AffectedPaths = []string{scope}
		if i.Owner == "pipx" && s.Action == "update" {
			p.AffectedPaths = append(p.AffectedPaths, filepath.Join(filepath.Dir(filepath.Dir(scope)), "shared"))
		}
		for _, related := range report.Items {
			if related.ID != i.ID && under(related.Path, scope) {
				p.AffectedIDs = append(p.AffectedIDs, related.ID)
			}
		}
	}
	if i.Kind == "formula" || i.Kind == "cask" {
		if resolved := p.Binding.ResolvedTarget; resolved != nil {
			for _, dependency := range resolved.AffectedPackages {
				parts := strings.Split(dependency, "/")
				valid := len(parts) == 1 || len(parts) == 3
				for _, part := range parts {
					valid = valid && regexp.MustCompile(`^[a-z0-9][a-z0-9@+._-]*$`).MatchString(part) && part != "." && part != ".."
				}
				if !valid {
					p.Status, p.Reason = "blocked", "candidate_dependency_scope_unverified"
					return p
				}
				name := filepath.Base(dependency)
				path := filepath.Join(scope, "Cellar", name)
				if !contains(p.AffectedPaths, path) {
					p.AffectedPaths = append(p.AffectedPaths, path)
				}
				for _, related := range report.Items {
					if related.Kind == "formula" && related.Path == path && !contains(p.AffectedIDs, related.ID) {
						p.AffectedIDs = append(p.AffectedIDs, related.ID)
					}
				}
			}
		}
		for _, dep := range i.Dependencies {
			for _, related := range report.Items {
				if related.Kind == "formula" && related.Name == filepath.Base(dep.Name) && under(related.Path, scope) {
					p.AffectedIDs = append(p.AffectedIDs, related.ID)
					p.AffectedPaths = append(p.AffectedPaths, related.Path)
				}
			}
		}
		for _, e := range i.Evidence {
			if strings.HasPrefix(e, "dependency:") {
				name := filepath.Base(strings.TrimPrefix(e, "dependency:"))
				for _, related := range report.Items {
					if related.Kind == "formula" && related.Name == name && under(related.Path, scope) && !contains(p.AffectedIDs, related.ID) {
						p.AffectedIDs = append(p.AffectedIDs, related.ID)
						p.AffectedPaths = append(p.AffectedPaths, related.Path)
					}
				}
			}
		}
	}
	if s.Action == "update" && i.Owner == "homebrew" && protectedDependencyMayChange(p, report) {
		p.Status, p.Reason = "manual", "protected_runtime_dependency_requires_separate_update"
		p.RequiredConsents = []string{"runtime_update"}
		return p
	}
	for _, r := range report.Items {
		if r.Kind == "application" && contains(r.Related, i.ID) {
			p.Applications = append(p.Applications, ApplicationBinding{ID: r.ID, Path: r.Path, Identity: r.Identity, Version: r.Version})
			p.AffectedIDs = append(p.AffectedIDs, r.ID)
			p.AffectedPaths = append(p.AffectedPaths, r.Path)
			proof := o.ResolvedTargets[r.ID]
			if proof.ResolutionError != "" {
				p.Status, p.Reason, p.TargetVersion = "manual", ResolutionFailureReason(proof.ResolutionError), ""
				p.Binding.ResolvedTarget = nil
				p.ApplicationLifecycle = nil
				if proof.ApplicationState == "application_requires_owner_permission" {
					p.Reason, p.RequiresAdmin = "application_requires_owner_permission", true
				}
				return p
			}
			if contains(s.QuitApps, r.ID) {
				t := o.ResolvedTargets[r.ID]
				if t.ApplicationState == "application_requires_owner_permission" {
					p.Status, p.Reason, p.RequiresAdmin, p.RequiresQuit = "manual", "application_requires_owner_permission", true, true
					return p
				}
				if s.Action != "update" || i.Kind != "cask" || !t.OriginVerified || t.Provider != "native-app" || t.Scope != r.Path || t.ApplicationLifecycle == nil || t.ApplicationState != "application_instances_verified" {
					p.RequiresQuit = true
					p.Status, p.Reason = "manual", "application_quit_target_unverified"
					return p
				}
				lifecycle := *t.ApplicationLifecycle
				if lifecycle.ID != r.ID || lifecycle.Path != r.Path || lifecycle.Identity != r.Identity || lifecycle.Version != r.Version || len(lifecycle.Instances) == 0 {
					p.Status, p.Reason = "blocked", "application_quit_binding_changed"
					return p
				}
				lifecycle.Reopen = contains(s.ReopenApps, r.ID)
				p.ApplicationLifecycle = append(p.ApplicationLifecycle, lifecycle)
				p.RequiresQuit = true
			} else {
				if !r.ActivityKnown || r.Active {
					p.RequiresQuit = true
					p.Status, p.Reason = "manual", "application_must_be_closed_and_verified"
					return p
				}
				if verified, ok := o.ResolvedTargets[r.ID]; !ok || !verified.OriginVerified || verified.Provider != "native-app" || verified.Scope != r.Path || !applicationClosed(verified) {
					p.RequiresQuit = true
					p.Status, p.Reason = "manual", "close_application_then_verify_all_bundle_processes"
					return p
				}
			}
		}
	}
	if s.Cleanup {
		paths, reason := cleanupPaths(i, report, o)
		if reason != "" {
			p.Reason = reason
			return p
		}
		p.CleanupPaths = paths
		p.AffectedPaths = append(p.AffectedPaths, paths...)
		if digest, err := cleanupHash(paths); err == nil {
			p.Binding.Fingerprint += "/" + digest
		} else {
			p.Reason = "cleanup_fingerprint_failed"
			return p
		}
	}
	if s.Action == "update" {
		if i.Kind == "node-package" && nodeScripts(i) {
			p.RequiresScripts = true
			if !s.Special || (i.Owner != "npm" && i.Owner != "pnpm" && i.Owner != "bun") {
				p.Status, p.Reason = "manual", "lifecycle_scripts_require_separate_review"
				return p
			}
			p.SpecialApproved = true
		}
		if p.TargetVersion == i.Version {
			p.Status, p.Reason = "skipped", "already_current"
			return p
		}
		if (i.Update.Status != "available" && p.Binding.ResolvedTarget == nil) || !version.MatchString(p.TargetVersion) {
			p.Status, p.Reason = "manual", "exact_update_candidate_unverified"
			return p
		}
	} else {
		p.TargetVersion = ""
		for _, r := range report.Items {
			if r.ID == i.ID {
				continue
			}
			if r.Kind == "mcp-server" && contains(r.Related, i.ID) {
				p.AffectedIDs = append(p.AffectedIDs, r.ID)
				p.Reason = "mcp_configuration_still_uses_installation"
				return p
			}
			if independentBrewDependency(i, r, p.Binding, o) {
				continue
			}
			for _, d := range r.Dependencies {
				if filepath.Base(d.Name) == i.Name && (i.Kind == "formula" || sameScope(i, r)) {
					p.AffectedIDs = append(p.AffectedIDs, r.ID)
					p.Reason = "required_by_other_installations"
					return p
				}
			}
			for _, e := range r.Evidence {
				if e == "dependency:"+i.Name || e == "dependency:"+i.Source+"/"+i.Name {
					p.AffectedIDs = append(p.AffectedIDs, r.ID)
					p.Reason = "required_by_other_installations"
					return p
				}
			}
		}
	}
	if i.Kind == "formula" && protectedRuntime(i.Name) {
		if !reviewedRuntime(i, &p, report, s) {
			return p
		}
		digest, err := runtimeTreeHash(filepath.Join(i.Path, i.Version))
		if err != nil {
			p.Status, p.Reason = "blocked", "runtime_content_unverified"
			return p
		}
		p.Binding.Fingerprint += "/" + digest
	}
	for _, d := range i.Dependencies {
		if d.Status == "error" {
			p.Reason = "dependency_issue_requires_review"
			return p
		}
	}
	p.Status, p.Reason = "ready", "original_manager_verified"
	return p
}

// ResolutionBinding establishes owner/root identity before asking the original
// manager for a candidate. It does not authorize any mutation or public source.
func ResolutionBinding(i audit.Item, r audit.Report, options Options) (Binding, error) {
	o := normalizeOptions(options)
	if voltaOwned(i, o) {
		return Binding{}, CommandFailure{Code: "volta_owner_manual"}
	}
	if !supported(i) || !filepath.IsAbs(i.Path) || filepath.Clean(i.Path) != i.Path {
		return Binding{}, errors.New("unsupported installation")
	}
	copyTargets := map[string]ResolvedTarget{}
	for k, v := range o.ResolvedTargets {
		copyTargets[k] = v
	}
	copyTargets[i.ID] = ResolvedTarget{OriginVerified: true}
	o.ResolvedTargets = copyTargets
	action := "uninstall"
	if i.Kind == "node-package" {
		action = "resolve"
	}
	manager, scope, reason := route(i, r, o, action)
	if reason != "" {
		return Binding{}, errors.New(reason)
	}
	if audit.VoltaRoot(manager, o.Home, o.ManagerRoots) != "" {
		return Binding{}, CommandFailure{Code: "volta_owner_manual"}
	}
	if err := ownedPath(i.Path, false); err != nil {
		return Binding{}, errors.New("installation permission unverified")
	}
	if err := ownedPath(manager, true); err != nil {
		if os.IsNotExist(err) {
			return Binding{}, errors.New("original_manager_unavailable")
		}
		return Binding{}, errors.New("manager permission unverified")
	}
	rp, err := filepath.EvalSymlinks(i.Path)
	if err != nil {
		return Binding{}, err
	}
	digest, err := fileHash(manager)
	if err != nil {
		return Binding{}, err
	}
	return Binding{Path: i.Path, RealPath: rp, Owner: i.Owner, Identity: i.Identity, Source: i.Source, Channel: i.Channel, Scope: scope, ManagerPath: manager, ManagerFingerprint: digest}, nil
}

func voltaOwned(i audit.Item, o Options) bool {
	return i.Owner == "volta" || audit.VoltaRoot(i.Path, o.Home, o.ManagerRoots) != ""
}

func supported(i audit.Item) bool {
	return (i.Owner == "homebrew" && (i.Kind == "formula" || i.Kind == "cask")) || (i.Kind == "node-package" && (i.Owner == "npm" || i.Owner == "pnpm" || i.Owner == "bun")) || (i.Kind == "python-package" && (i.Owner == "pipx" || i.Owner == "uv"))
}

func route(i audit.Item, report audit.Report, o Options, action string) (manager, scope, reason string) {
	if i.Owner == "homebrew" {
		if !simpleName.MatchString(i.Name) {
			return "", "", "invalid_package_identity"
		}
		if i.Kind == "formula" && i.Source != "homebrew/core" || i.Kind == "cask" && i.Source != "homebrew/cask" {
			return "", "", "untrusted_or_custom_tap"
		}
		rack := "Cellar"
		if i.Kind == "cask" {
			rack = "Caskroom"
		}
		for _, root := range o.BrewRoots {
			if i.Path != filepath.Join(root, rack, i.Name) {
				continue
			}
			if i.Kind == "formula" && (i.ActiveVersion == "" || i.ActiveVersion != i.Version) {
				return "", "", "active_version_unverified"
			}
			return filepath.Join(root, "bin/brew"), root, ""
		}
		return "", "", "installation_outside_manager_scope"
	}
	if i.Kind == "node-package" {
		if !packageName.MatchString(i.Name) || i.Identity != i.Name || !version.MatchString(i.Version) {
			return "", "", "invalid_package_identity"
		}
		root := filepath.Dir(i.Path)
		if strings.HasPrefix(i.Name, "@") {
			root = filepath.Dir(root)
		}
		if filepath.Base(root) != "node_modules" || filepath.Join(root, filepath.FromSlash(i.Name)) != i.Path {
			return "", "", "installation_outside_manager_scope"
		}
		prefix := filepath.Dir(filepath.Dir(root))
		switch i.Owner {
		case "npm":
			if filepath.Base(filepath.Dir(root)) != "lib" || (action == "update" && i.Source != "npm-public" && !o.ResolvedTargets[i.ID].OriginVerified) {
				return "", "", "package_source_unverified"
			}
		case "pnpm":
			prefix = filepath.Dir(root)
			if filepath.Base(prefix) != "5" || (filepath.Dir(prefix) != filepath.Join(o.Home, "Library/pnpm/global") && filepath.Dir(prefix) != filepath.Join(o.Home, ".local/share/pnpm/global") && !configuredManagerRoot(o, "pnpm", filepath.Dir(prefix))) {
				return "", "", "installation_outside_manager_scope"
			}
			if !o.ResolvedTargets[i.ID].OriginVerified {
				return "", "", "original_manager_scope_unverified"
			}
			if !nodeDirect(i, prefix, action) {
				return "", "", "not_direct_global_dependency"
			}
			if action == "update" && !nodeOrigin(i, root) && !o.ResolvedTargets[i.ID].OriginVerified {
				return "", "", "package_source_unverified"
			}
		case "bun":
			prefix = filepath.Dir(root)
			if (prefix != filepath.Join(o.Home, ".bun/install/global") && !configuredManagerRoot(o, "bun", prefix)) || (action == "update" && !nodeOrigin(i, root) && !o.ResolvedTargets[i.ID].OriginVerified) {
				return "", "", "package_source_unverified"
			}
			if !nodeDirect(i, prefix, action) {
				return "", "", "not_direct_global_dependency"
			}
		}
		if !under(prefix, o.Home) {
			allowed := configuredManagerRoot(o, i.Owner, prefix) || (i.Owner == "pnpm" && configuredManagerRoot(o, i.Owner, filepath.Dir(prefix)))
			for _, br := range o.BrewRoots {
				allowed = allowed || prefix == br
			}
			if !allowed {
				return "", "", "installation_outside_manager_scope"
			}
		}
		if action == "update" {
			realRoot, er := filepath.EvalSymlinks(root)
			realPackage, ep := filepath.EvalSymlinks(i.Path)
			if er != nil || ep != nil || i.Owner != "pnpm" && realPackage != filepath.Join(realRoot, filepath.FromSlash(i.Name)) || i.Owner == "pnpm" && !pnpmPackagePath(i, prefix) {
				return "", "", "linked_package_update_requires_review"
			}
		}
		manager = findManager(i.Owner, report, o, prefix)
		if i.Owner == "npm" && manager != "" && filepath.Dir(manager) != filepath.Join(prefix, "bin") {
			// An explicit --prefix is still necessary; one manager can own several global prefixes.
			if !under(prefix, o.Home) {
				return "", "", "manager_prefix_mismatch"
			}
		}
		if manager == "" {
			return "", "", "original_manager_unavailable"
		}
		return manager, prefix, ""
	}
	if i.Kind == "python-package" {
		if !simpleName.MatchString(i.Name) || !version.MatchString(i.Version) || i.Source == "direct-url" {
			return "", "", "package_source_unverified"
		}
		env := ""
		for _, e := range i.Evidence {
			if strings.HasPrefix(e, "environment:") {
				env = strings.TrimPrefix(e, "environment:")
			}
		}
		allowed := false
		bases := []string{filepath.Join(o.Home, ".local/share/uv/tools")}
		if i.Owner == "pipx" {
			bases = []string{filepath.Join(o.Home, ".local/share/pipx/venvs"), filepath.Join(o.Home, ".local/pipx/venvs")}
		}
		if root := o.ManagerRoots[i.Owner]; configuredManagerRoot(o, i.Owner, root) {
			bases = append(bases, root)
		}
		for _, base := range bases {
			allowed = allowed || filepath.Dir(env) == base && under(i.Path, env)
		}
		if !allowed || filepath.Base(env) != normalizePython(i.Name) {
			return "", "", "python_dependency_not_direct_tool"
		}
		if _, err := readPythonReceipt(i, env, managerBin(i.Owner, Binding{Scope: env}, o), action); err != nil {
			return "", "", err.Error()
		}
		if action == "update" && !o.ResolvedTargets[i.ID].OriginVerified {
			return "", "", "package_source_unverified"
		}
		manager = findManager(i.Owner, report, o, env)
		if manager == "" {
			return "", "", "original_manager_unavailable"
		}
		return manager, env, ""
	}
	return "", "", "installation_owner_not_supported"
}

func findManager(owner string, r audit.Report, o Options, scope string) string {
	if p := o.ManagerPaths[owner]; filepath.IsAbs(p) {
		return p
	}
	if bin := o.ManagerBins[owner]; filepath.IsAbs(bin) && filepath.Clean(bin) == bin && bin != "/" {
		if p := filepath.Join(bin, owner); ownedPath(p, true) == nil {
			return p
		}
	}
	if owner == "npm" {
		p := filepath.Join(scope, "bin/npm")
		if ownedPath(p, true) == nil {
			return p
		}
	}
	for _, i := range r.Items {
		if i.Kind == "executable" && i.Name == owner && i.Active && i.Health.Status == "ok" && filepath.IsAbs(i.Path) {
			return i.Path
		}
	}
	var candidates []string
	if owner == "bun" {
		candidates = append(candidates, filepath.Join(o.Home, ".bun/bin/bun"))
	}
	if owner == "pnpm" {
		candidates = append(candidates, filepath.Join(o.Home, "Library/pnpm/pnpm"), filepath.Join(o.Home, ".local/share/pnpm/pnpm"))
	}
	for _, root := range o.BrewRoots {
		if filepath.IsAbs(root) && filepath.Clean(root) == root && root != "/" {
			candidates = append(candidates, filepath.Join(root, "bin", owner))
		}
	}
	if owner == "uv" || owner == "pipx" || owner == "pnpm" || owner == "claude" {
		candidates = append(candidates, filepath.Join(o.Home, ".local/bin", owner))
	}
	for _, p := range candidates {
		if ownedPath(p, true) == nil {
			return p
		}
	}
	return ""
}

func protectedRuntime(name string) bool {
	for _, n := range []string{"node", "python", "python3", "java", "openjdk", "ruby", "perl", "php", "go", "rust", "bun", "pipx", "uv", "npm", "pnpm", "openssl", "readline", "sqlite", "icu4c"} {
		if name == n || strings.HasPrefix(name, n+"@") {
			return true
		}
	}
	return false
}
func sameScope(a, b audit.Item) bool { return filepath.Dir(a.Path) == filepath.Dir(b.Path) }

// Exclude a same-name dependency only when its separate installation is proven.
func independentBrewDependency(target, consumer audit.Item, binding Binding, o Options) bool {
	if target.Kind != "formula" || target.Owner != "homebrew" || consumer.Owner != "homebrew" || !simpleName.MatchString(consumer.Name) {
		return false
	}
	rack := "Cellar"
	if consumer.Kind == "cask" {
		rack = "Caskroom"
	} else if consumer.Kind != "formula" {
		return false
	}
	for _, root := range o.BrewRoots {
		if !filepath.IsAbs(root) || consumer.Path != filepath.Join(root, rack, consumer.Name) || root == binding.Scope {
			continue
		}
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return false
		}
		realConsumer, err := filepath.EvalSymlinks(consumer.Path)
		if err != nil || realConsumer != filepath.Join(realRoot, rack, consumer.Name) {
			return false
		}
		dependency, err := filepath.EvalSymlinks(filepath.Join(root, "Cellar", target.Name))
		if err != nil || under(dependency, binding.RealPath) || under(binding.RealPath, dependency) {
			return false
		}
		active, err := filepath.EvalSymlinks(filepath.Join(root, "opt", target.Name))
		if err != nil || filepath.Dir(active) != dependency {
			return false
		}
		st, err := os.Stat(active)
		return err == nil && st.IsDir()
	}
	return false
}
func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}
func under(p, root string) bool {
	r, e := filepath.Rel(root, p)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}
func normalizePython(n string) string {
	return strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(n))
}

func ownedPath(path string, executable bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("invalid path")
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if executable && (!st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0) {
		return errors.New("not executable")
	}
	if st.Mode().Perm()&0022 != 0 {
		return errors.New("writable by other users")
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) && stat.Uid != 0 {
		return errors.New("unknown owner")
	}
	if !executable && stat.Uid != uint32(os.Getuid()) {
		return errors.New("installation requires owner permission")
	}
	if syscall.Access(path, 4) != nil {
		return errors.New("unreadable")
	}
	if !executable && syscall.Access(filepath.Dir(path), 2) != nil {
		return errors.New("parent not writable")
	}
	return nil
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", errors.New("not regular")
	}
	if st.Size() > 256<<20 {
		return "", errors.New("file too large")
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func installationHash(i audit.Item, scope string) (string, error) {
	h := sha256.New()
	b, _ := json.Marshal(struct {
		ID, Kind, Name, Path, Owner, Source, Version, Active, Channel string
		Versions, Architecture                                        []string
	}{i.ID, i.Kind, i.Name, i.Path, i.Owner, i.Source, i.Version, i.ActiveVersion, i.Channel, i.Versions, i.Architecture})
	h.Write(b)
	var paths []string
	switch i.Kind {
	case "formula":
		paths = []string{filepath.Join(i.Path, i.Version, "INSTALL_RECEIPT.json")}
	case "cask":
		paths = []string{filepath.Join(i.Path, ".metadata/INSTALL_RECEIPT.json")}
	case "node-package":
		paths = []string{filepath.Join(i.Path, "package.json")}
	case "python-package":
		paths = []string{filepath.Join(i.Path, "METADATA")}
		if i.Owner == "pipx" {
			paths = append(paths, filepath.Join(scope, "pipx_metadata.json"))
		} else {
			paths = append(paths, filepath.Join(scope, "uv-receipt.toml"))
		}
	}
	for _, p := range paths {
		digest, err := fileHash(p)
		if err != nil {
			return "", err
		}
		fmt.Fprintln(h, p, digest)
	}
	st, err := os.Stat(i.Path)
	if err != nil {
		return "", err
	}
	fmt.Fprintln(h, st.Mode(), st.ModTime().UnixNano())
	return hex.EncodeToString(h.Sum(nil)), nil
}
