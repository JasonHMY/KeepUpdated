package manage

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const RecentOperationSchema = "keepupdated/recent-operation/v1"
const recentOperationFile = "recent-operation.json"
const recentOperationLimit = 8 << 20

// RecentOperation contains observations only, never an executable plan or consent.
type RecentOperation struct {
	Schema    string       `json:"schema"`
	Operation *ApplyResult `json:"operation"`
}

func ActionStateDir(home string) string {
	return filepath.Join(home, "Library/Application Support/KeepUpdated/actions")
}

// ReadRecentOperation is read-only, including when no record exists. An unfinished
// record says nothing about whether a manager or helper is still running.
func ReadRecentOperation(dir string) (RecentOperation, error) {
	out := RecentOperation{Schema: RecentOperationSchema}
	if err := operationDirectory(dir, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, err
	}
	data, err := readOperationFile(filepath.Join(dir, recentOperationFile))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out = RecentOperation{}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&out); err != nil {
		return RecentOperation{}, errors.New("saved operation record is invalid")
	}
	var extra any
	if d.Decode(&extra) != io.EOF || out.Schema != RecentOperationSchema || out.Operation == nil || validateOperation(*out.Operation) != nil {
		return RecentOperation{}, errors.New("saved operation record is invalid or uses an unsupported version")
	}
	return out, nil
}

func validateOperation(r ApplyResult) error {
	invalid := errors.New("invalid operation record")
	id, err := hex.DecodeString(r.PlanID)
	if err != nil || len(id) != 32 || hex.EncodeToString(id) != r.PlanID || r.Schema != ApplySchema || (r.Action != "update" && r.Action != "uninstall") || (r.State != "in_progress" && r.State != "finished") || len(r.OriginalIDs) == 0 || len(r.OriginalIDs) > 10000 || len(r.Results) != len(r.OriginalIDs) {
		return invalid
	}
	started, err := time.Parse(time.RFC3339Nano, r.StartedAt)
	if err != nil {
		return invalid
	}
	if r.State == "finished" {
		finished, err := time.Parse(time.RFC3339Nano, r.FinishedAt)
		if err != nil || finished.Before(started) {
			return invalid
		}
	} else if r.FinishedAt != "" {
		return invalid
	}
	seen := map[string]bool{}
	for i, id := range r.OriginalIDs {
		if id == "" || len(id) > 4096 || seen[id] || r.Results[i].ID != id || r.Results[i].Status == "" {
			return invalid
		}
		seen[id] = true
	}
	return nil
}

// Reject symlinks in every component before creating anything. Ancestors may be
// shared system directories; the action directory itself must be user-private.
func operationDirectory(dir string, create bool) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || dir == "/" {
		return errors.New("invalid operation directory")
	}
	parent := filepath.Dir(dir)
	if parent != "/" {
		if err := operationAncestors(parent, create); err != nil {
			return err
		}
	}
	st, err := os.Lstat(dir)
	if os.IsNotExist(err) && create {
		if err = os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		if err = syncSkillDir(parent); err != nil {
			return err
		}
		st, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	if !privateOperationFile(st, true) {
		return errors.New("operation directory must be private, owned and not a symbolic link")
	}
	return nil
}

func operationAncestors(dir string, create bool) error {
	if dir == "/" {
		return nil
	}
	if err := operationAncestors(filepath.Dir(dir), create); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if os.IsNotExist(err) && create {
		if err = os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		if err = syncSkillDir(filepath.Dir(dir)); err != nil {
			return err
		}
		st, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.IsDir() || (stat.Uid != 0 && stat.Uid != uint32(os.Getuid())) || st.Mode().Perm()&0022 != 0 && !(stat.Uid == 0 && st.Mode()&os.ModeSticky != 0) {
		return errors.New("unsafe operation directory ancestor")
	}
	return nil
}

func privateOperationFile(st os.FileInfo, directory bool) bool {
	stat, ok := st.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid()) && st.Mode().Perm()&0077 == 0 && ((directory && st.IsDir()) || (!directory && st.Mode().IsRegular() && stat.Nlink == 1))
}

func readOperationFile(path string) ([]byte, error) {
	var f *os.File
	for attempt := 0; ; attempt++ {
		var err error
		f, err = os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, err
		}
		st, err := f.Stat()
		if err == nil && attempt < 7 {
			if stat, ok := st.Sys().(*syscall.Stat_t); ok && stat.Nlink == 0 {
				// Atomic replacement can unlink the opened inode before Stat.
				// Reopen instead of relaxing the single-link validation.
				f.Close()
				continue
			}
		}
		if err != nil || !privateOperationFile(st, false) || st.Size() > recentOperationLimit {
			f.Close()
			return nil, errors.New("saved operation must be a bounded private regular file")
		}
		break
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, recentOperationLimit+1))
	if err != nil || len(data) > recentOperationLimit {
		return nil, errors.New("saved operation could not be read within the size limit")
	}
	return data, nil
}

func saveRecentOperation(dir string, r ApplyResult) error {
	if err := validateOperation(r); err != nil {
		return err
	}
	data, err := json.Marshal(RecentOperation{Schema: RecentOperationSchema, Operation: &r})
	if err != nil || len(data) > recentOperationLimit {
		return errors.New("operation record exceeds the size limit")
	}
	if err = operationDirectory(dir, false); err != nil {
		return err
	}
	path := filepath.Join(dir, recentOperationFile)
	if st, err := os.Lstat(path); err == nil {
		if !privateOperationFile(st, false) {
			return errors.New("unsafe saved operation file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// The containing directory is private. Recheck its identity before rename so
	// moving it during serialization cannot redirect the final replacement.
	before, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".recent-operation-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	after, err := os.Lstat(dir)
	if err != nil || !os.SameFile(before, after) {
		return errors.New("operation directory changed")
	}
	if err = operationDirectory(dir, false); err != nil {
		return err
	}
	if st, err := os.Lstat(path); err == nil && !privateOperationFile(st, false) {
		return errors.New("unsafe saved operation file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	return syncSkillDir(dir)
}

// Providers call this before changing content, once their recovery location is
// known. A failed checkpoint must propagate before the installation is touched.
func recordRecovery(o Options, backup, recovery string) error {
	if o.recordRecovery == nil {
		return nil
	}
	return o.recordRecovery(backup, recovery)
}

func recoveryLocationExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Lstat(path)
	return err == nil
}
