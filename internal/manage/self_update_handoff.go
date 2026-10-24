package manage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
)

type SelfUpdateDirectoryIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}
type SelfUpdatePlan struct {
	RecoveryVersion int `json:"recovery_version,omitempty"`
	// Preparation evidence stays separate from the stable release check.
	Evidence          *SelfUpdateEvidence         `json:"evidence,omitempty"`
	DirectoryIdentity SelfUpdateDirectoryIdentity `json:"directory_identity"`
	StageIdentity     SelfUpdateDirectoryIdentity `json:"stage_identity"`
	CandidateIdentity SelfUpdateDirectoryIdentity `json:"candidate_identity"`
	TargetIdentity    SelfUpdateDirectoryIdentity `json:"target_identity"`
	Schema            string                      `json:"schema"`
	ID                string                      `json:"id"`
	Check             SelfUpdateCheck             `json:"check"`
	Instance          ApplicationInstance         `json:"instance"`
	OriginalSHA256    string                      `json:"original_sha256"`
	CandidateSHA256   string                      `json:"candidate_sha256"`
	TargetBuild       string                      `json:"target_build"`
	Directory         string                      `json:"directory"`
	Staged            string                      `json:"staged"`
	WorkerSHA256      string                      `json:"worker_sha256"`
	Expires           time.Time                   `json:"expires"`
}
type SelfUpdateState struct {
	Evidence        *SelfUpdateEvidence `json:"evidence,omitempty"`
	Changed         bool                `json:"changed,omitempty"`
	Verification    string              `json:"verification,omitempty"`
	ReopenStatus    string              `json:"reopen_status,omitempty"`
	ReopenReason    string              `json:"reopen_reason,omitempty"`
	Schema          string              `json:"schema"`
	ID              string              `json:"id"`
	Status          string              `json:"status"`
	Reason          string              `json:"reason,omitempty"`
	Backup          string              `json:"backup,omitempty"`
	Target          string              `json:"target"`
	OriginalSHA256  string              `json:"original_sha256"`
	CandidateSHA256 string              `json:"candidate_sha256"`
}

func selfPlanID(p SelfUpdatePlan) string {
	p.ID = ""
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func PrepareSelfUpdate(ctx context.Context, c SelfUpdateCheck, pid int) (SelfUpdatePlan, error) {
	return productionSelfUpdater().prepare(ctx, c, pid)
}
func (u selfUpdater) prepare(ctx context.Context, c SelfUpdateCheck, pid int) (p SelfUpdatePlan, err error) {
	fresh, e := u.check(ctx, c.Path)
	if e != nil || !sameSelfRelease(c, fresh) {
		return p, errors.New("self_update_check_changed")
	}
	u.team = fresh.TeamID
	if e = selfDestination(c.Path); e != nil {
		return p, e
	}
	live, e := inspectApplication(ctx, u.runner, c.Path)
	if e != nil || len(live) != 1 || live[0].PID != pid || live[0].Path != c.Path || live[0].Identity != selfBundleID || live[0].Executable != filepath.Join(c.Path, "Contents/MacOS/KeepUpdatedDesktop") || live[0].LaunchedAt == "" {
		return p, errors.New("self_update_instance_unverified")
	}
	p = SelfUpdatePlan{RecoveryVersion: 1, Schema: SelfUpdateSchema, Check: c, Instance: live[0], Expires: time.Now().UTC().Add(20 * time.Minute)}
	if fresh.Evidence != nil {
		evidence := *fresh.Evidence
		p.Evidence = &evidence
	}
	p.OriginalSHA256, e = applicationTreeHash(ctx, c.Path)
	if e != nil {
		return p, e
	}
	sums, e := u.download(ctx, *c.Checksums)
	if e != nil {
		return p, e
	}
	expected := strings.TrimPrefix(c.Archive.Digest, "sha256:") + "  " + c.Archive.Name
	matches := 0
	for _, line := range strings.Split(string(sums), "\n") {
		if strings.TrimSpace(line) == expected {
			matches++
		}
	}
	if matches != 1 {
		return p, errors.New("self_update_checksum_mismatch")
	}
	p.Evidence.ArchiveLength, p.Evidence.ArchiveHash = "unverified", "unverified"
	data, e := u.download(ctx, *c.Archive)
	if e != nil {
		return p, e
	}
	p.Evidence.ArchiveLength, p.Evidence.ArchiveHash = "verified", "verified"
	p.Directory, e = os.MkdirTemp(filepath.Dir(c.Path), ".KeepUpdated-self-")
	if e != nil {
		return p, errors.New("self_update_staging_unavailable")
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(p.Directory)
		}
	}()
	stage := filepath.Join(p.Directory, "stage")
	if e = os.Mkdir(stage, 0700); e != nil {
		return p, e
	}
	p.Staged, e = extractApplicationZIP(ctx, data, stage)
	if e == nil && filepath.Base(p.Staged) != "KeepUpdated.app" {
		e = errors.New("self_update_identity_mismatch")
	}
	if e != nil {
		return p, e
	}
	info, e := readApplicationInfo(ctx, u.runner, p.Staged)
	if e != nil || !numericApplicationVersion(info.Build) || info.Build == c.CurrentBuild || !applicationOSCompatible(info.Build, c.CurrentBuild) {
		return p, errors.New("self_update_build_not_newer")
	}
	p.TargetBuild = info.Build
	p.Evidence.CandidatePayloadSignature = "unverified"
	if e = u.verifySignedBundle(ctx, p.Staged, c.TargetVersion, p.TargetBuild); e != nil {
		return p, e
	}
	p.Evidence.CandidatePayloadSignature = "verified"
	p.DirectoryIdentity, e = selfDirectoryIdentity(p.Directory)
	if e != nil {
		return p, e
	}
	p.StageIdentity, e = selfDirectoryIdentity(stage)
	if e != nil {
		return p, e
	}
	p.CandidateIdentity, e = selfDirectoryIdentity(p.Staged)
	if e != nil {
		return p, e
	}
	p.TargetIdentity, e = selfDirectoryIdentity(c.Path)
	if e != nil {
		return p, e
	}
	p.CandidateSHA256, e = applicationTreeHash(ctx, p.Staged)
	if e != nil {
		return p, e
	}
	// Copy the authenticated installed CLI, never the newly downloaded executable.
	worker := filepath.Join(c.Path, "Contents/Resources/keepupdated")
	self, e := u.executable()
	if e != nil {
		return p, e
	}
	self, e = filepath.EvalSymlinks(self)
	if e != nil || self != worker {
		return p, errors.New("self_update_bundled_worker_required")
	}
	src, e := os.Open(worker)
	if e != nil {
		return p, e
	}
	defer src.Close()
	dst, e := os.OpenFile(filepath.Join(p.Directory, "worker"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if e != nil {
		return p, e
	}
	_, e = io.Copy(dst, io.LimitReader(src, 128<<20))
	syncErr := dst.Sync()
	closeErr := dst.Close()
	if e != nil || syncErr != nil || closeErr != nil {
		return p, errors.New("self_update_worker_copy_failed")
	}
	p.WorkerSHA256, e = fileHash(filepath.Join(p.Directory, "worker"))
	if e != nil {
		return p, e
	}
	originalWorker, e := fileHash(worker)
	if e != nil || originalWorker != p.WorkerSHA256 {
		return p, errors.New("self_update_worker_copy_failed")
	}
	fresh, e = u.check(ctx, c.Path)
	if e != nil || !sameSelfRelease(c, fresh) {
		return p, errors.New("self_update_release_changed")
	}
	p.ID = selfPlanID(p)
	if e = u.validatePrepared(ctx, p, true); e != nil {
		return p, e
	}
	if e = writeSelfJSON(filepath.Join(p.Directory, "plan.json"), p); e != nil {
		return p, e
	}
	if e = writeSelfState(p, "prepared", "", ""); e != nil {
		return p, e
	}
	return p, nil
}
func selfDestination(path string) error {
	real, e := filepath.EvalSymlinks(path)
	if e != nil || real != path || filepath.Ext(path) != ".app" || strings.Contains(path, ".app/") || ownedPath(path, false) != nil || !applicationParentWritable(path) {
		return errors.New("self_update_destination_not_writable")
	}
	return nil
}
func writeSelfJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path+".new", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, we := f.Write(b)
	se := f.Sync()
	ce := f.Close()
	if we != nil || se != nil || ce != nil {
		return errors.New("self_update_recovery_write_failed")
	}
	if e = os.Rename(path+".new", path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func writeSelfState(p SelfUpdatePlan, status, reason, backup string) error {
	return writeSelfResult(p, SelfUpdateState{Status: status, Reason: reason, Backup: backup})
}
func writeSelfResult(p SelfUpdatePlan, state SelfUpdateState) error {
	if p.Evidence != nil {
		evidence := *p.Evidence
		state.Evidence = &evidence
	} else {
		state.Evidence = &SelfUpdateEvidence{}
	}
	// Only worker readback results establish installation and reopening evidence.
	state.Evidence.InstallationReadback, state.Evidence.Reopen = "", ""
	switch state.Verification {
	case "verified":
		state.Evidence.InstallationReadback = "verified"
	case "unknown":
		state.Evidence.InstallationReadback = "unverified"
	case "unchanged":
		state.Evidence.InstallationReadback = "not_checked"
	}
	switch state.ReopenStatus {
	case "reopened":
		state.Evidence.Reopen = "verified"
	case "pending":
		state.Evidence.Reopen = "not_checked"
	case "not_requested":
		state.Evidence.Reopen = "not_applicable"
	case "":
	default:
		state.Evidence.Reopen = "unverified"
	}
	state.Schema, state.ID, state.Target = SelfUpdateSchema, p.ID, p.Check.Path
	state.OriginalSHA256, state.CandidateSHA256 = p.OriginalSHA256, p.CandidateSHA256
	return writeSelfJSON(filepath.Join(p.Directory, "status.json"), state)
}
func ReadSelfUpdatePlan(path, confirm string) (SelfUpdatePlan, error) {
	return readSelfUpdatePlan(path, confirm, false)
}

// Inspection may read expired plans; execution always uses ReadSelfUpdatePlan.
func readSelfUpdatePlan(path, confirm string, inspect bool) (SelfUpdatePlan, error) {
	var p SelfUpdatePlan
	b, e := selfRecoveryFile(path, false)
	if e != nil || json.Unmarshal(b, &p) != nil || p.ID != confirm || p.ID != selfPlanID(p) || path != filepath.Join(p.Directory, "plan.json") || p.Schema != SelfUpdateSchema || (!inspect && time.Now().After(p.Expires)) {
		return p, errors.New("self_update_plan_unverified")
	}
	real, e := filepath.EvalSymlinks(p.Directory)
	st, e2 := os.Lstat(p.Directory)
	if e != nil || e2 != nil || real != p.Directory || !st.IsDir() || st.Mode().Perm()&0077 != 0 || ownedPath(p.Directory, false) != nil || filepath.Dir(p.Directory) != filepath.Dir(p.Check.Path) || !strings.HasPrefix(filepath.Base(p.Directory), ".KeepUpdated-self-") || p.Staged != filepath.Join(p.Directory, "stage/KeepUpdated.app") {
		return p, errors.New("self_update_plan_unverified")
	}
	return p, nil
}
func (u selfUpdater) validatePrepared(ctx context.Context, p SelfUpdatePlan, running bool) error {
	if p.Schema != SelfUpdateSchema || p.ID != selfPlanID(p) || time.Now().After(p.Expires) || !p.Check.CanInstall || p.Check.Status != "available" {
		return errors.New("self_update_plan_unverified")
	}
	if e := validateSelfStaging(p); e != nil {
		return e
	}
	if e := selfDestination(p.Check.Path); e != nil {
		return e
	}
	for _, bundle := range []struct{ path, version, build, digest string }{{p.Check.Path, p.Check.CurrentVersion, p.Check.CurrentBuild, p.OriginalSHA256}, {p.Staged, p.Check.TargetVersion, p.TargetBuild, p.CandidateSHA256}} {
		hash, e := applicationTreeHash(ctx, bundle.path)
		if e != nil || hash != bundle.digest {
			return errors.New("self_update_content_changed")
		}
		if e = u.verifySignedBundle(ctx, bundle.path, bundle.version, bundle.build); e != nil {
			return e
		}
	}
	worker := filepath.Join(p.Directory, "worker")
	st, statErr := os.Lstat(worker)
	realWorker, resolveErr := filepath.EvalSymlinks(worker)
	if statErr != nil || resolveErr != nil || !st.Mode().IsRegular() || realWorker != worker || st.Mode().Perm()&0077 != 0 {
		return errors.New("self_update_worker_changed")
	}
	hash, e := fileHash(worker)
	if e != nil || hash != p.WorkerSHA256 {
		return errors.New("self_update_worker_changed")
	}
	original, e := fileHash(filepath.Join(p.Check.Path, "Contents/Resources/keepupdated"))
	if e != nil || original != hash {
		return errors.New("self_update_worker_changed")
	}
	live, e := inspectApplication(ctx, u.runner, p.Check.Path)
	if e != nil {
		return e
	}
	if running && (len(live) != 1 || !reflect.DeepEqual(live[0], p.Instance)) {
		return errors.New("self_update_instance_changed")
	}
	if !running && (len(live) != 0 || !applicationProcessesClosed(ctx, u.runner, p.Check.Path)) {
		return errors.New("self_update_still_running")
	}
	return nil
}

// StartSelfUpdate launches a bounded, user-owned worker outside the bundle. It
// returns only after that worker validates the bound running instance. The app
// then requests its own normal termination; no service or force-quit is used.
func StartSelfUpdate(ctx context.Context, path, confirm string) (SelfUpdateState, error) {
	p, e := ReadSelfUpdatePlan(path, confirm)
	if e != nil {
		return SelfUpdateState{}, e
	}
	u := productionSelfUpdater()
	team, err := u.currentTeam(ctx, p.Check.Path)
	if err != nil || team != p.Check.TeamID {
		return SelfUpdateState{}, errors.New("self_update_current_signature_unverified")
	}
	u.team = team
	if e = u.validatePrepared(ctx, p, true); e != nil {
		return SelfUpdateState{}, e
	}
	lock, e := os.OpenFile(filepath.Join(p.Directory, "handoff.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return SelfUpdateState{}, errors.New("self_update_already_started")
	}
	lock.Close()
	cmd := exec.Command(filepath.Join(p.Directory, "worker"), "self-update", "finish", "--plan", path, "--confirm", confirm)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if e = cmd.Start(); e != nil {
		return SelfUpdateState{}, errors.New("self_update_handoff_failed")
	}
	go func() { _ = cmd.Wait() }()
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return waitSelfUpdateReady(wait, p)
}
func waitSelfUpdateReady(ctx context.Context, p SelfUpdatePlan) (SelfUpdateState, error) {
	for {
		b, e := os.ReadFile(filepath.Join(p.Directory, "ready.json"))
		var state SelfUpdateState
		if e == nil && json.Unmarshal(b, &state) == nil && state.ID == p.ID && state.Status == "waiting_for_exit" {
			return state, nil
		}
		select {
		case <-ctx.Done():
			return SelfUpdateState{}, errors.New("self_update_handoff_not_ready")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func FinishSelfUpdate(ctx context.Context, path, confirm string) error {
	p, e := ReadSelfUpdatePlan(path, confirm)
	if e != nil {
		return e
	}
	self, e := os.Executable()
	if e != nil || self != filepath.Join(p.Directory, "worker") {
		return errors.New("self_update_detached_worker_required")
	}
	u := productionSelfUpdater()
	u.team = p.Check.TeamID
	return u.finish(ctx, p)
}
func (u selfUpdater) finish(ctx context.Context, p SelfUpdatePlan) (failure error) {
	u.team = p.Check.TeamID

	// Only the worker that claims this operation may write its recovery state.
	lock, e := os.OpenFile(filepath.Join(p.Directory, "worker.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return errors.New("self_update_already_started")
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("self_update_target_locked")
	}
	if e = writeSelfJSON(filepath.Join(p.Directory, "worker.json"), SelfUpdateState{Schema: SelfUpdateSchema, ID: p.ID, Target: p.Check.Path, Status: "claimed"}); e != nil {
		return e
	}
	final := SelfUpdateState{Status: "failed", Verification: "unchanged", ReopenStatus: "not_requested"}
	defer func() {
		if failure != nil {
			final.Reason = selfError(failure).Error()
			_ = writeSelfResult(p, final)
		}
	}()
	targetLock, e := lockSelfUpdateTarget(p.Check.Path)
	if e != nil {
		return e
	}
	defer targetLock.Close()
	if e = u.validatePrepared(ctx, p, true); e != nil {
		return e
	}
	freshRelease, e := u.release(ctx, nil)
	if e != nil || !reflect.DeepEqual(p.Check.Release, freshRelease) {
		return errors.New("self_update_release_changed")
	}
	if e = writeSelfState(p, "waiting_for_exit", "", ""); e != nil {
		return e
	}
	if e = writeSelfJSON(filepath.Join(p.Directory, "ready.json"), SelfUpdateState{Schema: SelfUpdateSchema, ID: p.ID, Status: "waiting_for_exit", Target: p.Check.Path, Evidence: p.Evidence}); e != nil {
		return e
	}
	wait, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		live, e := inspectApplication(wait, u.runner, p.Check.Path)
		if e != nil {
			return e
		}
		if len(live) == 0 {
			approved, e := selfExitApproved(p)
			if e != nil || !approved {
				return errors.New("self_update_exit_not_acknowledged")
			}
			break
		}
		if len(live) != 1 || live[0] != p.Instance {
			return errors.New("self_update_instance_changed")
		}
		select {
		case <-wait.Done():
			return errors.New("self_update_exit_timed_out")
		case <-time.After(100 * time.Millisecond):
		}
	}
	if e = u.validatePrepared(ctx, p, false); e != nil {
		return e
	}
	final.Backup = filepath.Join(p.Directory, "original.app")
	if e = writeSelfResult(p, SelfUpdateState{Status: "installing", Verification: "unknown", Backup: final.Backup, ReopenStatus: "not_requested"}); e != nil {
		return e
	}
	// Recheck the actual staging directories immediately before moving any tree.
	if e = validateSelfStaging(p); e != nil {
		return e
	}
	saved, changed, e := replaceApplication(ctx, p.Check.Path, p.Staged, final.Backup, p.OriginalSHA256, p.CandidateSHA256, func(path string) error { return u.verifySignedBundle(ctx, path, p.Check.TargetVersion, p.TargetBuild) }, func(a, b string) error {
		if err := exchangeApplications(a, b); err != nil {
			return err
		}
		// This witness only supports inspection; failure never prevents restoration.
		if identity, err := selfDirectoryIdentity(a); err == nil && identity == p.CandidateIdentity {
			_ = writeSelfJSON(filepath.Join(p.Directory, "exchanged.json"), SelfUpdateExchange{ID: p.ID, Target: p.Check.Path, Original: p.TargetIdentity, Candidate: p.CandidateIdentity})
		}
		return nil
	})
	final.Changed = changed
	if saved != "" {
		final.Backup = saved
	}
	if e != nil {
		if changed {
			final.Status = "verification_unknown"
			final.Verification = "unknown"
		}
		return e
	}
	final.Status, final.Verification, final.ReopenStatus = "installed", "verified", "pending"
	if e = writeSelfResult(p, final); e != nil {
		return e
	}
	target := ApplicationLifecycleTarget{ApplicationBinding: ApplicationBinding{ID: "self", Path: p.Check.Path, Identity: selfBundleID, Version: p.Check.CurrentVersion}, RealPath: p.Check.Path, Reopen: true}
	installed := audit.Item{ID: "self", Kind: "application", Path: p.Check.Path, Identity: selfBundleID, Version: p.Check.TargetVersion, Build: p.TargetBuild, Health: audit.Result{Status: "ok"}}
	status, reason := ReopenApplication(ctx, target, installed, u.runner)
	final.ReopenStatus, final.ReopenReason = status, reason
	if status != "reopened" {
		return errors.New("self_update_reopen_failed")
	}
	final.Status = "reopened"
	return writeSelfResult(p, final)
}

// Keep the lock file in place: unlinking an inode while another worker holds it
// would let a later worker obtain a different lock for the same destination.
func lockSelfUpdateTarget(target string) (*os.File, error) {
	path := selfUpdateTargetLockPath(target)
	fd, e := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, errors.New("self_update_target_locked")
	}
	f := os.NewFile(uintptr(fd), path)
	if ownedPath(path, false) != nil || syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, errors.New("self_update_target_locked")
	}
	return f, nil
}

// The UI writes this only after receiving and validating the worker's ready
// reply. Losing the CLI response cannot leave a worker armed to install later.
func selfExitApproved(p SelfUpdatePlan) (bool, error) {
	path := filepath.Join(p.Directory, "exit-approved.json")
	st, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return false, nil
	}
	if e != nil || !st.Mode().IsRegular() || st.Size() > 1024 || ownedPath(path, false) != nil {
		return false, errors.New("self_update_exit_not_acknowledged")
	}
	b, e := os.ReadFile(path)
	var ack struct {
		ID string `json:"id"`
	}
	if e != nil || json.Unmarshal(b, &ack) != nil || ack.ID != p.ID {
		return false, errors.New("self_update_exit_not_acknowledged")
	}
	return true, nil
}

func selfDirectoryIdentity(path string) (SelfUpdateDirectoryIdentity, error) {
	var identity SelfUpdateDirectoryIdentity
	st, e := os.Lstat(path)
	real, resolveErr := filepath.EvalSymlinks(path)
	if e != nil || resolveErr != nil || real != path || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return identity, errors.New("self_update_staging_changed")
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return identity, errors.New("self_update_staging_changed")
	}
	return SelfUpdateDirectoryIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}
func validateSelfStaging(p SelfUpdatePlan) error {
	for _, d := range []struct {
		path     string
		identity SelfUpdateDirectoryIdentity
	}{
		{p.Directory, p.DirectoryIdentity}, {filepath.Join(p.Directory, "stage"), p.StageIdentity}, {p.Staged, p.CandidateIdentity}, {p.Check.Path, p.TargetIdentity},
	} {
		current, e := selfDirectoryIdentity(d.path)
		if e != nil || current != d.identity {
			return errors.New("self_update_staging_changed")
		}
	}
	return nil
}
