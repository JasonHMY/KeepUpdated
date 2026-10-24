// Package localmcp implements the bounded local Agent interface.
package localmcp

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const fileLimit = 16 << 20

type store struct{ dir string }

func handle() (string, error) {
	b := make([]byte, 32)
	_, e := rand.Read(b)
	return hex.EncodeToString(b), e
}
func validHandle(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func private(st os.FileInfo, dir bool) bool {
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && s.Uid == uint32(os.Getuid()) && st.Mode().Perm()&0077 == 0 && ((dir && st.IsDir()) || (!dir && st.Mode().IsRegular() && s.Nlink == 1))
}
func safeDir(path string, leaf bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(strings.Split(path, "/")) > 64 {
		return errors.New("invalid private state directory")
	}
	if path == "/" {
		if leaf {
			return errors.New("root is not a private state directory")
		}
		return nil
	}
	if e := safeDir(filepath.Dir(path), false); e != nil {
		return e
	}
	st, e := os.Lstat(path)
	if os.IsNotExist(e) {
		if e = os.Mkdir(path, 0700); e != nil && !os.IsExist(e) {
			return e
		}
		st, e = os.Lstat(path)
	}
	if e != nil {
		return e
	}
	if leaf {
		if !private(st, true) {
			return errors.New("state directory must be private and owned")
		}
		return nil
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.IsDir() || (s.Uid != 0 && s.Uid != uint32(os.Getuid())) || (st.Mode().Perm()&0022 != 0 && !(s.Uid == 0 && st.Mode()&os.ModeSticky != 0)) {
		return errors.New("unsafe state ancestor")
	}
	return nil
}
func (s store) path(id, kind string) (string, error) {
	if !validHandle(id) {
		return "", errors.New("invalid opaque handle")
	}
	if e := safeDir(s.dir, true); e != nil {
		return "", e
	}
	return filepath.Join(s.dir, id+"."+kind), nil
}
func syncDir(dir string) error {
	f, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func (s store) write(id, kind string, v any, exclusive bool) error {
	path, e := s.path(id, kind)
	if e != nil {
		return e
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b) > fileLimit {
		return errors.New("state exceeds size limit")
	}
	before, e := os.Lstat(s.dir)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(s.dir, ".mcp-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	after, e := os.Lstat(s.dir)
	if e != nil || !os.SameFile(before, after) {
		return errors.New("state directory changed")
	}
	if e = safeDir(s.dir, true); e != nil {
		return e
	}
	if exclusive {
		if e = os.Link(f.Name(), path); e != nil {
			return e
		}
		if e = os.Remove(f.Name()); e != nil {
			return e
		}
	} else {
		st, err := os.Lstat(path)
		if err == nil && !private(st, false) {
			return errors.New("unsafe state file")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if e = os.Rename(f.Name(), path); e != nil {
			return e
		}
	}
	return syncDir(s.dir)
}
func (s store) read(id, kind string, v any) error {
	path, e := s.path(id, kind)
	if e != nil {
		return e
	}
	var f *os.File
	for attempt := 0; ; attempt++ {
		f, e = os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return e
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
		if err != nil || !private(st, false) || st.Size() > fileLimit {
			f.Close()
			return errors.New("unsafe or oversized state file")
		}
		break
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, fileLimit+1))
	if e != nil {
		return e
	}
	if len(b) > fileLimit {
		return errors.New("state exceeds size limit")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid trailing state")
	}
	return nil
}
func (s store) claim(id, kind string) (*os.File, error) {
	p, e := s.path(id, kind)
	if e != nil {
		return nil, e
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e == nil {
		e = f.Sync()
	}
	if e == nil {
		e = syncDir(s.dir)
	}
	if e != nil {
		f.Close()
		return nil, e
	}
	return f, nil
}
func (s store) active(id, kind string) bool {
	p, e := s.path(id, kind)
	if e != nil {
		return false
	}
	f, e := os.OpenFile(p, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return false
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !private(st, false) {
		return false
	}
	e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if e == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false
	}
	return e == syscall.EWOULDBLOCK || e == syscall.EAGAIN
}

// A reusable advisory lock serializes identical previews without creating orphan
// plans. Durable decision/worker claims use claim instead and are never reused.
func (s store) lock(id, kind string) (*os.File, error) {
	p, e := s.path(id, kind)
	if e != nil {
		return nil, e
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if e != nil {
		return nil, e
	}
	st, e := f.Stat()
	if e != nil || !private(st, false) {
		f.Close()
		return nil, errors.New("unsafe preview lock")
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); e != nil {
		f.Close()
		return nil, e
	}
	return f, nil
}
