package manage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/configedit"
)

// Apply rejects edited, expired and replayed plans before any installation command.
func Apply(ctx context.Context, p Plan, confirm string, e Engine) (out ApplyResult, err error) {
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	start := now()
	out = ApplyResult{Schema: ApplySchema, PlanID: p.PlanID, StartedAt: start.UTC().Format(time.RFC3339Nano), Results: []ItemResult{}}
	defer func() {
		if out.State == "" {
			out.FinishedAt = now().UTC().Format(time.RFC3339Nano)
		}
	}()
	if confirm != p.PlanID {
		return out, errors.New("plan or exact confirmation does not match")
	}
	if err := ValidatePlan(p, start); err != nil {
		return out, err
	}
	created, _ := time.Parse(time.RFC3339Nano, p.CreatedAt)
	expires, _ := time.Parse(time.RFC3339Nano, p.ExpiresAt)
	if e.Snapshot == nil {
		return out, errors.New("fresh inventory reader required")
	}
	if e.ResolveTargets == nil {
		for _, pi := range p.Items {
			if pi.Status == "ready" && (pi.Binding.ResolvedTarget != nil || len(pi.Applications) > 0 || p.Selection.MCPCandidate != "") {
				return out, errors.New("fresh owner-native resolver required")
			}
		}
	}
	if e.Runner == nil {
		e.Runner = ExecRunner{}
	}
	o := normalizeOptions(e.Options)
	if e.StateDir == "" {
		e.StateDir = ActionStateDir(o.Home)
	}
	if err := operationDirectory(e.StateDir, true); err != nil {
		return out, err
	}
	unlock, err := lockState(e.StateDir)
	if err != nil {
		return out, err
	}
	defer unlock()
	current, err := managementSnapshot(ctx, e, "Approved plan")
	if err != nil {
		return out, errors.New("fresh inventory unavailable")
	}
	if e.ResolveTargets != nil {
		o.ResolvedTargets, err = e.ResolveTargets(ctx, current, p.Selection)
		if err != nil {
			return out, errors.New("owner-native target query unavailable")
		}
	}
	o.Now, o.TTL = created, expires.Sub(created)
	rebuilt, err := NewPlan(current, p.Selection, o)
	if err != nil || !reflect.DeepEqual(rebuilt, p) {
		return out, errors.New("plan no longer matches the current installation, source, manager or update candidate")
	}
	// Do not overwrite a damaged or unsupported record silently.
	if _, err := ReadRecentOperation(e.StateDir); err != nil {
		return out, err
	}
	claim := filepath.Join(e.StateDir, p.PlanID+".attempted")
	f, err := os.OpenFile(claim, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return out, errors.New("plan already attempted; create and confirm a new plan")
	}
	_, writeErr := f.WriteString(out.StartedAt + "\n")
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return out, errors.New("could not durably record action attempt")
	}
	if err := syncSkillDir(e.StateDir); err != nil {
		return out, errors.New("could not durably record action attempt")
	}
	out.Action, out.State = p.Action, "in_progress"
	for _, pi := range p.Items {
		out.OriginalIDs = append(out.OriginalIDs, pi.ID)
		out.Results = append(out.Results, ItemResult{ID: pi.ID, Name: pi.Name, BeforeVersion: pi.FromVersion, Status: "skipped", Reason: "operation_not_started"})
	}
	if err := saveOperation(e, out); err != nil {
		return out, fmt.Errorf("could not save operation before execution: %w", err)
	}
	var journalErr error
	finished := false
	defer func() {
		if journalErr != nil {
			err = fmt.Errorf("could not save operation results; later items were stopped: %w", journalErr)
			return
		}
		if !finished {
			return
		}
		out.State, out.FinishedAt = "finished", now().UTC().Format(time.RFC3339Nano)
		if saveErr := saveOperation(e, out); saveErr != nil {
			out.State, out.FinishedAt = "in_progress", ""
			err = fmt.Errorf("could not save final operation results: %w", saveErr)
		}
	}()
	canonicalResults := map[string]ItemResult{}
	configHashes := map[string]string{}
	batchSatisfied := map[string]string{}
	pluginRefreshPlans := map[string]PlanItem{}
	total := len(p.Items)
	progress := audit.NewPhaseProgress(ctx, o.Progress, "apply", &total)
	defer progress.Finish()
	itemIndex := 0
	complete := func(result ItemResult) {
		// Keep recovery links recorded before a provider returned its final result.
		// A provider may have removed its prepared location after safe rollback.
		if result.BackupPath == "" && recoveryLocationExists(out.Results[itemIndex].BackupPath) {
			result.BackupPath = out.Results[itemIndex].BackupPath
		}
		if result.RecoveryPath == "" && recoveryLocationExists(out.Results[itemIndex].RecoveryPath) {
			result.RecoveryPath = out.Results[itemIndex].RecoveryPath
		}
		for _, path := range out.Results[itemIndex].RecoveryPaths {
			if recoveryLocationExists(path) {
				result.RecoveryPaths = append(result.RecoveryPaths, path)
			}
		}
		out.Results[itemIndex] = result
		if journalErr == nil {
			journalErr = saveOperation(e, out)
		}
		canonicalResults[result.ID] = result
		progress.Begin(result.Name)
		resolved := result.Status == "updated" || result.Status == "uninstalled" || result.Status == "configured" || result.Status == "skipped" && (result.Reason == "already_current" || result.Reason == "duplicate_installation_target" && result.CanonicalID != "")
		progress.Done(!resolved)
	}
	for index, pi := range p.Items {
		if journalErr != nil {
			return out, journalErr
		}
		itemIndex = index
		progress.Begin(pi.Name)
		r := ItemResult{ID: pi.ID, Name: pi.Name, Status: pi.Status, Reason: pi.Reason, BeforeVersion: pi.FromVersion}
		if pi.Status != "ready" {
			if pi.CanonicalID != "" {
				canonical, found := canonicalResults[pi.CanonicalID]
				if found {
					r.CanonicalID = canonical.ID
				}
				if !found || !canonical.Executed {
					r.Reason = "duplicate_canonical_not_executed"
				} else if canonical.Status != "updated" && canonical.Status != "uninstalled" && canonical.Status != "configured" {
					r.Reason = "duplicate_canonical_not_verified"
				}
			}
			complete(r)
			continue
		}
		if ctx.Err() != nil {
			r.Status, r.Reason = "skipped", "cancelled_before_execution"
			complete(r)
			continue
		}
		out.Results[index].Status, out.Results[index].Reason = "verification_unknown", "operation_in_progress_or_interrupted"
		if pi.PrivilegedRequest != nil {
			out.Results[index].PrivilegedOperationID = p.PlanID
		}
		journalErr = saveOperation(e, out)
		if journalErr != nil {
			return out, journalErr
		}
		o.recordRecovery = func(backup, recovery string) error {
			if backup != "" {
				if out.Results[index].BackupPath == "" {
					out.Results[index].BackupPath = backup
				} else if out.Results[index].BackupPath != backup && !contains(out.Results[index].RecoveryPaths, backup) {
					out.Results[index].RecoveryPaths = append(out.Results[index].RecoveryPaths, backup)
				}
			}
			if recovery != "" {
				out.Results[index].RecoveryPath = recovery
			}
			journalErr = saveOperation(e, out)
			return journalErr
		}
		if reconciled, ok := pluginRefreshPlans[pi.ID]; ok {
			pi = reconciled
		}
		fresh, readErr := managementSnapshot(ctx, e, pi.Name)
		if readErr != nil {
			r.Status, r.Reason = "blocked", "preflight_inventory_unavailable"
			complete(r)
			continue
		}
		selection := lifecycleItemSelection(p.Selection, pi)
		if e.ResolveTargets != nil {
			o.ResolvedTargets, readErr = e.ResolveTargets(ctx, fresh, selection)
			if readErr != nil {
				r.Status, r.Reason = "blocked", "owner_native_preflight_unavailable"
				complete(r)
				continue
			}
		}
		if pi.MCP != nil {
			if expected, ok := configHashes[pi.MCP.RealPath]; ok {
				data, readErr := readMetadata(pi.MCP.ConfigPath)
				if readErr != nil || hashText(string(data)) != expected {
					r.Status, r.Reason = "blocked", "configuration_changed_after_previous_selected_edit"
					complete(r)
					continue
				}
				copyTarget := *pi.MCP
				copyTarget.SHA256 = expected
				pi.MCP = &copyTarget
				pi.Binding.Fingerprint = expected
			}
		}
		one, planErr := NewPlan(fresh, selection, o)
		if planErr != nil || len(one.Items) != 1 || !reflect.DeepEqual(one.Items[0], pi) {
			if p.Action == "update" && planErr == nil && len(one.Items) == 1 && batchTargetStillSatisfied(pi, one.Items[0], fresh, batchSatisfied[pi.ID]) {
				verifyResult(&r, pi, "update", fresh, nil)
				if r.Status == "updated" {
					r.Status, r.Reason = "skipped", "already_current"
				}
				complete(r)
				continue
			}
			r.Status, r.Reason = "blocked", "installation_changed_since_preview"
			complete(r)
			continue
		}
		progress.Begin(pi.Name)
		if pi.Provider == "claude-plugin" {
			result := applyPlugin(ctx, pi, o, e.StateDir, e.Runner)
			complete(result)
			for id, reconciled := range reconcilePluginRefresh(ctx, p, pi, result, e, o) {
				pluginRefreshPlans[id] = reconciled
			}
			continue
		}
		executionItem := pi
		if len(pi.ApplicationLifecycle) > 0 {
			var quitErr error
			r.ApplicationLifecycle, quitErr = QuitApplications(ctx, pi.ApplicationLifecycle, e.Runner, e.LifecycleTimeout)
			out.Results[index].ApplicationLifecycle = r.ApplicationLifecycle
			journalErr = saveOperation(e, out)
			if journalErr != nil {
				return out, journalErr
			}
			if quitErr == nil {
				executionItem, quitErr = revalidateAfterQuit(ctx, pi, selection, e, o)
			}
			if quitErr != nil {
				r.Status, r.Reason = "blocked", quitErr.Error()
				complete(r)
				continue
			}
		}
		var approvedEffects map[string]audit.Item
		if p.Action == "update" {
			approvedEffects = approvedBatchEffects(p.Items, pi, fresh, batchSatisfied)
		}
		progress.Begin(pi.Name)
		var runErr error
		var pythonReceiptErr error
		var configResult configedit.Result
		if pi.Provider == "app-store" && pi.PrivilegedRequest != nil {
			r = applyAppStore(ctx, pi, p.Selection, o, e, p.PlanID)
			complete(r)
			continue
		} else if pi.PrivilegedRequest != nil {
			r = applyPrivileged(ctx, pi, p.Selection, o, p.PlanID)
			complete(r)
			continue
		} else if pi.Provider == "uv-standalone" {
			r.BackupPath, r.Changed, runErr = applyNativeCLI(ctx, pi, o, e.StateDir, e.Runner, &r)
		} else if pi.Provider == "user-skill" {
			runErr = moveSkill(pi.Binding.Scope, o.Home, p.PlanID, func(path string) error { return recordRecovery(o, path, "") })
		} else if pi.Provider == "git-skill" {
			runErr = applyGit(ctx, pi, o, e.Runner)
		} else if pi.Provider == "copied-skill" {
			r.BackupPath, r.Changed, runErr = applyCopiedSkill(ctx, pi, o, e.StateDir)
		} else if pi.Provider == "native-app" {
			if p.Action == "update" {
				r.BackupPath, r.Changed, runErr = applyApplicationUpdate(ctx, executionItem, o, e.Runner)
			} else {
				runErr = moveApplication(pi.Binding.Scope, o.Home, p.PlanID, func(path string) error { return recordRecovery(o, path, "") })
			}
		} else if pi.Provider == "mcp-config" && pi.MCP != nil {
			configResult, runErr = configedit.ApplyWithBackup(*pi.MCP, func(path string) error { return recordRecovery(o, path, "") })
			r.BackupPath = configResult.BackupPath
			r.Changed = configResult.Changed
			if runErr == nil {
				configHashes[pi.MCP.RealPath] = configResult.ContentSHA256
			}
		} else {
			command, buildErr := commandFor(executionItem, p.Action, o, e.StateDir)
			if buildErr != nil {
				r.Status, r.Reason = "blocked", "provider_command_unavailable"
				complete(r)
				continue
			}
			if pi.Provider == "pipx" && p.Action == "update" {
				if err := preparePipxConstraint(e.StateDir, pi); err != nil {
					r.Status, r.Reason = "blocked", "approved_python_constraint_unavailable"
					complete(r)
					continue
				}
			}
			if len(pi.ApplicationLifecycle) > 0 {
				if err := VerifyApplicationsClosed(ctx, pi.ApplicationLifecycle, e.Runner); err != nil {
					r.Status, r.Reason = "blocked", err.Error()
					complete(r)
					continue
				}
			}
			var originalUVReceipt []byte
			if pi.Provider == "uv" && p.Action == "update" {
				var err error
				originalUVReceipt, r.BackupPath, err = captureUVReceipt(pi, e.StateDir, p.PlanID)
				if err != nil {
					r.Status, r.Reason = "blocked", "python_receipt_backup_unavailable"
					complete(r)
					continue
				}
			}
			if r.BackupPath != "" {
				if err := recordRecovery(o, r.BackupPath, ""); err != nil {
					return out, err
				}
			}
			_, runErr = e.Runner.Run(ctx, command)
			if originalUVReceipt != nil && runErr == nil {
				pythonReceiptErr = restoreUVInvocationOption(pi, originalUVReceipt)
			}
		}
		if pi.Provider != "uv-standalone" {
			r.Executed = true
			r.CommandSucceeded = runErr == nil
		}
		if runErr == nil && (pi.Provider == "user-skill" || pi.Provider == "native-app") {
			r.Changed = true
		}
		post, postErr := managementSnapshot(ctx, e, pi.Name)
		if postErr != nil {
			r.Status, r.Reason = "verification_unknown", "post_action_inventory_unavailable"
			if runErr != nil {
				r.Reason = "manager_failed_and_inventory_unavailable"
			}
			complete(r)
			continue
		}
		if pi.Provider == "uv-standalone" {
			if runErr != nil {
				r.Status, r.Reason = "failed", "native_cli_preparation_failed"
				if strings.HasPrefix(runErr.Error(), "native_cli_") {
					r.Reason = runErr.Error()
				}
			} else if verifyNativeCLI(pi, o) {
				verifyResult(&r, pi, p.Action, post, nil)
			} else {
				r.Status, r.Reason = "verification_unknown", "native_cli_post_verification_failed"
			}
		} else if pi.Provider == "mcp-config" {
			r.Changed = configResult.Changed
			if runErr != nil {
				r.Status, r.Reason = "failed", "mcp_configuration_edit_failed"
			} else if pi.MCP != nil && pi.MCP.PackageReference != nil {
				if mcpReferenceVerified(pi, post, configHashes[pi.MCP.RealPath]) {
					r.Status, r.Reason = "configured", "package_reference_changed_server_not_started"
				} else {
					r.Status, r.Reason = "verification_unknown", "mcp_reference_postverification_failed"
				}
			} else if containsReportID(post, pi.ID) {
				r.Status, r.Reason = "verification_unknown", "configuration_removed_but_inventory_still_contains_definition"
			} else {
				r.Status, r.Reason = "uninstalled", "configuration_removed_shared_software_preserved"
			}
		} else if pi.Provider == "copied-skill" {
			if runErr != nil {
				r.Status, r.Reason = "failed", copiedError(runErr, "skill_update_failed")
			} else if verifyCopiedSkill(pi, o) {
				r.Status, r.Reason, r.AfterVersion, r.Changed = "updated", "approved_skill_content_verified", pi.TargetVersion, true
			} else {
				r.Status, r.Reason = "verification_unknown", "skill_update_verification_failed"
			}
		} else if pi.Provider == "native-app" && p.Action == "update" {
			if runErr != nil {
				r.Status, r.Reason = "failed", applicationUpdateError(runErr)
			} else {
				verifyResult(&r, pi, p.Action, post, nil)
				found, unique := currentInstallation(post, pi)
				expected := pi.Binding.ResolvedTarget.ApplicationUpdate
				if unique && found != nil && found.Version == pi.TargetVersion && found.Build == expected.Archive.Build && installationPathVerified(*found, pi) && found.Owner == pi.Binding.Owner && sourceVerified(*found, pi) && identityVerified(*found, pi) && found.Channel == pi.Binding.Channel && found.Health.Status != "error" {
					r.Status, r.Reason, r.Changed = "updated", "signed_application_bundle_verified", true
				} else {
					r.Status, r.Reason = "verification_unknown", "application_post_inventory_mismatch"
					r.Changed = true
				}
			}
		} else if pi.Provider == "git-skill" && gitVerified(ctx, pi, o, e.Runner) {
			r.Status, r.Reason, r.AfterVersion, r.Changed = "updated", "approved_git_commit_verified", pi.TargetVersion, pi.TargetVersion != pi.FromVersion
		} else {
			verifyResult(&r, pi, p.Action, post, runErr)
		}
		if pythonReceiptErr != nil {
			r.Status, r.Reason = "verification_unknown", "python_receipt_restore_unverified"
		}
		r.RelatedChanges = observedChanges(fresh, post, pi)
		if p.Action == "update" && r.CommandSucceeded && r.Status == "updated" {
			recordBatchSatisfaction(p.Items, approvedEffects, post, batchSatisfied)
			reopenApplications(ctx, pi, post, e.Runner, r.ApplicationLifecycle)
		}
		if r.Status == "uninstalled" && pi.Cleanup && len(pi.CleanupPaths) > 0 {
			if err := moveCleanup(pi.CleanupPaths, o.Home, p.PlanID, func(path string) error { return recordRecovery(o, path, "") }); err != nil {
				r.Status, r.Reason = "cleanup_failed", "installation_removed_but_some_data_not_moved"
			} else {
				r.Reason = "installation_removed_and_selected_data_moved_to_trash"
			}
		}
		complete(r)
	}
	finished = true
	return out, nil
}

// Each fresh snapshot is an explicit verification phase. It may itself emit scan
// phases; transport sequence numbers preserve their return to this outer phase.
func managementSnapshot(ctx context.Context, e Engine, current string) (audit.Report, error) {
	progress := audit.NewPhaseProgress(ctx, e.Options.Progress, "verify", nil)
	progress.Begin(current)
	report, err := e.Snapshot(ctx)
	progress.Done(err != nil)
	progress.Finish()
	return report, err
}

// Only an approved impact scope observed immediately after a successful earlier
// operation can satisfy a later row; a later concurrent edit invalidates its digest.
func approvedBatchEffects(items []PlanItem, actor PlanItem, before audit.Report, known map[string]string) map[string]audit.Item {
	out := map[string]audit.Item{}
	for _, p := range items {
		if p.Status != "ready" || p.ID == actor.ID || p.Binding.ResolvedTarget == nil || p.Binding.ManagerPath == "" {
			continue
		}
		i, unique := currentInstallation(before, p)
		if !unique || i == nil || !affectedItem(*i, actor) {
			continue
		}
		digest, err := installationHash(*i, p.Binding.Scope)
		if err != nil {
			continue
		}
		if digest == p.Binding.Fingerprint && i.Version == p.FromVersion || known[p.ID] != "" && digest == known[p.ID] {
			copy := *i
			copy.Architecture = append([]string(nil), i.Architecture...)
			out[p.ID] = copy
		}
	}
	return out
}

func recordBatchSatisfaction(items []PlanItem, approved map[string]audit.Item, post audit.Report, known map[string]string) {
	for _, p := range items {
		before, approvedTarget := approved[p.ID]
		if !approvedTarget {
			continue
		}
		delete(known, p.ID)
		result := ItemResult{}
		verifyResult(&result, p, "update", post, nil)
		if result.Status != "updated" {
			continue
		}
		i, unique := currentInstallation(post, p)
		if !unique || i == nil || !reflect.DeepEqual(before.Architecture, i.Architecture) {
			continue
		}
		if digest, err := installationHash(*i, p.Binding.Scope); err == nil {
			known[p.ID] = digest
		}
	}
}

func batchTargetStillSatisfied(p, current PlanItem, report audit.Report, observed string) bool {
	if observed == "" || current.Status != "skipped" || current.Reason != "already_current" || current.FromVersion != p.TargetVersion || current.TargetVersion != p.TargetVersion {
		return false
	}
	a, b := p.Binding, current.Binding
	if p.Provider != current.Provider || a.TargetID != b.TargetID || a.PackageName != b.PackageName || a.InstallationKind != b.InstallationKind || a.Path != b.Path || a.RealPath != b.RealPath || a.Owner != b.Owner || a.Identity != b.Identity || a.Channel != b.Channel || a.Scope != b.Scope || a.ManagerPath != b.ManagerPath || a.ManagerEnvFingerprint != b.ManagerEnvFingerprint || a.ResolvedTarget == nil || b.ResolvedTarget == nil {
		return false
	}
	if a.ResolvedTarget.Source != b.ResolvedTarget.Source {
		if p.Provider != "homebrew" || a.Source != b.Source || !strings.HasPrefix(a.ResolvedTarget.Source, a.Source+":") || b.ResolvedTarget.Source != a.Source {
			return false
		}
	}
	if ownedPath(b.ManagerPath, true) != nil {
		return false
	}
	manager, err := fileHash(b.ManagerPath)
	if err != nil || manager != a.ManagerFingerprint {
		return false
	}
	i, unique := currentInstallation(report, p)
	if !unique || i == nil {
		return false
	}
	digest, err := installationHash(*i, a.Scope)
	if err != nil || digest != observed {
		return false
	}
	result := ItemResult{}
	verifyResult(&result, p, "update", report, nil)
	return result.Status == "updated"
}

func containsReportID(r audit.Report, id string) bool {
	for _, i := range r.Items {
		if i.ID == id {
			return true
		}
	}
	return false
}

func observedChanges(before, after audit.Report, selected PlanItem) []Change {
	old := map[string]audit.Item{}
	for _, i := range before.Items {
		if affectedItem(i, selected) {
			old[i.ID] = i
		}
	}
	var changes []Change
	for _, i := range after.Items {
		if !affectedItem(i, selected) {
			continue
		}
		prior, ok := old[i.ID]
		delete(old, i.ID)
		if !ok {
			changes = append(changes, Change{ID: i.ID, Name: i.Name, AfterVersion: i.Version, Status: "added"})
		} else if prior.Version != i.Version {
			changes = append(changes, Change{ID: i.ID, Name: i.Name, BeforeVersion: prior.Version, AfterVersion: i.Version, Status: "version_changed"})
		}
	}
	for _, i := range old {
		changes = append(changes, Change{ID: i.ID, Name: i.Name, BeforeVersion: i.Version, Status: "not_observed_after"})
	}
	sort.Slice(changes, func(a, b int) bool { return changes[a].ID < changes[b].ID })
	return changes
}

func affectedItem(i audit.Item, p PlanItem) bool {
	if i.ID == p.ID || i.ID == p.Binding.TargetID {
		return false
	}
	if pythonPlan(p) && i.Kind == "python-package" && i.Owner == p.Provider && normalizePython(i.Identity) == normalizePython(p.Binding.Identity) && pythonEnvironment(i) == p.Binding.Scope {
		return false
	}
	if contains(p.AffectedIDs, i.ID) {
		return true
	}
	if !filepath.IsAbs(i.Path) {
		return false
	}
	for _, root := range p.AffectedPaths {
		if filepath.IsAbs(root) && root != "/" && under(i.Path, root) {
			return true
		}
	}
	return false
}

func verifyResult(r *ItemResult, p PlanItem, action string, report audit.Report, runErr error) {
	found, uniquePython := currentInstallation(report, p)
	if found != nil {
		r.AfterVersion = found.Version
	}
	if action == "uninstall" {
		// Inventory omission alone does not prove removal after a failed query.
		removedPath := p.Binding.Path
		if pythonPlan(p) {
			removedPath = p.Binding.Scope
		}
		_, statErr := os.Lstat(removedPath)
		if uniquePython && found == nil && os.IsNotExist(statErr) && !scopeDiagnostic(report, removedPath, p) && (!pythonPlan(p) || pythonEntrypointsRemoved(p)) {
			r.Changed = true
			r.Status, r.Reason = "uninstalled", "installation_absence_verified"
			return
		}
	} else if uniquePython && found != nil && installationPathVerified(*found, p) && found.Owner == p.Binding.Owner && sourceVerified(*found, p) && identityVerified(*found, p) && found.Channel == p.Binding.Channel && found.Version == p.TargetVersion && found.Health.Status != "error" && (found.ActiveVersion == "" || found.ActiveVersion == p.TargetVersion) && (!p.Binding.WasActive || found.Active) && installationVerified(*found, p, report) && aliasVerified(p, report) {
		r.Changed = found.Version != p.FromVersion
		if r.Changed {
			r.Status, r.Reason = "updated", "target_version_verified"
			return
		}
	}
	if found != nil && found.Version != p.FromVersion {
		r.Changed = true
	}
	if runErr != nil {
		r.Status, r.Reason = "failed", "manager_command_failed"
		return
	}
	r.Status, r.Reason = "verification_unknown", "manager_succeeded_but_requested_state_unverified"
}

func currentInstallation(report audit.Report, p PlanItem) (*audit.Item, bool) {
	var found *audit.Item
	lookupID := p.Binding.TargetID
	if lookupID == "" {
		lookupID = p.ID
	}
	for n := range report.Items {
		i := &report.Items[n]
		if i.ID == lookupID {
			found = i
			break
		}
	}
	if pythonPlan(p) {
		return pythonPostInstall(report, p)
	}
	return found, true
}

func installationPathVerified(i audit.Item, p PlanItem) bool {
	if pythonPlan(p) {
		return pythonPostCandidate(i, p)
	}
	if i.Owner == "pnpm" {
		return i.Path == p.Binding.Path && pnpmPackagePath(i, p.Binding.Scope)
	}
	return i.Path == p.Binding.Path
}
func identityVerified(i audit.Item, p PlanItem) bool {
	if pythonPlan(p) {
		return normalizePython(i.Identity) == normalizePython(p.Binding.Identity)
	}
	return i.Identity == p.Binding.Identity
}

func sourceVerified(i audit.Item, p PlanItem) bool {
	if i.Source == p.Binding.Source {
		return true
	}
	// Native manager ownership and configuration were freshly bound. npm may add
	// or omit provenance metadata on upgrade; that evidence grade is not a new registry.
	return i.Kind == "node-package" && p.Binding.ResolvedTarget != nil && p.Binding.ResolvedTarget.OriginVerified && contains([]string{"npm-public", "npm-public-candidate"}, i.Source) && contains([]string{"npm-public", "npm-public-candidate"}, p.Binding.Source)
}

func aliasVerified(p PlanItem, report audit.Report) bool {
	if p.Binding.TargetID == "" || p.Binding.TargetID == p.ID {
		return true
	}
	targetID := p.Binding.TargetID
	if pythonPlan(p) {
		if item, unique := pythonPostInstall(report, p); unique && item != nil {
			targetID = item.ID
		} else {
			return false
		}
	}
	for _, i := range report.Items {
		if i.ID == p.ID && i.Kind == "mcp-server" && contains(i.Related, targetID) && i.Health.Status != "error" {
			if p.MCP == nil {
				return false
			}
			b, err := readMetadata(p.MCP.ConfigPath)
			return err == nil && hashText(string(b)) == p.MCP.SHA256
		}
	}
	return false
}

func scopeDiagnostic(r audit.Report, path string, p PlanItem) bool {
	kind := p.Binding.InstallationKind
	if kind == "" {
		kind = p.Kind
	}
	adapter := ""
	switch kind {
	case "node-package":
		adapter = "node-packages"
	case "python-package", "python-environment":
		adapter = "python-tools"
	case "formula", "cask", "tap":
		adapter = "homebrew"
	case "application":
		adapter = "apps"
	case "skill":
		adapter = "skills"
	case "mcp-server", "mcp-config":
		adapter = "mcp"
	case "executable", "runtime":
		adapter = "health"
	}
	if pythonPlan(p) {
		adapter = "python-tools"
	}
	for _, d := range r.Diagnostics {
		if d.Code == "scan_cancelled" {
			return true
		}
		if d.Path == "" {
			if adapter != "" && d.Adapter == adapter {
				return true
			}
			continue
		}
		if under(path, d.Path) || under(d.Path, path) {
			return true
		}
	}
	return false
}

func commandFor(p PlanItem, action string, o Options, dir string) (Command, error) {
	if p.Status != "ready" || p.RequiresAdmin || p.RequiresQuit || p.RequiresRestart || !filepath.IsAbs(p.Binding.ManagerPath) {
		return Command{}, errors.New("not executable")
	}
	if p.Binding.PackageName != "" {
		p.Name = p.Binding.PackageName
	}
	if p.Binding.InstallationKind != "" {
		p.Kind = p.Binding.InstallationKind
	}
	env := ManagerEnvironment(p.Provider, p.Binding, o)
	c := Command{Path: p.Binding.ManagerPath, Dir: o.Home, Env: env}
	if action != "update" && action != "uninstall" {
		return c, errors.New("invalid action")
	}
	switch p.Provider {
	case "uv-standalone":
		if action != "update" || !p.SpecialApproved || !p.RequiresScripts || p.RuntimeReview == nil || !uvVersion.MatchString(p.TargetVersion) {
			return c, errors.New("native CLI review required")
		}
		c.Args = []string{"self", "update", p.TargetVersion}
	case "homebrew":
		if !simpleName.MatchString(p.Name) || (p.Kind != "formula" && p.Kind != "cask") {
			return c, errors.New("invalid brew identity")
		}
		verb := "upgrade"
		if action == "uninstall" {
			verb = "uninstall"
		}
		c.Args = []string{verb, "--" + p.Kind, p.Name}
		if p.Kind == "cask" && action == "update" {
			c.Args = []string{verb, "--cask", "--no-quit", p.Name}
		}
	case "npm":
		if !packageName.MatchString(p.Name) {
			return c, errors.New("invalid npm identity")
		}
		verb, target := "install", p.Name+"@"+p.TargetVersion
		if action == "uninstall" {
			verb, target = "uninstall", p.Name
		}
		c.Args = []string{verb, "--global", "--prefix", p.Binding.Scope, "--ignore-scripts", "--no-audit", "--no-fund", "--", target}
		if p.RequiresScripts && p.SpecialApproved {
			c.Args = []string{verb, "--global", "--prefix", p.Binding.Scope, "--no-audit", "--no-fund", "--", target}
		}
		if action == "update" {
			c.Args = append(c.Args[:len(c.Args)-2], "--no-save", "--", target)
		}
	case "pnpm":
		if !packageName.MatchString(p.Name) {
			return c, errors.New("invalid pnpm identity")
		}
		verb, target := "add", p.Name+"@"+p.TargetVersion
		if action == "uninstall" {
			verb, target = "remove", p.Name
		}
		c.Args = []string{verb, "--global", "--config.global-dir=" + PnpmGlobalDir(p.Binding.Scope), "--ignore-pnpmfile", "--ignore-scripts", "--", target}
		if p.RequiresScripts && p.SpecialApproved {
			c.Args = []string{verb, "--global", "--config.global-dir=" + PnpmGlobalDir(p.Binding.Scope), "--ignore-pnpmfile", "--", target}
		}
		if action == "uninstall" {
			c.Args = append(c.Args[:len(c.Args)-2], PnpmDisableRuntime, "--", target)
		}
		if action == "update" {
			if p.Binding.ResolvedTarget == nil || p.Binding.ResolvedTarget.Node == nil {
				return c, errors.New("node policy requires new preview")
			}
			c.Args = append([]string{"update"}, PnpmDirectoryArgs(p.Binding, o, p.Binding.NodeVirtualStore)...)
			c.Args = append(c.Args, "--no-save", "--depth=0")
			if !p.RequiresScripts || !p.SpecialApproved {
				c.Args = append(c.Args, "--ignore-scripts")
			}
			c.Args = append(c.Args, "--", target)
		}
	case "bun":
		c.Dir = p.Binding.Scope
		if !packageName.MatchString(p.Name) {
			return c, errors.New("invalid bun identity")
		}
		verb, target := "add", p.Name+"@"+p.TargetVersion
		if action == "uninstall" {
			verb, target = "remove", p.Name
		}
		c.Args = []string{verb, "--global", "--ignore-scripts", "--", target}
		if p.RequiresScripts && p.SpecialApproved {
			c.Args = []string{verb, "--global", "--", target}
		}
		if action == "update" {
			c.Args = append(c.Args[:len(c.Args)-2], "--no-save", "--", target)
		}
	case "pipx":
		if !simpleName.MatchString(p.Name) {
			return c, errors.New("invalid python identity")
		}
		if action == "update" {
			if !filepath.IsAbs(dir) || !version.MatchString(p.TargetVersion) {
				return c, errors.New("Python constraint binding unavailable")
			}
			c.Args = []string{"upgrade", p.Name}
			c.Env = append(c.Env, "PIP_CONSTRAINT="+pythonConstraintPath(dir, p), "PIP_ONLY_BINARY=:all:")
		} else {
			c.Args = []string{"uninstall", p.Name}
		}
	case "uv":
		if !simpleName.MatchString(p.Name) {
			return c, errors.New("invalid python identity")
		}
		if action == "update" {
			if !version.MatchString(p.TargetVersion) {
				return c, errors.New("invalid Python target version")
			}
			c.Args = []string{"tool", "upgrade", p.Name + "==" + p.TargetVersion, "--no-build"}
		} else {
			c.Args = []string{"tool", "uninstall", p.Name}
		}
	default:
		return c, errors.New("unsupported provider")
	}
	return c, nil
}

// ManagerEnvironment is shared with owner-native read-only resolvers so their
// effective configuration is the one used by apply, without inherited secrets.
func ManagerEnvironment(provider string, b Binding, o Options) []string {
	o = normalizeOptions(o)
	path := []string{filepath.Dir(b.ManagerPath), filepath.Join(b.Scope, "bin")}
	if provider == "pnpm" || provider == "bun" || provider == "pipx" || provider == "uv" {
		path = append(path, managerBin(provider, b, o))
	}
	if provider == "npm" || provider == "pnpm" {
		for _, root := range o.BrewRoots {
			bin := filepath.Join(root, "bin")
			if filepath.IsAbs(root) && root != "/" && ownedPath(bin, false) == nil && !contains(path, bin) {
				path = append(path, bin)
			}
		}
	}
	path = append(path, "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	env := []string{"HOME=" + o.Home, "LC_ALL=C", "PATH=" + strings.Join(path, ":")}
	switch provider {
	case "uv-standalone":
		env = []string{"HOME=" + o.Home, "LC_ALL=C", "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "XDG_CONFIG_HOME=" + filepath.Join(o.Home, ".config"), "UV_NO_MODIFY_PATH=1", "UV_INSTALL_DIR=" + b.Scope, "UV_PYTHON_DOWNLOADS=never", "UV_NO_CONFIG=1", "UV_NO_PROGRESS=1"}
	case "npm":
		env = append(env, "npm_config_prefix="+b.Scope, "npm_config_update_notifier=false", "npm_config_fund=false", "npm_config_audit=false")
	case "pnpm":
		env = append(env, "COREPACK_ENABLE_NETWORK=0", "COREPACK_DEFAULT_TO_LATEST=0", "PNPM_HOME="+managerBin(provider, b, o))
	case "homebrew":
		env = append(env, "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_INSTALL_CLEANUP=1", "HOMEBREW_NO_ANALYTICS=1", "HOMEBREW_NO_ENV_HINTS=1", "HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK=1", "HOMEBREW_NO_AUTOREMOVE=1")
	case "pipx":
		env = append(env, "PIPX_HOME="+filepath.Dir(filepath.Dir(b.Scope)), "PIPX_BIN_DIR="+managerBin(provider, b, o), "PIP_NO_INPUT=1", "PIP_DISABLE_PIP_VERSION_CHECK=1")
	case "uv":
		env = append(env, "UV_TOOL_DIR="+filepath.Dir(b.Scope), "UV_TOOL_BIN_DIR="+managerBin(provider, b, o), "UV_PYTHON_DOWNLOADS=never", "UV_NO_PROGRESS=1")
	case "bun":
		env = append(env, "BUN_INSTALL="+filepath.Join(o.Home, ".bun"), "BUN_INSTALL_GLOBAL_DIR="+b.Scope, "BUN_INSTALL_BIN="+managerBin(provider, b, o))
	}
	return env
}

func lockState(dir string) (func(), error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, errors.New("invalid state directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("state directory unavailable")
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("state directory must be private and not a symbolic link")
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Getuid()) {
		return nil, errors.New("state directory owner mismatch")
	}
	path := filepath.Join(dir, "apply.lock")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("action lock unavailable")
	}
	f := os.NewFile(uintptr(fd), path)
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("action lock is not a private regular file")
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another action is in progress")
	}
	return func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = f.Close() }, nil
}

// DecodePlan rejects command/environment extensions and ambiguous trailing JSON.
func DecodePlan(r io.Reader) (Plan, error) {
	var p Plan
	d := json.NewDecoder(io.LimitReader(r, 16<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, fmt.Errorf("invalid plan: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return p, errors.New("plan has trailing content")
	}
	return p, nil
}
