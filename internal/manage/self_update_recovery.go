package manage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
)

// A witness is not an outcome: inspection must also verify both retained trees.
type SelfUpdateExchange struct {
	ID        string                      `json:"id"`
	Target    string                      `json:"target"`
	Original  SelfUpdateDirectoryIdentity `json:"original"`
	Candidate SelfUpdateDirectoryIdentity `json:"candidate"`
}

type SelfUpdateInspection struct {
	WorkerStopped bool   `json:"worker_stopped"`
	Schema        string `json:"schema"`
	ID            string `json:"id"`
	Target        string `json:"target"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
	ReopenStatus  string `json:"reopen_status"`
}

func selfUpdateTargetLockPath(target string) string {
	sum := sha256.Sum256([]byte(target))
	return filepath.Join(filepath.Dir(target), ".KeepUpdated-self-lock-"+hex.EncodeToString(sum[:8]))
}

// InspectSelfUpdate never executes a saved plan, even if it has not expired.
func InspectSelfUpdate(ctx context.Context, path, id, target string) SelfUpdateInspection {
	return productionSelfUpdater().inspect(ctx, path, id, target)
}

func (u selfUpdater) inspect(ctx context.Context, path, id, target string) SelfUpdateInspection {
	result := SelfUpdateInspection{Schema: SelfUpdateSchema, ID: id, Target: target, Status: "unknown", ReopenStatus: "not_verified"}
	p, err := readSelfUpdatePlan(path, id, true)
	if err != nil || p.Check.Path != target || !p.Check.CanInstall || p.Check.Schema != SelfUpdateSchema || p.OriginalSHA256 == p.CandidateSHA256 {
		result.Reason = "self_update_inspection_plan_unverified"
		return result
	}
	u.team = p.Check.TeamID
	// Repeat the entire observation. A lock or changing evidence means no stable outcome.
	var previous selfRecoverySnapshot
	for pass := 0; pass < 2; pass++ {
		snapshot, busy, err := u.selfRecoverySnapshot(ctx, p)
		if busy {
			result.Status = "busy"
			result.Reason = "self_update_worker_active"
			return result
		}
		if p.RecoveryVersion != 1 || err != nil {
			result.Reason = "self_update_inspection_unverified"
			return result
		}
		if pass == 1 && !reflect.DeepEqual(snapshot, previous) {
			result.Reason = "self_update_inspection_changed"
			return result
		}
		previous = snapshot
	}
	fresh, err := readSelfUpdatePlan(path, id, true)
	if err != nil || !reflect.DeepEqual(fresh, p) {
		result.Reason = "self_update_inspection_changed"
		return result
	}
	result.Status = previous.outcome
	_, result.WorkerStopped = previous.records[filepath.Join(p.Directory, "worker.lock")]
	// Reopening remains historical evidence, never inferred from installation.
	if previous.state.ReopenStatus == "reopened" && previous.state.Status == "reopened" && previous.state.Verification == "verified" && previous.state.Evidence != nil && previous.state.Evidence.Reopen == "verified" && result.Status == "candidate_installed" {
		result.ReopenStatus = "recorded_reopened"
	}
	return result
}

type selfRecoverySnapshot struct {
	outcome    string
	state      SelfUpdateState
	records    map[string]string
	trees      map[string]string
	identities map[string]SelfUpdateDirectoryIdentity
}

func (u selfUpdater) selfRecoverySnapshot(ctx context.Context, p SelfUpdatePlan) (s selfRecoverySnapshot, busy bool, err error) {
	s.records = map[string]string{}
	s.trees = map[string]string{}
	s.identities = map[string]SelfUpdateDirectoryIdentity{}
	fail := errors.New("self_update_inspection_unverified")
	for _, d := range []struct {
		path     string
		identity SelfUpdateDirectoryIdentity
	}{
		{p.Directory, p.DirectoryIdentity}, {filepath.Join(p.Directory, "stage"), p.StageIdentity},
	} {
		identity, e := selfDirectoryIdentity(d.path)
		if e != nil || identity != d.identity || ownedPath(d.path, false) != nil {
			return s, false, fail
		}
		s.identities[d.path] = identity
	}
	for _, lock := range []string{filepath.Join(p.Directory, "worker.lock"), selfUpdateTargetLockPath(p.Check.Path)} {
		data, e := selfRecoveryFile(lock, true)
		if e != nil {
			return s, false, fail
		}
		if data != nil {
			s.records[lock] = string(data)
			active, e := selfUpdateLockActive(lock)
			if e != nil {
				return s, false, fail
			}
			if active {
				return s, true, nil
			}
		}
	}
	if _, started := s.records[filepath.Join(p.Directory, "worker.lock")]; !started {
		if _, e := os.Lstat(filepath.Join(p.Directory, "handoff.lock")); !os.IsNotExist(e) {
			return s, false, fail
		}
	}
	for _, name := range []string{"plan.json", "status.json", "exchanged.json", "handoff.lock", "ready.json", "exit-approved.json", "worker.json"} {
		data, e := selfRecoveryFile(filepath.Join(p.Directory, name), name != "plan.json")
		if e != nil {
			return s, false, fail
		}
		if data != nil {
			s.records[name] = string(data)
		}
	}
	if data, ok := s.records["status.json"]; ok {
		if json.Unmarshal([]byte(data), &s.state) != nil || s.state.Schema != SelfUpdateSchema || s.state.ID != p.ID || s.state.Target != p.Check.Path || s.state.OriginalSHA256 != p.OriginalSHA256 || s.state.CandidateSHA256 != p.CandidateSHA256 {
			return s, false, fail
		}
	}
	if _, claimed := s.records[filepath.Join(p.Directory, "worker.lock")]; claimed {
		var worker SelfUpdateState
		if json.Unmarshal([]byte(s.records["worker.json"]), &worker) != nil || worker.Schema != SelfUpdateSchema || worker.ID != p.ID || worker.Target != p.Check.Path || worker.Status != "claimed" {
			return s, false, fail
		}
	}
	if data, exists := s.records["exchanged.json"]; exists {
		var witness SelfUpdateExchange
		if json.Unmarshal([]byte(data), &witness) != nil || witness != (SelfUpdateExchange{p.ID, p.Check.Path, p.TargetIdentity, p.CandidateIdentity}) {
			return s, false, fail
		}
	}
	if data, exists := s.records["ready.json"]; exists {
		var ready SelfUpdateState
		if json.Unmarshal([]byte(data), &ready) != nil || ready.Schema != SelfUpdateSchema || ready.ID != p.ID || ready.Target != p.Check.Path || ready.Status != "waiting_for_exit" {
			return s, false, fail
		}
	}
	if data, exists := s.records["exit-approved.json"]; exists {
		var ack struct {
			ID string `json:"id"`
		}
		if json.Unmarshal([]byte(data), &ack) != nil || ack.ID != p.ID {
			return s, false, fail
		}
	}
	backup := filepath.Join(p.Directory, "original.app")
	for _, path := range []string{p.Check.Path, p.Staged, backup} {
		_, e := os.Lstat(path)
		if os.IsNotExist(e) && path != p.Check.Path {
			continue
		}
		identity, e := selfDirectoryIdentity(path)
		if e != nil || ownedPath(path, false) != nil {
			return s, false, fail
		}
		digest, e := applicationTreeHash(ctx, path)
		if e != nil {
			return s, false, fail
		}
		s.identities[path], s.trees[path] = identity, digest
	}
	matches := func(path string, identity SelfUpdateDirectoryIdentity, hash string) bool {
		return identity.Inode != 0 && s.identities[path] == identity && s.trees[path] == hash
	}
	original, candidate := "", ""
	switch {
	case len(s.trees) == 2 && matches(p.Check.Path, p.CandidateIdentity, p.CandidateSHA256) && matches(backup, p.TargetIdentity, p.OriginalSHA256):
		s.outcome, original, candidate = "candidate_installed", backup, p.Check.Path
	case len(s.trees) == 2 && matches(p.Check.Path, p.TargetIdentity, p.OriginalSHA256) && matches(p.Staged, p.CandidateIdentity, p.CandidateSHA256):
		if _, exists := s.records["exchanged.json"]; exists {
			return s, false, fail
		}
		s.outcome, original, candidate = "not_replaced", p.Check.Path, p.Staged
	case len(s.trees) == 2 && matches(p.Check.Path, p.TargetIdentity, p.OriginalSHA256) && matches(backup, p.CandidateIdentity, p.CandidateSHA256):
		// Before the first swap and after restoration share this layout. Only a
		// matching post-exchange witness can distinguish restoration; otherwise unknown.
		var witness SelfUpdateExchange
		if json.Unmarshal([]byte(s.records["exchanged.json"]), &witness) != nil || witness != (SelfUpdateExchange{p.ID, p.Check.Path, p.TargetIdentity, p.CandidateIdentity}) {
			return s, false, fail
		}
		s.outcome, original, candidate = "original_restored", p.Check.Path, backup
	default:
		return s, false, fail
	}
	if err = u.verifySignedBundle(ctx, original, p.Check.CurrentVersion, p.Check.CurrentBuild); err != nil {
		return s, false, err
	}
	if err = u.verifySignedBundle(ctx, candidate, p.Check.TargetVersion, p.TargetBuild); err != nil {
		return s, false, err
	}
	// Rehash after the external static verifiers as well as on the next pass.
	for path, digest := range s.trees {
		current, e := applicationTreeHash(ctx, path)
		identity, ie := selfDirectoryIdentity(path)
		if e != nil || ie != nil || current != digest || identity != s.identities[path] {
			return s, false, fail
		}
	}
	for _, lock := range []string{filepath.Join(p.Directory, "worker.lock"), selfUpdateTargetLockPath(p.Check.Path)} {
		if _, e := os.Lstat(lock); e == nil {
			active, e := selfUpdateLockActive(lock)
			if e != nil {
				return s, false, fail
			}
			if active {
				return s, true, nil
			}
		} else if !os.IsNotExist(e) {
			return s, false, fail
		}
	}
	return s, false, nil
}

// Bound, owned regular files only. Optional absence is distinct from an empty file.
func selfRecoveryFile(path string, optional bool) ([]byte, error) {
	st, err := os.Lstat(path)
	if optional && os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 4<<20 || ownedPath(path, false) != nil {
		return nil, errors.New("self_update_record_unverified")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path {
		return nil, errors.New("self_update_record_unverified")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	current, err := f.Stat()
	if err != nil || !os.SameFile(st, current) {
		return nil, errors.New("self_update_record_changed")
	}
	data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return nil, errors.New("self_update_record_unverified")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(st, after) || st.ModTime() != after.ModTime() || st.Size() != after.Size() {
		return nil, errors.New("self_update_record_changed")
	}
	return data, nil
}
