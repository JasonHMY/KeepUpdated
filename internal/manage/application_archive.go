package manage

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

func extractApplicationZIP(ctx context.Context, data []byte, dest string) (string, error) {
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil {
		return "", errors.New("application_zip_invalid")
	}
	if len(z.File) > 100000 {
		return "", errors.New("application_zip_limit")
	}
	root := ""
	seen := map[string]bool{}
	var total uint64
	links := map[string]string{}
	for _, f := range z.File {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		name := strings.TrimSuffix(f.Name, "/")
		if name == "" || strings.ContainsAny(name, "\\\x00:") || strings.HasPrefix(name, "/") || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return "", errors.New("application_zip_unsafe_path")
		}
		key := strings.ToLower(name)
		if seen[key] {
			return "", errors.New("application_zip_duplicate_path")
		}
		seen[key] = true
		if name == "__MACOSX" || strings.HasPrefix(name, "__MACOSX/") || name == ".DS_Store" {
			continue
		}
		top := strings.Split(name, "/")[0]
		if !strings.HasSuffix(top, ".app") || root != "" && root != top {
			return "", errors.New("application_zip_single_bundle_required")
		}
		root = top
		if f.UncompressedSize64 > 2<<30 || total+f.UncompressedSize64 > 4<<30 {
			return "", errors.New("application_zip_limit")
		}
		total += f.UncompressedSize64
		target := filepath.Join(dest, filepath.FromSlash(name))
		if !under(target, dest) {
			return "", errors.New("application_zip_unsafe_path")
		}
		mode := f.Mode()
		if mode.IsDir() {
			if e = os.MkdirAll(target, 0755); e != nil {
				return "", e
			}
			continue
		}
		if !mode.IsRegular() && mode&os.ModeSymlink == 0 {
			return "", errors.New("application_zip_special_file")
		}
		r, e := f.Open()
		if e != nil {
			return "", errors.New("application_zip_invalid")
		}
		if mode&os.ModeSymlink != 0 {
			b, readErr := io.ReadAll(io.LimitReader(r, 4097))
			r.Close()
			link := string(b)
			if readErr != nil || len(b) > 4096 || len(b) == 0 || filepath.IsAbs(link) || strings.ContainsAny(link, "\\\x00:") || !under(filepath.Clean(filepath.Join(filepath.Dir(target), link)), filepath.Join(dest, root)) {
				return "", errors.New("application_zip_escaping_symlink")
			}
			links[target] = link
			continue
		}
		if e = os.MkdirAll(filepath.Dir(target), 0755); e != nil {
			r.Close()
			return "", e
		}
		out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm()&0755|0600)
		if e != nil {
			r.Close()
			return "", e
		}
		n, copyErr := io.Copy(out, io.LimitReader(r, int64(f.UncompressedSize64)+1))
		r.Close()
		syncErr := out.Sync()
		closeErr := out.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil || uint64(n) != f.UncompressedSize64 {
			return "", errors.New("application_zip_content_invalid")
		}
	}
	if root == "" {
		return "", errors.New("application_zip_single_bundle_required")
	}
	// A link entry must never be created through another archive link. Without
	// this check, mkdir of a link's parent could follow an escaping chain before
	// the final containment walk has a chance to reject it.
	linkNames := map[string]bool{}
	for name := range links {
		linkNames[strings.ToLower(name)] = true
	}
	for name := range links {
		for parent := filepath.Dir(name); parent != dest && under(parent, dest); parent = filepath.Dir(parent) {
			if linkNames[strings.ToLower(parent)] {
				return "", errors.New("application_zip_symlink_conflict")
			}
		}
	}
	for name, target := range links {
		if e = os.MkdirAll(filepath.Dir(name), 0755); e != nil {
			return "", e
		}
		if e = os.Symlink(target, name); e != nil {
			return "", errors.New("application_zip_symlink_conflict")
		}
	}
	app := filepath.Join(dest, root)
	st, e := os.Lstat(app)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("application_zip_single_bundle_required")
	}
	// Resolve link chains after all links exist; lexical checks alone miss chains.
	if _, e = applicationTreeHash(ctx, app); e != nil {
		return "", errors.New("application_zip_escaping_symlink")
	}
	return app, nil
}

func applyApplicationUpdate(ctx context.Context, p PlanItem, o Options, runner Runner) (saved string, changed bool, failure error) {
	if p.Binding.ResolvedTarget == nil || p.Binding.ResolvedTarget.ApplicationUpdate == nil {
		return "", false, errors.New("application_update_binding_missing")
	}
	preview := *p.Binding.ResolvedTarget.ApplicationUpdate
	data, e := audit.DownloadApplicationArchive(ctx, preview.Archive, o.ApplicationHTTPClient)
	if e != nil {
		return "", false, e
	}
	target := p.Binding.Scope
	real, e := filepath.EvalSymlinks(target)
	if e != nil || real != p.Binding.RealPath || ownedPath(target, false) != nil || !applicationParentWritable(target) {
		return "", false, errors.New("application_target_changed")
	}
	container, e := os.MkdirTemp(filepath.Dir(real), ".KeepUpdated-native-")
	if e != nil {
		return "", false, errors.New("application_staging_unavailable")
	}
	backup := filepath.Join(container, "original.app")
	defer func() {
		if saved == "" {
			_ = os.RemoveAll(container)
		}
	}()
	stage := filepath.Join(container, "stage")
	if e = os.Mkdir(stage, 0700); e != nil {
		return "", false, e
	}
	app, retained, e := stageApplicationArchive(ctx, runner, data, stage, preview)
	if retained {
		saved = container
	}
	data = nil
	if e != nil {
		return saved, false, e
	}
	if e = verifyApplicationPayload(ctx, runner, app, preview); e != nil {
		return "", false, e
	}
	stageDigest, e := applicationTreeHash(ctx, app)
	if e != nil {
		return "", false, e
	}
	// Download and validation may take minutes: recheck the original immediately
	// before replacement, including content, source preferences and live processes.
	digest, e := applicationTreeHash(ctx, real)
	if e != nil || digest != preview.InstalledSHA256 {
		return "", false, errors.New("application_changed_during_download")
	}
	info, e := readApplicationInfo(ctx, runner, real)
	if e != nil || !applicationPreferencesUnchanged(ctx, runner, o.Home, info) {
		return "", false, errors.New("application_update_preferences_changed")
	}
	installed := audit.Item{Version: info.Version, Build: info.Build}
	freshArchive, err := audit.ResolveApplicationArchive(ctx, installed, info.Feed, info.Key, preview.MacOS, preview.Architecture, o.ApplicationHTTPClient)
	if err != nil || freshArchive != preview.Archive {
		return "", false, errors.New("application_source_changed_during_download")
	}
	currentReal, err := filepath.EvalSymlinks(target)
	st, statErr := os.Lstat(target)
	if err != nil || currentReal != real || statErr != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || ownedPath(target, false) != nil || !applicationParentWritable(target) {
		return "", false, errors.New("application_target_changed")
	}
	if !nativeApplicationClosed(ctx, runner, real) {
		return "", false, errors.New("application_running_before_replace")
	}
	if ctx.Err() != nil {
		return "", false, ctx.Err()
	}
	// Store both content identities before the atomic exchange. After process
	// interruption either path can be identified without guessing its phase.
	recovery := []byte("Target: " + real + "\nExchange partner: " + backup + "\nOriginal SHA256: " + preview.InstalledSHA256 + "\nCandidate SHA256: " + stageDigest + "\nThe target path remains populated across an atomic exchange. Identify contents by hash before recovery.\n")
	f, e := os.OpenFile(filepath.Join(container, "RECOVERY.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", false, e
	}
	_, we := f.Write(recovery)
	se := f.Sync()
	ce := f.Close()
	if we != nil || se != nil || ce != nil {
		return "", false, errors.New("application_recovery_record_failed")
	}
	if err := recordRecovery(o, backup, filepath.Join(container, "RECOVERY.txt")); err != nil {
		return backup, false, err
	}
	return replaceApplication(ctx, real, app, backup, preview.InstalledSHA256, stageDigest, func(path string) error { return verifyApplicationPayload(ctx, runner, path, preview) }, exchangeApplications)
}

// exchange is injected only by isolated filesystem tests. Production uses
// Darwin's atomic directory swap, keeping the installation path present even
// if the process exits immediately before or after the exchange.
func replaceApplication(ctx context.Context, target, staged, backup, oldDigest, newDigest string, verify func(string) error, exchange func(string, string) error) (string, bool, error) {
	if e := os.Rename(staged, backup); e != nil {
		return "", false, errors.New("application_staging_move_failed")
	}
	if ctx.Err() != nil {
		return "", false, ctx.Err()
	}
	if e := exchange(target, backup); e != nil {
		return "", false, errors.New("application_atomic_swap_failed")
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	restore := func(reason string) (string, bool, error) {
		// Unexpected or concurrently edited content is never deleted or silently
		// moved out from under another writer. Preserve both paths for recovery.
		installed, e := applicationTreeHash(recoveryCtx, target)
		if e != nil || installed != newDigest {
			return backup, true, errors.New("application_target_changed_original_preserved")
		}
		if e = exchange(target, backup); e != nil {
			return backup, true, errors.New("application_restore_failed_original_preserved")
		}
		digest, e := applicationTreeHash(recoveryCtx, target)
		if e != nil || digest != oldDigest {
			return backup, true, errors.New("application_restore_verification_failed")
		}
		// backup now holds the failed candidate. Retain it rather than deleting
		// evidence or any content that may have been introduced concurrently.
		return backup, false, errors.New(reason + "_original_restored")
	}
	digest, e := applicationTreeHash(recoveryCtx, backup)
	if e != nil || digest != oldDigest {
		return restore("application_original_changed")
	}
	digest, e = applicationTreeHash(ctx, target)
	if e != nil || digest != newDigest {
		return restore("application_installed_content_changed")
	}
	if e = verify(target); e != nil {
		return restore("application_post_verification_failed")
	}
	return backup, true, nil
}
