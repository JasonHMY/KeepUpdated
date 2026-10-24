package manage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const SelfUpdateSchema = "keepupdated/self-update/v1"
const selfRepository = "JasonHMY/KeepUpdated"
const selfBundleID = "com.keepupdated.desktop"

var releaseVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type SelfUpdateAsset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
	State  string `json:"state"`
}
type SelfUpdateRelease struct {
	ID         int64             `json:"id"`
	Tag        string            `json:"tag_name"`
	URL        string            `json:"html_url"`
	Notes      string            `json:"body"`
	Draft      bool              `json:"draft"`
	Prerelease bool              `json:"prerelease"`
	Published  string            `json:"published_at"`
	Assets     []SelfUpdateAsset `json:"assets"`
}

// Evidence describes collected checks, never installation eligibility.
// Empty fields in older records mean that no evidence was recorded.
type SelfUpdateEvidence struct {
	Repository                string `json:"repository,omitempty"`
	Release                   string `json:"release,omitempty"`
	ArchiveLength             string `json:"archive_length,omitempty"`
	ArchiveHash               string `json:"archive_hash,omitempty"`
	ArchivePublisherSignature string `json:"archive_publisher_signature,omitempty"`
	InstalledPayloadSignature string `json:"installed_payload_signature,omitempty"`
	CandidatePayloadSignature string `json:"candidate_payload_signature,omitempty"`
	MetadataFreshness         string `json:"metadata_freshness,omitempty"`
	InstallationReadback      string `json:"installation_readback,omitempty"`
	Reopen                    string `json:"reopen,omitempty"`
}

type SelfUpdateCheck struct {
	Evidence       *SelfUpdateEvidence `json:"evidence,omitempty"`
	TeamID         string              `json:"team_id,omitempty"`
	Schema         string              `json:"schema"`
	Status         string              `json:"status"`
	Reason         string              `json:"reason,omitempty"`
	Path           string              `json:"path"`
	CurrentVersion string              `json:"current_version"`
	CurrentBuild   string              `json:"current_build"`
	TargetVersion  string              `json:"target_version,omitempty"`
	CanInstall     bool                `json:"can_install"`
	Release        *SelfUpdateRelease  `json:"release,omitempty"`
	Archive        *SelfUpdateAsset    `json:"archive,omitempty"`
	Checksums      *SelfUpdateAsset    `json:"checksums,omitempty"`
}

// Dependencies are private: fixtures can exercise the entire flow, but no CLI
// flag or environment variable can replace the production trust root or verifier.
type selfUpdater struct {
	executable func() (string, error)
	parentPID  func() int
	client     *http.Client
	api        string
	team       string
	runner     Runner
}

func productionSelfUpdater() selfUpdater {
	u := selfUpdater{client: &http.Client{Timeout: 2 * time.Minute}, api: "https://api.github.com/repos/" + selfRepository, executable: os.Executable, parentPID: os.Getppid, runner: ExecRunner{}}
	return u
}
func CheckSelfUpdate(ctx context.Context, app string) (SelfUpdateCheck, error) {
	return productionSelfUpdater().check(ctx, app)
}

func (u selfUpdater) get(ctx context.Context, address string, limit int64, assets bool) ([]byte, int, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" {
		return nil, 0, errors.New("self_update_source_unverified")
	}
	client := *u.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !assets || len(via) > 4 || req.URL.Scheme != "https" || req.URL.User != nil || (req.URL.Host != "release-assets.githubusercontent.com" && req.URL.Host != "objects.githubusercontent.com" && req.URL.Host != "github.com") {
			return errors.New("self_update_redirect_rejected")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "KeepUpdated-App-Updates")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, errors.New("self_update_network_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, errors.New("self_update_release_query_failed")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, resp.StatusCode, errors.New("self_update_response_invalid")
	}
	return b, resp.StatusCode, nil
}
func (u selfUpdater) release(ctx context.Context, evidence *SelfUpdateEvidence) (*SelfUpdateRelease, error) {
	if evidence != nil {
		evidence.Repository = "unverified"
	}
	// A missing release is distinguishable from an inaccessible or renamed repository.
	b, _, err := u.get(ctx, u.api, 1<<20, false)
	var repo struct {
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
		ID       int64  `json:"id"`
	}
	if err != nil || json.Unmarshal(b, &repo) != nil || repo.FullName != selfRepository || repo.Private || repo.ID <= 0 {
		return nil, errors.New("self_update_repository_unverified")
	}
	if evidence != nil {
		evidence.Repository, evidence.Release = "verified", "unverified"
	}
	b, status, err := u.get(ctx, u.api+"/releases/latest", 2<<20, false)
	if status == http.StatusNotFound {
		b, _, err = u.get(ctx, u.api+"/releases?per_page=1", 2<<20, false)
		var releases []SelfUpdateRelease
		// Only an empty, successful public listing proves no public release. A
		// prerelease-only history is deliberately reported unknown, not up to date.
		if err == nil && json.Unmarshal(b, &releases) == nil && releases != nil && len(releases) == 0 {
			return nil, nil
		}
		return nil, errors.New("self_update_stable_release_unverified")
	}
	var r SelfUpdateRelease
	if err != nil || json.Unmarshal(b, &r) != nil {
		return nil, errors.New("self_update_release_query_failed")
	}
	v := strings.TrimPrefix(r.Tag, "v")
	if r.ID <= 0 || r.Draft || r.Prerelease || r.Published == "" || !validSelfVersion(v) || r.Tag != "v"+v || r.URL != "https://github.com/"+selfRepository+"/releases/tag/"+r.Tag {
		return nil, errors.New("self_update_release_unverified")
	}
	if _, err = time.Parse(time.RFC3339, r.Published); err != nil {
		return nil, errors.New("self_update_release_unverified")
	}
	return &r, nil
}
func selfAsset(r *SelfUpdateRelease, name string) (*SelfUpdateAsset, error) {
	var found *SelfUpdateAsset
	for _, a := range r.Assets {
		if a.Name != name {
			continue
		}
		if found != nil || a.ID <= 0 || a.State != "uploaded" || a.Size <= 0 || a.Size > 512<<20 || a.URL != "https://github.com/"+selfRepository+"/releases/download/"+r.Tag+"/"+name || !strings.HasPrefix(a.Digest, "sha256:") || !sha256Pattern.MatchString(strings.TrimPrefix(a.Digest, "sha256:")) {
			return nil, errors.New("self_update_asset_unverified")
		}
		copy := a
		found = &copy
	}
	if found == nil {
		return nil, errors.New("self_update_asset_missing")
	}
	return found, nil
}
func (u selfUpdater) check(ctx context.Context, app string) (SelfUpdateCheck, error) {
	c := SelfUpdateCheck{Schema: SelfUpdateSchema, Status: "failed", Path: app,
		Evidence: &SelfUpdateEvidence{Repository: "not_checked", Release: "not_checked",
			InstalledPayloadSignature: "not_checked", MetadataFreshness: "unverified"}}
	fail := func(e error) (SelfUpdateCheck, error) { c.Reason = e.Error(); return c, e }
	if !filepath.IsAbs(app) || filepath.Clean(app) != app || filepath.Ext(app) != ".app" {
		return fail(errors.New("self_update_app_unverified"))
	}
	info, err := readApplicationInfo(ctx, u.runner, app)
	if err != nil || info.ID != selfBundleID || info.Executable != "KeepUpdatedDesktop" || !validSelfVersion(info.Version) || !numericApplicationVersion(info.Build) {
		return fail(errors.New("self_update_app_unverified"))
	}
	c.CurrentVersion, c.CurrentBuild = info.Version, info.Build
	r, err := u.release(ctx, c.Evidence)
	if err != nil {
		return fail(err)
	}
	c.Evidence.Repository = "verified"
	if r == nil {
		c.Evidence.Release = "not_applicable"
		c.Status = "not_published"
		return c, nil
	}
	c.Release = r
	c.Evidence.Release = "verified"
	c.Evidence.ArchiveLength, c.Evidence.ArchiveHash = "not_checked", "not_checked"
	c.Evidence.ArchivePublisherSignature = "unverified"
	c.Evidence.CandidatePayloadSignature = "not_checked"
	c.Evidence.InstalledPayloadSignature = "unverified"
	team, trustError := u.currentTeam(ctx, app)
	if trustError == nil {
		u.team = team
		c.TeamID = team
	}
	c.TargetVersion = strings.TrimPrefix(r.Tag, "v")
	if !applicationOSCompatible(c.TargetVersion, c.CurrentVersion) || c.TargetVersion == c.CurrentVersion {
		if trustError != nil {
			return fail(trustError)
		}
		if err = u.verifySignedBundle(ctx, app, c.CurrentVersion, c.CurrentBuild); err != nil {
			return fail(err)
		}
		c.Evidence.InstalledPayloadSignature = "verified"
		c.Status = "up_to_date"
		if c.TargetVersion != c.CurrentVersion {
			c.Status = "newer_than_release"
		}
		return c, nil
	}
	c.Status = "available"
	if runtime.GOARCH != "arm64" {
		c.Reason = "self_update_architecture_unsupported"
		return c, nil
	}
	c.Archive, err = selfAsset(r, "KeepUpdated_"+c.TargetVersion+"_darwin_arm64.zip")
	if err == nil {
		c.Checksums, err = selfAsset(r, "KeepUpdated_"+c.TargetVersion+"_SHA256SUMS")
	}
	if err == nil {
		err = trustError
	}
	if err == nil {
		err = u.verifySignedBundle(ctx, app, c.CurrentVersion, c.CurrentBuild)
	}
	if err != nil {
		c.Reason = err.Error()
		return c, nil
	}
	c.Evidence.InstalledPayloadSignature = "verified"
	c.CanInstall = true
	return c, nil
}

// The already installed, running Developer ID app is the trust root. Both the
// CLI location and its actual parent process must belong to that same bundle.
func (u selfUpdater) currentTeam(ctx context.Context, app string) (string, error) {
	self, e := u.executable()
	if e != nil {
		return "", errors.New("self_update_bundled_worker_required")
	}
	self, e = filepath.EvalSymlinks(self)
	if e != nil || self != filepath.Join(app, "Contents/Resources/keepupdated") {
		return "", errors.New("self_update_bundled_worker_required")
	}
	live, e := inspectApplication(ctx, u.runner, app)
	if e != nil || len(live) != 1 || live[0].PID != u.parentPID() || live[0].Path != app || live[0].Identity != selfBundleID || live[0].Executable != filepath.Join(app, "Contents/MacOS/KeepUpdatedDesktop") || live[0].LaunchedAt == "" {
		return "", errors.New("self_update_instance_unverified")
	}
	team, e := verifyApplicationSignature(ctx, u.runner, app, selfBundleID, "")
	if e != nil {
		return "", errors.New("self_update_current_signature_unverified")
	}
	return team, nil
}
func (u selfUpdater) verifySignedBundle(ctx context.Context, app, version, build string) error {
	if !regexp.MustCompile(`^[A-Z0-9]{10}$`).MatchString(u.team) {
		return errors.New("self_update_current_signature_unverified")
	}
	info, err := readApplicationInfo(ctx, u.runner, app)
	if err != nil || info.ID != selfBundleID || info.Executable != "KeepUpdatedDesktop" || info.Version != version || info.Build != build {
		return errors.New("self_update_identity_mismatch")
	}
	if _, err = verifyApplicationSignature(ctx, u.runner, app, selfBundleID, u.team); err != nil {
		return err
	}
	osVersion, err := appCommand(ctx, u.runner, "/usr/bin/sw_vers", "-productVersion")
	if err != nil || !applicationArchitecture(app, info.Executable, "arm64") || !applicationOSCompatible(strings.TrimSpace(string(osVersion)), info.Minimum) || !applicationOSCompatible(strings.TrimSpace(string(osVersion)), info.MinimumByArch["arm64"]) {
		return errors.New("self_update_incompatible")
	}
	if _, err = appCommand(ctx, u.runner, "/usr/sbin/spctl", "--assess", "--type", "execute", app); err != nil {
		return errors.New("self_update_gatekeeper_rejected")
	}
	return nil
}
func (u selfUpdater) download(ctx context.Context, a SelfUpdateAsset) ([]byte, error) {
	b, _, err := u.get(ctx, a.URL, a.Size, true)
	sum := sha256.Sum256(b)
	if err != nil || int64(len(b)) != a.Size || "sha256:"+hex.EncodeToString(sum[:]) != a.Digest {
		return nil, errors.New("self_update_download_verification_failed")
	}
	return b, nil
}
func sameSelfRelease(a, b SelfUpdateCheck) bool {
	return a.TeamID == b.TeamID && a.Schema == b.Schema && a.Path == b.Path && a.CurrentVersion == b.CurrentVersion && a.CurrentBuild == b.CurrentBuild && a.TargetVersion == b.TargetVersion && a.CanInstall && b.CanInstall && reflect.DeepEqual(a.Release, b.Release) && reflect.DeepEqual(a.Archive, b.Archive) && reflect.DeepEqual(a.Checksums, b.Checksums)
}
func selfError(e error) error {
	if e == nil {
		return nil
	}
	if strings.HasPrefix(e.Error(), "self_update_") || strings.HasPrefix(e.Error(), "application_") {
		return e
	}
	return errors.New("self_update_failed")
}

func validSelfVersion(v string) bool {
	if !releaseVersion.MatchString(v) {
		return false
	}
	for _, part := range strings.Split(v, ".") {
		if _, e := strconv.ParseUint(part, 10, 64); e != nil {
			return false
		}
	}
	return true
}
