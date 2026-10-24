package manage

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
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
)

//go:embed lifecycle.js
var lifecycleScript string

// ApplicationInstance binds a quit request to one launch, not an app name or PID alone.
type ApplicationInstance struct {
	PID        int    `json:"pid"`
	LaunchedAt string `json:"launched_at"`
	Executable string `json:"executable"`
	Path       string `json:"path"`
	Identity   string `json:"identity"`
}

type ApplicationLifecycleTarget struct {
	ApplicationBinding
	RealPath    string                `json:"real_path"`
	Fingerprint string                `json:"fingerprint"`
	Instances   []ApplicationInstance `json:"instances"`
	Reopen      bool                  `json:"reopen"`
}

type ApplicationLifecycleResult struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	QuitStatus   string `json:"quit_status"`
	QuitReason   string `json:"quit_reason"`
	ReopenStatus string `json:"reopen_status"`
	ReopenReason string `json:"reopen_reason"`
}

// ValidateLifecycleSelection keeps exit/reopen consent separate from scripts and batch updates.
func ValidateLifecycleSelection(s Selection) error {
	if len(s.QuitApps) == 0 && len(s.ReopenApps) == 0 {
		return nil
	}
	if s.Action != "update" || s.All || len(s.IDs) == 0 {
		return errors.New("application exit requires an update preview with explicit installation IDs")
	}
	for _, ids := range [][]string{s.QuitApps, s.ReopenApps} {
		seen := map[string]bool{}
		for _, id := range ids {
			if id == "" || seen[id] {
				return errors.New("empty or duplicate lifecycle application ID")
			}
			seen[id] = true
		}
	}
	for _, id := range s.ReopenApps {
		if !contains(s.QuitApps, id) {
			return errors.New("reopen-app must also be explicitly selected with quit-app")
		}
	}
	return nil
}

func sortedIDs(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

func validateLifecycleScope(report audit.Report, s Selection) error {
	for _, id := range s.QuitApps {
		found := false
		for _, app := range report.Items {
			if app.ID != id || app.Kind != "application" {
				continue
			}
			if app.Owner == "standalone" && app.Source == "sparkle" && contains(s.IDs, app.ID) {
				found = true
				break
			}
			if app.Owner != "homebrew" {
				continue
			}
			for _, item := range report.Items {
				if item.Kind == "cask" && contains(s.IDs, item.ID) && contains(app.Related, item.ID) {
					found = true
				}
			}
		}
		if !found {
			return errors.New("quit-app must identify an explicitly selected supported application or its cask")
		}
	}
	return nil
}

// ResolveApplicationLifecycle is read-only and can be reused by other exact app providers.
func ResolveApplicationLifecycle(ctx context.Context, app audit.Item, options Options, runner Runner) (ApplicationLifecycleTarget, error) {
	target := ApplicationLifecycleTarget{ApplicationBinding: ApplicationBinding{ID: app.ID, Path: app.Path, Identity: app.Identity, Version: app.Version}}
	o := normalizeOptions(options)
	allowed := false
	for _, root := range o.AppRoots {
		if filepath.IsAbs(root) && root != "/" && under(app.Path, root) && app.Path != root {
			allowed = true
		}
	}
	if app.Kind != "application" || app.Owner == "system" || app.Owner == "app-component" || !allowed || app.Identity == "" || app.Version == "" {
		return target, errors.New("application_lifecycle_scope_unverified")
	}
	for _, evidence := range app.Evidence {
		if strings.Contains(evidence, "dirty") || strings.Contains(evidence, "local modification") {
			return target, errors.New("local_modifications_present")
		}
	}
	var err error
	target.RealPath, err = filepath.EvalSymlinks(app.Path)
	if err != nil || target.RealPath != app.Path || app.RealPath != "" && app.RealPath != target.RealPath {
		return target, errors.New("application_path_changed")
	}
	if err := ownedPath(app.Path, false); err != nil {
		return target, errors.New("application_requires_owner_permission")
	}
	target.Fingerprint, err = applicationFingerprint(ctx, target.Path)
	if err != nil {
		return target, err
	}
	target.Instances, err = inspectApplication(ctx, runner, target.Path)
	if err != nil {
		return target, err
	}
	if len(target.Instances) == 0 {
		return target, errors.New("application_not_running_create_new_preview")
	}
	for _, instance := range target.Instances {
		if instance.PID <= 0 || instance.LaunchedAt == "" || instance.Path != target.RealPath || instance.Identity != target.Identity || !under(instance.Executable, filepath.Join(target.RealPath, "Contents")) {
			return target, errors.New("application_instance_unverified")
		}
	}
	return target, nil
}

func applicationFingerprint(ctx context.Context, root string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Ext(root) != ".app" || strings.Contains(root, ".app/") {
		return "", errors.New("application_path_unverified")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return "", errors.New("application_path_changed")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("application_path_changed")
	}
	h := sha256.New()
	count, total := 0, int64(0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		count++
		st, err := entry.Info()
		if err != nil {
			return err
		}
		total += st.Size()
		if count > 100000 || total > 8<<30 {
			return errors.New("application_fingerprint_limit")
		}
		stat, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("application_stat_unavailable")
		}
		rel, _ := filepath.Rel(root, path)
		fmt.Fprintf(h, "%s\x00%s\x00%d:%d:%d:%d\x00%s\x00", rel, st.Mode(), stat.Dev, stat.Ino, stat.Uid, st.Size(), st.ModTime().UTC())
		switch {
		case st.Mode().IsRegular():
			digest, err := fileHash(path)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(h, digest)
		case st.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			dest, err := filepath.EvalSymlinks(path)
			if err != nil || !under(dest, root) {
				return errors.New("application_external_link_unverified")
			}
			_, _ = io.WriteString(h, link)
		case !st.IsDir():
			return errors.New("application_file_type_unverified")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func lifecycleCommand(ctx context.Context, runner Runner, input any, output any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	query, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	data, err := runner.Run(query, Command{Path: "/usr/bin/osascript", Args: []string{"-l", "JavaScript", "-e", lifecycleScript, string(payload)}, Env: []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}})
	if err != nil || json.Unmarshal(data, output) != nil {
		return errors.New("application_lifecycle_query_failed")
	}
	return nil
}

func inspectApplication(ctx context.Context, runner Runner, path string) ([]ApplicationInstance, error) {
	var response struct {
		Instances []ApplicationInstance `json:"instances"`
	}
	err := lifecycleCommand(ctx, runner, map[string]any{"operation": "inspect", "path": path}, &response)
	if err != nil {
		return nil, err
	}
	// A missing/null array is not evidence of absence.
	if response.Instances == nil {
		return nil, errors.New("application_process_state_unknown")
	}
	sort.Slice(response.Instances, func(i, j int) bool { return response.Instances[i].PID < response.Instances[j].PID })
	return response.Instances, nil
}

func applicationProcessesClosed(ctx context.Context, runner Runner, path string) bool {
	if ctx.Err() != nil {
		return false
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	query, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	data, err := runner.Run(query, Command{Path: "/bin/ps", Args: []string{"-axo", "comm="}, Env: []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}})
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		executable := strings.TrimSpace(line)
		if executable == path || strings.HasPrefix(executable, path+"/") {
			return false
		}
	}
	return true
}

// QuitApplications requests normal exit of only the approved instances, then proves absence.
// An app may refuse or show unsaved-work UI; neither condition permits force termination.
func QuitApplications(ctx context.Context, targets []ApplicationLifecycleTarget, runner Runner, timeout time.Duration) ([]ApplicationLifecycleResult, error) {
	if timeout <= 0 || timeout > 2*time.Minute {
		timeout = 30 * time.Second
	}
	results := make([]ApplicationLifecycleResult, len(targets))
	for n, target := range targets {
		results[n] = ApplicationLifecycleResult{ID: target.ID, Path: target.Path, QuitStatus: "not_requested", QuitReason: "not_reached", ReopenStatus: "not_requested", ReopenReason: "not_selected"}
		if target.Reopen {
			results[n].ReopenStatus, results[n].ReopenReason = "skipped", "update_not_verified"
		}
	}
	for n, target := range targets {
		reason := quitApplication(ctx, target, runner, timeout, &results[n])
		if reason != "" {
			return results, errors.New(reason)
		}
	}
	return results, nil
}

func quitApplication(ctx context.Context, target ApplicationLifecycleTarget, runner Runner, timeout time.Duration, result *ApplicationLifecycleResult) string {
	fail := func(reason string) string { result.QuitReason = reason; return reason }
	if ctx.Err() != nil {
		return fail("application_quit_cancelled")
	}
	digest, err := applicationFingerprint(ctx, target.Path)
	if err != nil || digest != target.Fingerprint || target.RealPath != target.Path {
		return fail("application_path_or_content_changed")
	}
	live, err := inspectApplication(ctx, runner, target.Path)
	if err != nil || len(target.Instances) == 0 || !reflect.DeepEqual(live, target.Instances) {
		return fail("application_instance_changed")
	}
	for _, instance := range target.Instances {
		if instance.Path != target.Path || instance.Identity != target.Identity {
			return fail("application_instance_changed")
		}
		if ctx.Err() != nil {
			return fail("application_quit_cancelled")
		}
		var response struct {
			Status string `json:"status"`
		}
		err := lifecycleCommand(ctx, runner, map[string]any{"operation": "quit", "path": target.Path, "identity": target.Identity, "instance": instance}, &response)
		if err != nil {
			result.QuitStatus = "verification_unknown"
			return fail("application_quit_request_unverified")
		}
		if response.Status != "requested" {
			result.QuitStatus = "refused"
			if response.Status == "instance_changed" {
				return fail("application_instance_changed")
			}
			return fail("application_quit_not_accepted")
		}
		result.QuitStatus, result.QuitReason = "requested", "normal_exit_requested"
	}
	wait, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if ctx.Err() != nil {
			return fail("application_quit_cancelled")
		}
		live, err := inspectApplication(wait, runner, target.Path)
		if err != nil {
			if ctx.Err() != nil {
				return fail("application_quit_cancelled")
			}
			if wait.Err() != nil {
				return fail("application_quit_timed_out_or_unsaved_work")
			}
			return fail("application_exit_unverified")
		}
		for _, instance := range live {
			found := false
			for _, approved := range target.Instances {
				if instance == approved {
					found = true
				}
			}
			if !found {
				return fail("application_restarted_after_preview")
			}
		}
		if len(live) == 0 && applicationProcessesClosed(wait, runner, target.Path) {
			result.QuitStatus, result.QuitReason = "exited", "application_exit_verified"
			return ""
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-wait.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return fail("application_quit_cancelled")
			}
			return fail("application_quit_timed_out_or_unsaved_work")
		case <-timer.C:
		}
	}
}

// VerifyApplicationsClosed is a final guard; it never retries termination or chases a new process.
func VerifyApplicationsClosed(ctx context.Context, targets []ApplicationLifecycleTarget, runner Runner) error {
	for _, target := range targets {
		digest, err := applicationFingerprint(ctx, target.Path)
		if err != nil || digest != target.Fingerprint {
			return errors.New("application_path_or_content_changed")
		}
		live, err := inspectApplication(ctx, runner, target.Path)
		if err != nil || len(live) > 0 || !applicationProcessesClosed(ctx, runner, target.Path) {
			return errors.New("application_restarted_or_exit_unverified")
		}
	}
	return nil
}

func lifecycleItemSelection(selection Selection, item PlanItem) Selection {
	out := selection
	out.IDs, out.All = []string{item.ID}, false
	out.QuitApps, out.ReopenApps = nil, nil
	for _, target := range item.ApplicationLifecycle {
		out.QuitApps = append(out.QuitApps, target.ID)
		if target.Reopen {
			out.ReopenApps = append(out.ReopenApps, target.ID)
		}
	}
	return out
}

// Revalidate after the approved activity change, without masking any other plan difference.
func revalidateAfterQuit(ctx context.Context, original PlanItem, selection Selection, e Engine, o Options) (PlanItem, error) {
	current, err := e.Snapshot(ctx)
	if err != nil {
		return PlanItem{}, errors.New("post_quit_inventory_unavailable")
	}
	selection.QuitApps, selection.ReopenApps = nil, nil
	o.ResolvedTargets, err = e.ResolveTargets(ctx, current, selection)
	if err != nil {
		return PlanItem{}, errors.New("post_quit_candidate_unavailable")
	}
	rebuilt, err := NewPlan(current, selection, o)
	expected := original
	expected.RequiresQuit, expected.ApplicationLifecycle = false, nil
	if expected.Provider == "native-app" && expected.Binding.ResolvedTarget != nil && expected.Binding.ResolvedTarget.ApplicationUpdate != nil {
		// The candidate stays identical; only its independent running proof becomes closed.
		target := *expected.Binding.ResolvedTarget
		if target.ApplicationState != "application_instances_verified" || target.ApplicationLifecycle == nil {
			return PlanItem{}, errors.New("application_lifecycle_scope_unverified")
		}
		target.ApplicationState, target.ApplicationLifecycle = "closed_application_verified", nil
		expected.Binding.ResolvedTarget = &target
	}
	if err != nil || len(rebuilt.Items) != 1 || !reflect.DeepEqual(expected, rebuilt.Items[0]) {
		return PlanItem{}, errors.New("installation_changed_after_application_exit")
	}
	return rebuilt.Items[0], VerifyApplicationsClosed(ctx, original.ApplicationLifecycle, e.Runner)
}

func reopenApplications(ctx context.Context, item PlanItem, post audit.Report, runner Runner, results []ApplicationLifecycleResult) {
	for n, target := range item.ApplicationLifecycle {
		if !target.Reopen || n >= len(results) || results[n].QuitStatus != "exited" {
			continue
		}
		results[n].ReopenStatus, results[n].ReopenReason = "failed", "updated_application_identity_unverified"
		for _, installed := range post.Items {
			ownerMatches := installed.Owner == "homebrew" && contains(installed.Related, item.Binding.TargetID)
			if item.Provider == "native-app" {
				ownerMatches = installed.Owner == item.Binding.Owner && installed.ID == item.Binding.TargetID && sourceVerified(installed, item) && identityVerified(installed, item)
			}
			if installed.ID == target.ID && ownerMatches {
				results[n].ReopenStatus, results[n].ReopenReason = ReopenApplication(ctx, target, installed, runner)
				break
			}
		}
	}
}

// ReopenApplication launches only the approved path and verifies its running identity.
// Call only after the provider verifies its update and QuitApplications reports exited.
func ReopenApplication(ctx context.Context, target ApplicationLifecycleTarget, installed audit.Item, runner Runner) (status, reason string) {
	if !target.Reopen {
		return "not_requested", "not_selected"
	}
	if ctx.Err() != nil {
		return "skipped", "application_reopen_cancelled"
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	if installed.Kind != "application" || installed.ID != target.ID || installed.Path != target.Path || installed.Identity != target.Identity || installed.Health.Status != "ok" || installed.Version == "" || target.RealPath != target.Path {
		return "failed", "updated_application_identity_unverified"
	}
	digest, err := applicationFingerprint(ctx, target.Path)
	if err != nil {
		return "failed", "updated_application_identity_unverified"
	}
	live, err := inspectApplication(ctx, runner, target.Path)
	if err != nil || len(live) != 0 || !applicationProcessesClosed(ctx, runner, target.Path) {
		return "failed", "application_already_running_or_state_unknown"
	}
	// Read the actual bundle again rather than trust Launch Services' name/ID cache.
	var metadata struct {
		Identity string `json:"CFBundleIdentifier"`
		Version  string `json:"CFBundleShortVersionString"`
		Build    string `json:"CFBundleVersion"`
	}
	query, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	data, err := runner.Run(query, Command{Path: "/usr/bin/plutil", Args: []string{"-convert", "json", "-o", "-", filepath.Join(target.Path, "Contents/Info.plist")}, Env: []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}})
	if err != nil || json.Unmarshal(data, &metadata) != nil {
		return "failed", "updated_application_identity_unverified"
	}
	if metadata.Version == "" {
		metadata.Version = metadata.Build
	}
	if metadata.Identity != target.Identity || metadata.Version != installed.Version || installed.Build != "" && metadata.Build != installed.Build {
		return "failed", "updated_application_identity_unverified"
	}
	again, err := applicationFingerprint(ctx, target.Path)
	if err != nil || again != digest {
		return "failed", "application_changed_before_reopen"
	}
	if ctx.Err() != nil {
		return "skipped", "application_reopen_cancelled"
	}
	// -n prevents Launch Services from activating a different copy with the same bundle ID.
	_, err = runner.Run(query, Command{Path: "/usr/bin/open", Args: []string{"-n", "-a", target.Path}, Env: []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}})
	if err != nil {
		return "failed", "application_reopen_command_failed"
	}
	live, err = inspectApplication(ctx, runner, target.Path)
	if err == nil && len(live) > 0 {
		valid := true
		for _, instance := range live {
			valid = valid && instance.Path == target.Path && instance.Identity == target.Identity && instance.PID > 0 && instance.LaunchedAt != "" && under(instance.Executable, filepath.Join(target.Path, "Contents"))
		}
		if valid {
			return "reopened", "application_reopen_verified"
		}
	}
	return "verification_unknown", "application_reopen_unverified"
}

// MergeApplicationTarget retains an update candidate while adding independent lifecycle evidence.
// Call with the candidate as base and ResolveApplicationTargets' value as proof.
func MergeApplicationTarget(base, proof ResolvedTarget) ResolvedTarget {
	if proof.ResolutionError != "" {
		reason := ResolutionFailureReason(proof.ResolutionError)
		return ResolvedTarget{Provider: proof.Provider, Scope: proof.Scope, Reason: reason, ResolutionError: reason}
	}
	if base.ResolutionError != "" {
		reason := ResolutionFailureReason(base.ResolutionError)
		return ResolvedTarget{Provider: base.Provider, Scope: base.Scope, Reason: reason, ResolutionError: reason}
	}
	if base.Provider == "" {
		return proof
	}
	if base.Scope != proof.Scope {
		base.ApplicationLifecycle, base.ApplicationState = nil, ""
		return base
	}
	base.ApplicationLifecycle = proof.ApplicationLifecycle
	base.ApplicationState = proof.ApplicationState
	return base
}

func applicationClosed(target ResolvedTarget) bool {
	return target.ApplicationState == "closed_application_verified" || target.ApplicationState == "" && target.Reason == "closed_application_verified"
}
