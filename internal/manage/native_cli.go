package manage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

var uvVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
var sha256Text = regexp.MustCompile(`^[a-f0-9]{64}$`)

type NativeCLIUpdate struct {
	ArchiveSHA256 string            `json:"archive_sha256"`
	CurrentFiles  map[string]string `json:"current_files"`
	TargetFiles   map[string]string `json:"target_files"`
	ReceiptSHA256 string            `json:"receipt_sha256"`
	Architecture  string            `json:"architecture"`
}

func nativeCLIPlan(i audit.Item, p PlanItem, report audit.Report, o Options, s Selection) PlanItem {
	p.Status, p.Reason = "manual", "native_cli_requires_separate_review"
	if s.Action != "update" {
		p.Reason = "native_cli_uninstall_not_supported"
		return p
	}
	receipt, err := audit.ReadUVReceipt(o.Home, i.Path)
	if err != nil {
		p.Reason = err.Error()
		return p
	}
	if i.Name != "uv" || i.Identity != "uv" || i.Kind != "executable" || i.Source != "github:astral-sh/uv" || i.Channel != "stable" || receipt.Version != i.Version {
		p.Reason = "native_cli_identity_changed"
		return p
	}
	p.Binding.RealPath, p.Binding.ManagerPath, p.Binding.Scope, p.Binding.WasActive = i.Path, i.Path, receipt.InstallPrefix, i.Active
	target, ok := o.ResolvedTargets[i.ID]
	if !ok {
		p.Reason = "native_cli_candidate_required"
		return p
	}
	p.TargetVersion = target.Version
	if !target.OriginVerified || target.NativeCLI == nil {
		p.Reason = target.Reason
		return p
	}
	if target.Provider != i.Owner || target.Scope != receipt.InstallPrefix || target.ManagerPath != i.Path || target.Source != i.Source || !uvVersion.MatchString(target.Version) {
		p.Reason = "native_cli_candidate_unverified"
		return p
	}
	p.Binding.ResolvedTarget = &target
	p.Binding.Fingerprint, err = nativeCLIFingerprint(o.Home, target.NativeCLI)
	if err != nil {
		p.Reason = "native_cli_content_changed"
		return p
	}
	p.Binding.ManagerFingerprint = target.NativeCLI.CurrentFiles["uv"]
	if target.Version == i.Version {
		p.Status, p.Reason = "skipped", "already_current"
		return p
	}
	p.RequiresScripts = true
	p.AffectedPaths = []string{i.Path, filepath.Join(receipt.InstallPrefix, "uvx"), audit.UVReceiptPath(o.Home)}
	if !reviewedRuntime(i, &p, report, s) {
		return p
	}
	p.Status, p.Reason = "ready", "native_cli_original_source_verified"
	return p
}

func nativeCLIFingerprint(home string, update *NativeCLIUpdate) (string, error) {
	if len(update.CurrentFiles) != 2 || len(update.TargetFiles) != 2 || !sha256Text.MatchString(update.ArchiveSHA256) {
		return "", errors.New("invalid artifact binding")
	}
	digest := ""
	for _, name := range []string{"uv", "uvx"} {
		file := filepath.Join(home, ".local/bin", name)
		if ownedPath(file, true) != nil || ownedPath(file, false) != nil {
			return "", errors.New("native binary not owned")
		}
		hash, err := fileHash(file)
		if err != nil || hash != update.CurrentFiles[name] || !sha256Text.MatchString(update.TargetFiles[name]) {
			return "", errors.New("native binary modified")
		}
		digest += hash
	}
	receipt := audit.UVReceiptPath(home)
	if ownedPath(receipt, false) != nil {
		return "", errors.New("receipt not owned")
	}
	hash, err := fileHash(receipt)
	if err != nil || hash != update.ReceiptSHA256 {
		return "", errors.New("receipt changed")
	}
	return hashText(digest + hash), nil
}

// ResolveNativeCLITargets validates the installed and candidate official archives.
// No local executable or downloaded installer runs during this read-only step.
func ResolveNativeCLITargets(ctx context.Context, report audit.Report, selection Selection, options Options, _ Runner) (map[string]ResolvedTarget, error) {
	o := normalizeOptions(options)
	out := map[string]ResolvedTarget{}
	if selection.Action != "update" {
		return out, nil
	}
	for _, i := range report.Items {
		if i.Owner != "uv-standalone" || !selection.All && !contains(selection.IDs, i.ID) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		resolutionCurrent(o, i.Name)
		target := ResolvedTarget{Provider: i.Owner, ManagerPath: i.Path, Scope: filepath.Dir(i.Path), Source: "github:astral-sh/uv", Reason: "native_cli_source_unverified"}
		out[i.ID] = target
		receipt, err := audit.ReadUVReceipt(o.Home, i.Path)
		if err != nil || receipt.Version != i.Version || len(i.Architecture) != 1 {
			continue
		}
		arch := ""
		switch i.Architecture[0] {
		case "arm64":
			arch = "aarch64-apple-darwin"
		case "x86_64":
			arch = "x86_64-apple-darwin"
		}
		if arch == "" || runtime.GOOS != "darwin" {
			continue
		}
		current, _, err := uvReleaseFiles(ctx, o, receipt.Version, arch)
		if err != nil {
			target.Reason = "native_cli_installed_release_unavailable"
			out[i.ID] = target
			continue
		}
		receiptHash, err := fileHash(audit.UVReceiptPath(o.Home))
		if err != nil {
			continue
		}
		latest, err := uvFetch(ctx, o, "https://api.github.com/repos/astral-sh/uv/releases/latest", 1<<20)
		var release struct {
			Tag        string `json:"tag_name"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
		}
		if err != nil || json.Unmarshal(latest, &release) != nil || !uvVersion.MatchString(release.Tag) || release.Draft || release.Prerelease {
			target.Reason = "native_cli_candidate_unavailable"
			out[i.ID] = target
			continue
		}
		target.Version = release.Tag
		if cmp, known := audit.CompareVersions(i.Version, target.Version); !known {
			continue
		} else if cmp >= 0 {
			target.Version = i.Version
		}
		candidate, archiveHash, err := uvReleaseFiles(ctx, o, target.Version, arch)
		if err != nil {
			target.Reason = "native_cli_candidate_artifact_unavailable"
			out[i.ID] = target
			continue
		}
		update := &NativeCLIUpdate{CurrentFiles: current, TargetFiles: candidate, ArchiveSHA256: archiveHash, ReceiptSHA256: receiptHash, Architecture: arch}
		if _, err := nativeCLIFingerprint(o.Home, update); err != nil {
			target.Reason = "native_cli_local_modifications_or_permissions"
			out[i.ID] = target
			continue
		}
		target.NativeCLI, target.OriginVerified, target.Reason = update, true, "native_cli_original_source_verified"
		out[i.ID] = target
	}
	return out, ctx.Err()
}

func uvFetch(ctx context.Context, o Options, url string, limit int64) ([]byte, error) {
	client := o.NativeCLIHTTPClient
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Timeout: 2 * time.Minute, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 || req.URL.Scheme != "https" || req.URL.User != nil || !contains([]string{"github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"}, req.URL.Host) {
				return errors.New("untrusted release redirect")
			}
			return nil
		}}
		defer transport.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "KeepUpdated")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("official release unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("release size exceeded")
	}
	return data, nil
}

func uvReleaseFiles(ctx context.Context, o Options, version, arch string) (map[string]string, string, error) {
	if !uvVersion.MatchString(version) || !contains([]string{"aarch64-apple-darwin", "x86_64-apple-darwin"}, arch) {
		return nil, "", errors.New("invalid release identity")
	}
	archive := "uv-" + arch + ".tar.gz"
	url := "https://github.com/astral-sh/uv/releases/download/" + version + "/" + archive
	checksum, err := uvFetch(ctx, o, url+".sha256", 1024)
	fields := strings.Fields(string(checksum))
	if err != nil || len(fields) != 2 || !sha256Text.MatchString(fields[0]) || fields[1] != archive {
		return nil, "", errors.New("release checksum unavailable")
	}
	data, err := uvFetch(ctx, o, url, 96<<20)
	if err != nil || hashText(string(data)) != fields[0] {
		return nil, "", errors.New("release checksum mismatch")
	}
	reader, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return nil, "", err
	}
	defer reader.Close()
	limited := &io.LimitedReader{R: reader, N: 256 << 20}
	tarReader := tar.NewReader(limited)
	files := map[string]string{}
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		clean := path.Clean(header.Name)
		if clean != strings.TrimSuffix(header.Name, "/") || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") {
			return nil, "", errors.New("invalid archive path")
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 128<<20 {
			return nil, "", errors.New("unsupported archive entry")
		}
		name := path.Base(clean)
		if name != "uv" && name != "uvx" {
			continue
		}
		if clean != "uv-"+arch+"/"+name || files[name] != "" || header.Mode&0111 == 0 {
			return nil, "", errors.New("unexpected binary entry")
		}
		hash := sha256.New()
		if _, err = io.Copy(hash, tarReader); err != nil {
			return nil, "", err
		}
		files[name] = hex.EncodeToString(hash.Sum(nil))
	}
	if limited.N <= 0 || len(files) != 2 {
		return nil, "", errors.New("incomplete release archive")
	}
	return files, fields[0], nil
}

// The backup survives failed verification. Recovery never overwrites unexpected content.
func applyNativeCLI(ctx context.Context, p PlanItem, o Options, state string, runner Runner, execution *ItemResult) (string, bool, error) {
	return applyNativeCLIWithOps(ctx, p, o, state, runner, execution, nativeCLIOps{read: readNativeFile, writeBackup: writeNativeBackup, rename: os.Rename, link: os.Link})
}

type nativeCLIOps struct {
	read        func(string) ([]byte, os.FileMode, error)
	writeBackup func(string, []byte) error
	rename      func(string, string) error
	link        func(string, string) error
}

func applyNativeCLIWithOps(ctx context.Context, p PlanItem, o Options, state string, runner Runner, execution *ItemResult, ops nativeCLIOps) (string, bool, error) {
	update := p.Binding.ResolvedTarget.NativeCLI
	if _, err := nativeCLIFingerprint(o.Home, update); err != nil {
		return "", false, err
	}
	backup, err := os.MkdirTemp(state, "native-cli-backup-")
	if err != nil {
		return "", false, err
	}
	paths := []string{p.Binding.Path, filepath.Join(p.Binding.Scope, "uvx"), audit.UVReceiptPath(o.Home)}
	originals := map[string][]byte{}
	modes := map[string]os.FileMode{}
	for _, file := range paths {
		data, mode, err := ops.read(file)
		expected := update.CurrentFiles[filepath.Base(file)]
		if file == audit.UVReceiptPath(o.Home) {
			expected = update.ReceiptSHA256
		}
		if err != nil || hashText(string(data)) != expected {
			return backup, false, errors.New("native_cli_backup_content_changed")
		}
		originals[file], modes[file] = data, mode
		backupFile := filepath.Join(backup, filepath.Base(file))
		if err = ops.writeBackup(backupFile, data); err != nil {
			return backup, false, err
		}
		// Verify what was actually persisted, not only the live installation path.
		if digest, err := fileHash(backupFile); err != nil || digest != expected {
			return backup, false, errors.New("native_cli_backup_content_changed")
		}
	}
	// Recheck after copying: a concurrent edit must not become the rollback baseline.
	if _, err = nativeCLIFingerprint(o.Home, update); err != nil {
		return backup, false, err
	}
	command, err := commandFor(p, "update", o, state)
	if err != nil {
		return backup, false, err
	}
	if err := recordRecovery(o, backup, ""); err != nil {
		return backup, false, err
	}
	execution.Executed = true
	_, runErr := runner.Run(ctx, command)
	execution.CommandSucceeded = runErr == nil
	if ctx.Err() != nil {
		return backup, true, errors.New("native_cli_recovery_requires_review")
	}
	if runErr == nil && verifyNativeCLI(p, o) {
		return backup, true, nil
	}
	if err = restoreNativeCLI(p, paths, originals, modes, backup, ops); err != nil {
		return backup, true, err
	}
	if _, err = nativeCLIFingerprint(o.Home, update); err != nil {
		return backup, true, errors.New("native_cli_recovery_requires_review")
	}
	return backup, false, errors.New("native_cli_failed_original_restored")
}

func verifyNativeCLI(p PlanItem, o Options) bool {
	receipt, err := audit.ReadUVReceipt(o.Home, p.Binding.Path)
	if err != nil || receipt.Version != p.TargetVersion {
		return false
	}
	for _, name := range []string{"uv", "uvx"} {
		file := filepath.Join(p.Binding.Scope, name)
		hash, err := fileHash(file)
		if err != nil || hash != p.Binding.ResolvedTarget.NativeCLI.TargetFiles[name] || ownedPath(file, true) != nil {
			return false
		}
	}
	return true
}

func writeNativeBackup(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func readNativeFile(path string) ([]byte, os.FileMode, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 256<<20 {
		return nil, 0, errors.New("native_cli_file_unverified")
	}
	data, err := io.ReadAll(io.LimitReader(file, (256<<20)+1))
	if err != nil || len(data) > 256<<20 {
		return nil, 0, errors.New("native_cli_file_unverified")
	}
	return data, st.Mode().Perm(), nil
}

type nativeRecoveryFile struct {
	Path           string `json:"path"`
	Staged         string `json:"staged"`
	Displaced      string `json:"displaced"`
	OriginalSHA256 string `json:"original_sha256"`
}

func nativeCandidateReceipt(data, original []byte, target string) bool {
	var before, after audit.UVReceipt
	if json.Unmarshal(original, &before) != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&after) != nil || decoder.Decode(new(any)) != io.EOF || after.Version != target {
		return false
	}
	after.Version = before.Version
	after.Provider.Version = before.Provider.Version
	after.ModifyPath = before.ModifyPath
	sort.Strings(before.Binaries)
	sort.Strings(after.Binaries)
	return reflect.DeepEqual(before, after)
}

// Capture each destination before replacing it. No-overwrite links preserve edits
// made both before capture and while an earlier file is being restored.
func restoreNativeCLI(p PlanItem, paths []string, originals map[string][]byte, modes map[string]os.FileMode, backup string, ops nativeCLIOps) error {
	review := errors.New("native_cli_recovery_requires_review")
	entries := []nativeRecoveryFile{}
	for _, file := range paths {
		dir, err := os.MkdirTemp(filepath.Dir(file), ".keepupdated-recovery-")
		if err != nil {
			return review
		}
		entry := nativeRecoveryFile{Path: file, Staged: filepath.Join(dir, "original"), Displaced: filepath.Join(dir, "displaced"), OriginalSHA256: hashText(string(originals[file]))}
		if err = writeNativeBackup(entry.Staged, originals[file]); err != nil {
			return review
		}
		if err = os.Chmod(entry.Staged, modes[file]); err != nil {
			return review
		}
		entries = append(entries, entry)
	}
	manifest, err := json.MarshalIndent(entries, "", "  ")
	if err != nil || writeNativeBackup(filepath.Join(backup, "recovery.json"), manifest) != nil {
		return review
	}
	displacedHashes := map[string]string{}
	for _, entry := range entries {
		data, mode, err := ops.read(entry.Path)
		if err != nil || mode != modes[entry.Path] {
			return review
		}
		allowed := entry.OriginalSHA256
		candidate := p.Binding.ResolvedTarget.NativeCLI.TargetFiles[filepath.Base(entry.Path)]
		if filepath.Base(entry.Path) == "uv-receipt.json" && nativeCandidateReceipt(data, originals[entry.Path], p.TargetVersion) {
			candidate = hashText(string(data))
		}
		digest := hashText(string(data))
		if digest != allowed && digest != candidate {
			return review
		}
		if err = ops.rename(entry.Path, entry.Displaced); err != nil {
			return review
		}
		captured, capturedMode, err := ops.read(entry.Displaced)
		if err != nil || capturedMode != mode || hashText(string(captured)) != digest {
			// Put the captured user object back only if nobody has created a new destination.
			_ = ops.link(entry.Displaced, entry.Path)
			return review
		}
		displacedHashes[entry.Displaced] = digest
		if err = ops.link(entry.Staged, entry.Path); err != nil {
			return review
		}
		current, currentErr := os.Lstat(entry.Path)
		staged, stagedErr := os.Lstat(entry.Staged)
		installed, hashErr := fileHash(entry.Path)
		retained, retainedErr := fileHash(entry.Displaced)
		if currentErr != nil || stagedErr != nil || !os.SameFile(current, staged) || hashErr != nil || installed != allowed || retainedErr != nil || retained != digest {
			return review
		}
	}
	// Displaced objects remain retained even after successful recovery.
	for _, entry := range entries {
		if digest, err := fileHash(entry.Displaced); err != nil || digest != displacedHashes[entry.Displaced] {
			return review
		}
	}
	return nil
}
