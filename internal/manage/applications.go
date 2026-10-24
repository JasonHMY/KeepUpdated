package manage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func ResolveApplicationTargets(ctx context.Context, r audit.Report, s Selection, options Options, runner Runner) (map[string]ResolvedTarget, error) {
	options = normalizeOptions(options)
	out := map[string]ResolvedTarget{}
	if s.Action != "uninstall" && s.Action != "update" {
		return out, nil
	}
	selected := []audit.Item{}
	for _, i := range r.Items {
		selectedID := s.All || contains(s.IDs, i.ID)
		for _, id := range i.Related {
			selectedID = selectedID || contains(s.IDs, id)
		}
		if i.Kind == "application" && selectedID && contains([]string{"user", "standalone", "homebrew", "app-store"}, i.Owner) && (s.Action == "uninstall" || i.Owner == "homebrew" || i.Owner == "standalone" && i.Source == "sparkle") {
			selected = append(selected, i)
		}
	}
	if len(selected) == 0 {
		return out, nil
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	b, err := runner.Run(ctx, Command{Path: "/bin/ps", Args: []string{"-axo", "comm="}, Env: []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}})
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if err != nil {
		for _, i := range selected {
			out[i.ID] = queryFailureTarget("native-app", i.Path, err)
		}
		return out, nil
	}
	for _, i := range selected {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		resolutionCurrent(options, i.Name)
		if contains(s.QuitApps, i.ID) {
			base := options.ResolvedTargets[i.ID]
			if s.Action == "update" && i.Owner == "standalone" {
				base = resolveApplicationUpdate(ctx, i, r, options, runner)
			}
			if err := ctx.Err(); err != nil {
				return out, err
			}
			lifecycle, e := ResolveApplicationLifecycle(ctx, i, options, runner)
			if err := ctx.Err(); err != nil {
				return out, err
			}
			if e == nil {
				proof := ResolvedTarget{Provider: "native-app", Scope: i.Path, Source: lifecycle.Fingerprint, OriginVerified: true, Reason: "application_instances_verified", ApplicationState: "application_instances_verified", ApplicationLifecycle: &lifecycle}
				out[i.ID] = MergeApplicationTarget(base, proof)
			} else if e.Error() == "application_requires_owner_permission" {
				out[i.ID] = MergeApplicationTarget(base, ResolvedTarget{Provider: "native-app", Scope: i.Path, ApplicationState: "application_requires_owner_permission"})
			}
			continue
		}
		rp, e := filepath.EvalSymlinks(i.Path)
		if e != nil {
			continue
		}
		closed := true
		for _, line := range strings.Split(string(b), "\n") {
			path := strings.TrimSpace(line)
			if path == rp || strings.HasPrefix(path, rp+"/") {
				closed = false
				break
			}
		}
		if closed {
			proof := ResolvedTarget{Provider: "native-app", Scope: i.Path, Source: hashText(rp + "\x00" + i.Identity), OriginVerified: true, Reason: "closed_application_verified", ApplicationState: "closed_application_verified"}
			base := options.ResolvedTargets[i.ID]
			if s.Action == "update" && i.Owner == "standalone" {
				base = resolveApplicationUpdate(ctx, i, r, options, runner)
			}
			out[i.ID] = MergeApplicationTarget(base, proof)
		}
	}
	return out, ctx.Err()
}

func applicationPlan(i audit.Item, p PlanItem, report audit.Report, o Options, s Selection) PlanItem {
	if s.Action == "update" && i.Owner == "app-store" {
		return appStorePlan(i, p, report, o, s)
	}
	p.Status, p.Reason = "manual", "application_update_requires_vendor_adapter"
	if s.Action != "uninstall" && s.Action != "update" {
		return p
	}
	if s.Action == "update" && (i.Owner != "standalone" || i.Source != "sparkle") {
		return p
	}
	p.Status, p.Reason = "blocked", "application_path_not_user_manageable"
	if i.Owner == "app-component" || i.Owner == "system" || !filepath.IsAbs(i.Path) || filepath.Clean(i.Path) != i.Path || filepath.Ext(i.Path) != ".app" || strings.Contains(i.Path, ".app/") || strings.Contains(i.Path, "/Contents/") {
		return p
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]+\.[A-Za-z0-9.-]+$`).MatchString(i.Identity) {
		p.Reason = "application_bundle_identity_unverified"
		return p
	}
	allowed := false
	for _, root := range o.AppRoots {
		if root != "/" && filepath.IsAbs(root) && under(i.Path, root) && i.Path != root {
			allowed = true
		}
	}
	if !allowed {
		return p
	}
	st, err := os.Lstat(i.Path)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		p.Reason = "application_alias_or_path_unavailable"
		return p
	}
	rp, err := filepath.EvalSymlinks(i.Path)
	if err != nil {
		p.Reason = "application_path_unavailable"
		return p
	}
	if i.RealPath != "" && rp != i.RealPath {
		p.Reason = "application_path_changed"
		return p
	}
	if err = ownedPath(i.Path, false); err != nil {
		p.Status, p.Reason, p.RequiresAdmin = "manual", "application_requires_owner_permission", true
		return p
	}
	p.Applications = []ApplicationBinding{{ID: i.ID, Path: i.Path, Identity: i.Identity, Version: i.Version}}
	t, ok := o.ResolvedTargets[i.ID]
	if ok && t.ResolutionError != "" {
		p.Status, p.Reason, p.TargetVersion = "manual", ResolutionFailureReason(t.ResolutionError), ""
		return p
	}
	quitApproved := s.Action == "update" && contains(s.QuitApps, i.ID) && ok && t.OriginVerified && t.Provider == "native-app" && t.Scope == i.Path && t.ApplicationState == "application_instances_verified" && t.ApplicationLifecycle != nil
	if !i.ActivityKnown || i.Active && !quitApproved {
		p.Status, p.Reason, p.RequiresQuit = "manual", "close_application_then_create_new_preview", true
		return p
	}
	if !ok || !t.OriginVerified || t.Provider != "native-app" || t.Scope != i.Path || !applicationClosed(t) && !quitApproved || s.Action == "update" && t.ApplicationUpdate == nil {
		p.Status, p.Reason, p.RequiresQuit = "manual", "close_application_then_verify_all_bundle_processes", true
		if s.Action == "update" && ok && t.Reason != "" {
			p.Reason = t.Reason
			p.RequiresQuit = false
		}
		return p
	}
	digest, err := fileHash(filepath.Join(i.Path, "Contents/Info.plist"))
	if err != nil {
		p.Reason = "application_metadata_unreadable"
		return p
	}
	p.Provider = "native-app"
	p.TargetVersion = ""
	p.Binding.RealPath, p.Binding.Scope = rp, i.Path
	p.Binding.ResolvedTarget = &t
	p.Binding.Fingerprint = hashText(i.Path + "\x00" + rp + "\x00" + i.Identity + "\x00" + i.Version + "\x00" + digest + "\x00" + st.ModTime().String())
	p.AffectedPaths = []string{i.Path}
	if s.Cleanup {
		paths, reason := applicationCleanupPaths(i, report, o)
		if reason != "" {
			p.Reason = reason
			return p
		}
		digest, err := cleanupHash(paths)
		if err != nil {
			p.Reason = "cleanup_fingerprint_failed"
			return p
		}
		p.CleanupPaths = paths
		p.AffectedPaths = append(p.AffectedPaths, paths...)
		p.Binding.Fingerprint += "/" + digest
	}
	if s.Action == "update" {
		if i.Owner != "standalone" || i.Source != "sparkle" || t.ApplicationUpdate == nil || t.ApplicationUpdate.BundleID != i.Identity || t.FromVersion != i.Version || !applicationParentWritable(i.Path) {
			p.Status, p.Reason = "manual", "application_update_binding_unverified"
			return p
		}
		for _, e := range i.Evidence {
			if strings.Contains(e, "dirty") || strings.Contains(e, "local modification") || strings.Contains(e, "pinned") {
				p.Status, p.Reason = "blocked", "application_local_changes_or_pin"
				return p
			}
		}
		ctx := o.Context
		if ctx == nil {
			ctx = context.Background()
		}
		digest, err := applicationTreeHash(ctx, i.Path)
		if err != nil || digest != t.ApplicationUpdate.InstalledSHA256 {
			p.Status, p.Reason = "blocked", "application_content_changed"
			return p
		}
		p.Binding.Fingerprint = digest
		p.TargetVersion = t.Version
		if quitApproved {
			lifecycle := *t.ApplicationLifecycle
			if lifecycle.ID != i.ID || lifecycle.Path != i.Path || lifecycle.Identity != i.Identity || lifecycle.Version != i.Version || lifecycle.RealPath != rp {
				p.Status, p.Reason = "blocked", "application_lifecycle_scope_unverified"
				return p
			}
			lifecycle.Reopen = contains(s.ReopenApps, i.ID)
			p.ApplicationLifecycle = []ApplicationLifecycleTarget{lifecycle}
			p.RequiresQuit = true
		}
		p.Status, p.Reason = "ready", "signed_application_zip_ready"
		if t.ApplicationUpdate.Archive.Format == "dmg" {
			p.Reason = "signed_application_dmg_ready"
		}
		return p
	}
	p.Status, p.Reason = "ready", "closed_user_application_to_trash"
	return p
}

func moveApplication(path, home, id string, checkpoints ...func(string) error) error {
	st, err := os.Lstat(path)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("application path changed")
	}
	if err = ownedPath(path, false); err != nil {
		return err
	}
	trash := filepath.Join(home, ".Trash")
	if err = os.MkdirAll(trash, 0700); err != nil {
		return err
	}
	st, err = os.Lstat(trash)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("trash unavailable")
	}
	if err = ownedPath(trash, false); err != nil {
		return errors.New("trash not owned")
	}
	container, err := os.MkdirTemp(trash, "KeepUpdated-"+id[:12]+"-")
	if err != nil {
		return err
	}
	destination := filepath.Join(container, filepath.Base(path))
	for _, checkpoint := range checkpoints {
		if err := checkpoint(destination); err != nil {
			return err
		}
	}
	return os.Rename(path, destination)
}
