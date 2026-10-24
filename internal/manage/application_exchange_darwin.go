package manage

import (
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

// Darwin renameatx_np with RENAME_SWAP exchanges two existing directories in
// one filesystem operation. The SDK declares syscall 488 and flag 0x2; using
// the syscall keeps the CLI CGO-free. Unsupported volumes fail without fallback
// to a two-rename sequence that could leave the application path missing.
func exchangeApplications(a, b string) error {
	ad, e := syscall.Open(filepath.Dir(a), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return e
	}
	defer syscall.Close(ad)
	bd, e := syscall.Open(filepath.Dir(b), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return e
	}
	defer syscall.Close(bd)
	ap, e := syscall.BytePtrFromString(filepath.Base(a))
	if e != nil {
		return e
	}
	bp, e := syscall.BytePtrFromString(filepath.Base(b))
	if e != nil {
		return e
	}
	_, _, errno := syscall.Syscall6(488, uintptr(ad), uintptr(unsafe.Pointer(ap)), uintptr(bd), uintptr(unsafe.Pointer(bp)), 0x2, 0)
	runtime.KeepAlive(ap)
	runtime.KeepAlive(bp)
	if errno != 0 {
		return errno
	}
	return nil
}
