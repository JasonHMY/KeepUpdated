package manage

import (
	"context"
	"reflect"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/privileged"
)

func privilegeClient(o Options) privileged.Client {
	if o.PrivilegedClient != nil {
		return o.PrivilegedClient
	}
	return privileged.NativeClient{}
}

// ResolvePrivilegedTargets queries the shared native boundary without authorizing
// or installing anything. A typed request is bound to one existing inventory row.
func ResolvePrivilegedTargets(ctx context.Context, report audit.Report, s Selection, o Options, _ Runner) (map[string]ResolvedTarget, error) {
	out := map[string]ResolvedTarget{}
	if s.PrivilegedRequest == nil || s.Action != "update" || s.All || len(s.IDs) != 1 {
		return out, nil
	}
	r := *s.PrivilegedRequest
	for _, item := range report.Items {
		if item.ID == s.IDs[0] && (item.Owner == "app-store" || r.AppStore != nil) {
			out[item.ID] = ResolvedTarget{Provider: "app-store", Scope: item.Path, Reason: "app_store_manual_update"}
			return out, ctx.Err()
		}
	}
	if err := r.Validate(); err != nil {
		return out, err
	}
	check, err := privilegeClient(o).Check(ctx, r)
	if err != nil {
		return out, err
	}
	for _, item := range report.Items {
		if item.ID != s.IDs[0] {
			continue
		}
		target := ResolvedTarget{Provider: "privileged", Scope: item.Path, Source: "native-authorization-boundary", Version: item.Version, FromVersion: item.Version, PrivilegedRequest: &r, Reason: check.Reason, OriginVerified: check.Status == "ready"}
		if r.Package != nil {
			target.Version = r.Package.Version
		}
		if r.System != nil {
			target.Version = r.System.Version
		}
		if r.AppStore != nil {
			target.Version = r.AppStore.Version
		}
		out[item.ID] = target
	}
	return out, nil
}

func privilegedPlan(i audit.Item, p PlanItem, o Options, s Selection) PlanItem {
	p.Provider = "privileged"
	p.Status, p.Reason = "blocked", "privileged_operation_requires_single_target"
	p.RequiresAdmin = true
	p.RequiredConsents = []string{ConsentPrivilegedInstall}
	if s.Action != "update" || s.All || len(s.IDs) != 1 || s.IDs[0] != i.ID || s.PrivilegedRequest == nil {
		return p
	}
	if strings.Contains(i.Update.Code, "pinned") || strings.Contains(i.Update.Code, "constraint") {
		p.Reason = "version_constraint_requires_owner_review"
		return p
	}
	for _, evidence := range i.Evidence {
		if strings.Contains(evidence, "dirty") || strings.Contains(evidence, "local modification") {
			p.Reason = "local_modifications_present"
			return p
		}
	}
	request := *s.PrivilegedRequest
	if request.Validate() != nil {
		p.Reason = "privileged_request_invalid"
		return p
	}
	if request.Package != nil {
		if i.Kind != "application" || i.Owner == "system" || i.Owner == "app-component" || i.Owner == "homebrew" || i.Owner == "app-store" || strings.Contains(i.Path, ".app/") || i.Version != request.Package.FromVersion || i.Path != request.Package.InstallationPath || i.Identity != request.Package.BundleID {
			p.Reason = "package_installation_binding_mismatch"
			return p
		}
		if i.Active || !i.ActivityKnown {
			p.Reason = "close_application_then_verify_all_bundle_processes"
			p.RequiresQuit = true
			return p
		}
		p.TargetVersion = request.Package.Version
	} else if request.System != nil {
		if i.Kind != "os" || i.Owner != "system" || i.Source != "apple" || i.Path != "/System/Library/CoreServices/SystemVersion.plist" || i.Version != request.System.FromVersion {
			p.Reason = "system_installation_binding_mismatch"
			return p
		}
		p.RequiresScripts = true
		p.TargetVersion = request.System.Version
		if !s.Special {
			p.Reason = "lifecycle_scripts_require_separate_review"
			return p
		}
		p.SpecialApproved = true
	} else if request.RestartOf != "" {
		if i.Kind != "os" || i.Owner != "system" || i.Source != "apple" || i.Path != "/System/Library/CoreServices/SystemVersion.plist" {
			p.Reason = "restart_requires_system_target"
			return p
		}
		p.RequiresRestart = true
		p.RequiredConsents = []string{ConsentPrivilegedInstall, ConsentRestartSystem}
	} else {
		// App Store provider owns inventory and account/manager checks; it can attach
		// its typed request to its PlanItem and call the same executor below.
		p.Reason = "use_app_store_provider"
		return p
	}
	p.PrivilegedRequest = &request
	if !HasConsents(s, p.RequiredConsents...) {
		p.Reason = "explicit_action_consent_required"
		return p
	}
	resolved, ok := o.ResolvedTargets[i.ID]
	if !ok || resolved.PrivilegedRequest == nil || !reflect.DeepEqual(*resolved.PrivilegedRequest, request) {
		p.Reason = "privileged_preflight_required"
		return p
	}
	p.Binding.ResolvedTarget = &resolved
	p.Binding.RealPath = i.RealPath
	p.Binding.Scope = i.Path
	p.AffectedPaths = []string{i.Path, "/"}
	p.Binding.Fingerprint = hashText(i.ID + "\x00" + i.Path + "\x00" + i.Identity + "\x00" + i.Version + "\x00" + i.Source + "\x00" + i.Channel)
	if !resolved.OriginVerified {
		p.Reason = resolved.Reason
		return p
	}
	p.Status, p.Reason = "ready", "typed_privileged_target_verified"
	return p
}

func applyPrivileged(ctx context.Context, p PlanItem, s Selection, o Options, operationID string) ItemResult {
	r := ItemResult{ID: p.ID, Name: p.Name, BeforeVersion: p.FromVersion, Status: "blocked", Reason: "explicit_action_consent_required"}
	if p.PrivilegedRequest == nil || s.All || len(s.IDs) != 1 || !HasConsents(s, p.RequiredConsents...) || !HasConsents(s, ConsentPrivilegedInstall) {
		return r
	}
	if p.PrivilegedRequest.Kind == "restart" && !HasConsents(s, ConsentRestartSystem) {
		return r
	}
	// The outer plan ID is already known to the caller and durably claimed by
	// Apply before this call. It remains the helper lookup key after any lost reply.
	request := *p.PrivilegedRequest
	request.OperationID = operationID
	r.PrivilegedOperationID = operationID
	result, err := privilegeClient(o).Execute(ctx, request, privileged.Confirmation(request))
	r.RecoveryPath = result.RecoveryPath
	r.BackupPath = result.BackupPath
	r.Status, r.Reason = result.Status, result.Reason
	r.Executed, r.CommandSucceeded = result.Executed, result.CommandSucceeded
	r.AfterVersion = result.ObservedVersion
	r.Changed = r.AfterVersion != "" && r.AfterVersion != p.FromVersion
	if result.Status == "installed" {
		if result.CommandSucceeded && result.ObservedVersion == p.TargetVersion {
			r.Status = "updated"
		} else {
			r.Status, r.Reason = "verification_unknown", "privileged_target_version_unverified"
		}
	}
	if err != nil {
		r.Status, r.Reason = "verification_unknown", "privileged_result_unavailable_do_not_retry"
	}
	return r
}
