package manage

import (
	"context"
	"crypto/sha256"
	"debug/macho"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

type ApplicationUpdatePreview struct {
	Archive         audit.ApplicationArchive `json:"archive"`
	BundleID        string                   `json:"bundle_id"`
	TeamID          string                   `json:"team_id"`
	InstalledSHA256 string                   `json:"installed_sha256"`
	Architecture    string                   `json:"architecture"`
	MacOS           string                   `json:"macos"`
}

type applicationInfo struct {
	ID                string            `json:"CFBundleIdentifier"`
	Version           string            `json:"CFBundleShortVersionString"`
	Build             string            `json:"CFBundleVersion"`
	Executable        string            `json:"CFBundleExecutable"`
	Minimum           string            `json:"LSMinimumSystemVersion"`
	MinimumByArch     map[string]string `json:"LSMinimumSystemVersionByArchitecture"`
	Feed              string            `json:"SUFeedURL"`
	Key               string            `json:"SUPublicEDKey"`
	RequireSignedFeed bool              `json:"SURequireSignedFeed"`
}

func appCommand(ctx context.Context, runner Runner, path string, args ...string) ([]byte, error) {
	return runner.Run(ctx, Command{Path: path, Args: args, Env: []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}})
}

func readApplicationInfo(ctx context.Context, runner Runner, path string) (applicationInfo, error) {
	var info applicationInfo
	b, err := appCommand(ctx, runner, "/usr/bin/plutil", "-convert", "json", "-o", "-", filepath.Join(path, "Contents/Info.plist"))
	if err != nil || json.Unmarshal(b, &info) != nil || info.ID == "" || info.Build == "" || info.Executable == "" || filepath.Base(info.Executable) != info.Executable || info.Executable == "." || info.Executable == ".." {
		return info, errors.New("application_metadata_unverified")
	}
	if info.Version == "" {
		info.Version = info.Build
	}
	return info, nil
}

var applicationTeam = regexp.MustCompile(`certificate leaf\[subject\.OU\] = "?([A-Z0-9]{10})"?(?:[^A-Z0-9]|$)`)
var applicationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]+\.[A-Za-z0-9.-]+$`)

func verifyApplicationSignature(ctx context.Context, runner Runner, path, id, team string) (string, error) {
	if !applicationID.MatchString(id) {
		return "", errors.New("application_bundle_identity_unverified")
	}
	b, err := appCommand(ctx, runner, "/usr/bin/codesign", "-d", "-r-", path)
	matches := applicationTeam.FindAllStringSubmatch(string(b), -1)
	if err != nil || len(matches) != 1 || team != "" && matches[0][1] != team {
		return "", errors.New("application_developer_identity_changed")
	}
	team = matches[0][1]
	requirement := fmt.Sprintf(`=anchor apple generic and identifier "%s" and certificate leaf[subject.OU] = "%s" and certificate leaf[field.1.2.840.113635.100.6.1.13] exists`, id, team)
	_, err = appCommand(ctx, runner, "/usr/bin/codesign", "--verify", "--deep", "--strict", "--all-architectures", "-R", requirement, path)
	if err != nil {
		return "", errors.New("application_signature_invalid")
	}
	return team, nil
}

func applicationPreferencesUnchanged(ctx context.Context, runner Runner, home string, info applicationInfo) bool {
	path := filepath.Join(home, "Library/Preferences", info.ID+".plist")
	if _, e := os.Lstat(path); os.IsNotExist(e) {
		return true
	}
	b, e := appCommand(ctx, runner, "/usr/bin/plutil", "-convert", "json", "-o", "-", path)
	var prefs map[string]any
	if e != nil || json.Unmarshal(b, &prefs) != nil {
		return false
	}
	for k, v := range prefs {
		if !strings.HasPrefix(k, "SU") {
			continue
		}
		if strings.Contains(k, "Feed") || strings.Contains(k, "Channel") || strings.Contains(k, "Skipped") {
			if k == "SUFeedURL" && v == info.Feed {
				continue
			}
			if v == "" || v == nil {
				continue
			}
			return false
		}
	}
	return true
}

func resolveApplicationUpdate(ctx context.Context, i audit.Item, report audit.Report, o Options, runner Runner) ResolvedTarget {
	t := ResolvedTarget{Provider: "native-app", Scope: i.Path, Reason: "application_signed_source_required"}
	fail := func(err error) ResolvedTarget { t.Reason = err.Error(); return t }
	if i.Owner != "standalone" || i.Source != "sparkle" {
		t.Reason = "application_update_requires_vendor_adapter"
		return t
	}
	if err := ownedPath(i.Path, false); err != nil {
		t.Reason = "application_requires_owner_permission"
		return t
	}
	if i.Health.Status == "error" {
		t.Reason = "application_health_error"
		return t
	}
	info, err := readApplicationInfo(ctx, runner, i.Path)
	if err != nil {
		return fail(err)
	}
	if info.ID != i.Identity || info.Version != i.Version || info.Build != i.Build {
		t.Reason = "application_metadata_changed"
		return t
	}
	if info.RequireSignedFeed {
		t.Reason = "application_signed_feed_requires_vendor"
		return t
	}
	if !applicationPreferencesUnchanged(ctx, runner, o.Home, info) {
		t.Reason = "application_update_preferences_require_vendor"
		return t
	}
	team, err := verifyApplicationSignature(ctx, runner, i.Path, info.ID, "")
	if err != nil {
		return fail(err)
	}
	arch := report.Architecture
	if arch == "amd64" {
		arch = "x86_64"
	}
	if arch != "arm64" && arch != "x86_64" || !applicationArchitecture(i.Path, info.Executable, arch) {
		t.Reason = "application_architecture_unverified"
		return t
	}
	osBytes, err := appCommand(ctx, runner, "/usr/bin/sw_vers", "-productVersion")
	osVersion := strings.TrimSpace(string(osBytes))
	if err != nil || !numericApplicationVersion(osVersion) {
		t.Reason = "application_macos_unverified"
		return t
	}
	archive, err := audit.ResolveApplicationArchive(ctx, i, info.Feed, info.Key, osVersion, arch, o.ApplicationHTTPClient)
	if err != nil {
		return fail(err)
	}
	digest, err := applicationTreeHash(ctx, i.Path)
	if err != nil {
		return fail(err)
	}
	preview := ApplicationUpdatePreview{Archive: archive, BundleID: info.ID, TeamID: team, InstalledSHA256: digest, Architecture: arch, MacOS: osVersion}
	b, _ := json.Marshal(preview)
	t.Version, t.FromVersion, t.Source, t.OriginVerified, t.Reason, t.ApplicationUpdate = archive.Version, i.Version, hashText(string(b)), true, "signed_application_zip_verified", &preview
	if archive.Format == "dmg" {
		t.Reason = "signed_application_dmg_verified"
	}
	return t
}

func applicationArchitecture(path, executable, arch string) bool {
	path = filepath.Join(path, "Contents/MacOS", executable)
	if st, e := os.Stat(path); e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		return false
	}
	cpu := macho.CpuArm64
	if arch == "x86_64" {
		cpu = macho.CpuAmd64
	}
	if f, e := macho.OpenFat(path); e == nil {
		defer f.Close()
		for _, a := range f.Arches {
			if a.Cpu == cpu {
				return true
			}
		}
		return false
	}
	if f, e := macho.Open(path); e == nil {
		defer f.Close()
		return f.Cpu == cpu
	}
	return false
}

func numericApplicationVersion(v string) bool {
	return regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*$`).MatchString(v)
}
func applicationOSCompatible(current, minimum string) bool {
	if minimum == "" {
		return true
	}
	if !numericApplicationVersion(current) || !numericApplicationVersion(minimum) {
		return false
	}
	a, b := strings.Split(current, "."), strings.Split(minimum, ".")
	for n := 0; n < len(a) || n < len(b); n++ {
		var x, y uint64
		if n < len(a) {
			if _, e := fmt.Sscan(a[n], &x); e != nil {
				return false
			}
		}
		if n < len(b) {
			if _, e := fmt.Sscan(b[n], &y); e != nil {
				return false
			}
		}
		if x != y {
			return x > y
		}
	}
	return true
}

// Include every regular file, mode and contained symlink, including unsealed
// local additions. Signature verification alone does not bind those additions.
func applicationTreeHash(ctx context.Context, root string) (string, error) {
	h := sha256.New()
	var size int64
	count := 0
	real, e := filepath.EvalSymlinks(root)
	if e != nil {
		return "", errors.New("application_path_unavailable")
	}
	e = filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		count++
		if count > 100000 {
			return errors.New("too many files")
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(root, path)
		fmt.Fprintf(h, "%d:%s:%o:", len(rel), rel, st.Mode())
		switch {
		case st.Mode()&os.ModeSymlink != 0:
			target, e := os.Readlink(path)
			if e != nil {
				return e
			}
			resolved, e := filepath.EvalSymlinks(path)
			if e != nil || !under(resolved, real) {
				return errors.New("escaping link")
			}
			fmt.Fprintf(h, "%d:%s", len(target), target)
		case st.IsDir():
		case st.Mode().IsRegular():
			size += st.Size()
			if size > 4<<30 {
				return errors.New("application too large")
			}
			fmt.Fprintf(h, "%d:", st.Size())
			f, e := os.Open(path)
			if e != nil {
				return e
			}
			_, e = io.Copy(h, f)
			closeErr := f.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return errors.New("special file")
		}
		return nil
	})
	if e != nil {
		return "", errors.New("application_content_unverified")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifyApplicationPayload(ctx context.Context, runner Runner, path string, p ApplicationUpdatePreview) error {
	info, e := readApplicationInfo(ctx, runner, path)
	if e != nil {
		return e
	}
	if info.ID != p.BundleID || info.Version != p.Archive.Version || info.Build != p.Archive.Build {
		return errors.New("application_payload_identity_or_version_mismatch")
	}
	if info.Feed != p.Archive.FeedURL || info.Key != p.Archive.PublicKey {
		return errors.New("application_payload_source_changed")
	}
	if !applicationArchitecture(path, info.Executable, p.Architecture) || !applicationOSCompatible(p.MacOS, info.Minimum) || !applicationOSCompatible(p.MacOS, info.MinimumByArch[p.Architecture]) {
		return errors.New("application_payload_incompatible")
	}
	if _, e = verifyApplicationSignature(ctx, runner, path, p.BundleID, p.TeamID); e != nil {
		return e
	}
	if _, e = appCommand(ctx, runner, "/usr/sbin/spctl", "--assess", "--type", "execute", path); e != nil {
		return errors.New("application_gatekeeper_rejected")
	}
	return nil
}

func nativeApplicationClosed(ctx context.Context, runner Runner, path string) bool {
	b, e := appCommand(ctx, runner, "/bin/ps", "-axo", "comm=")
	if e != nil {
		return false
	}
	real, e := filepath.EvalSymlinks(path)
	if e != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		p := strings.TrimSpace(line)
		if p == real || strings.HasPrefix(p, real+"/") {
			return false
		}
		if r, e := filepath.EvalSymlinks(p); e == nil && (r == real || strings.HasPrefix(r, real+"/")) {
			return false
		}
	}
	return true
}

func applicationParentWritable(path string) bool {
	parent := filepath.Dir(path)
	st, e := os.Stat(parent)
	if e != nil || !st.IsDir() || st.Mode().Perm()&0002 != 0 {
		return false
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	return ok && (stat.Uid == uint32(os.Getuid()) || stat.Uid == 0) && syscall.Access(parent, 3) == nil
}

func applicationUpdateError(err error) string {
	if err == nil {
		return ""
	}
	if regexp.MustCompile(`^application_[a-z_]+$`).MatchString(err.Error()) {
		return err.Error()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "application_update_cancelled"
	}
	return "application_update_failed"
}
