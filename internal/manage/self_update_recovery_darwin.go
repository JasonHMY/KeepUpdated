package manage

import (
	"os"
	"syscall"
)

// Darwin F_GETLK observes BSD flock locks too (PID -1), without acquiring one.
func selfUpdateLockActive(path string) (bool, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false, err
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if err = syscall.Fstat(fd, &st); err != nil {
		return false, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Mode&0077 != 0 {
		return false, syscall.EINVAL
	}
	before, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	named, ok := before.Sys().(*syscall.Stat_t)
	if !ok || named.Dev != st.Dev || named.Ino != st.Ino {
		return false, syscall.EINVAL
	}
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err = syscall.FcntlFlock(uintptr(fd), syscall.F_GETLK, &lock); err != nil {
		return false, err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		return false, syscall.EINVAL
	}
	return lock.Type != syscall.F_UNLCK, nil
}
