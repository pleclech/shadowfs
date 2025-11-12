//go:build darwin
// +build darwin

package xattr

// UNTESTED: macOS implementation - requires testing on macOS hardware

import (
	"os"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"golang.org/x/sys/unix"
)

// Get retrieves an extended attribute from the given path
// This is the macOS-specific implementation using unix.Getxattr
func Get(path string, attr *XAttr) (exists bool, errno syscall.Errno) {
	// macOS getxattr returns the size needed if buffer is too small
	// First try with our buffer size
	buf := ToBytes(attr)
	n, err := unix.Getxattr(path, Name, buf)
	
	// On macOS, ENOATTR means attribute doesn't exist (equivalent to Linux ENODATA)
	if err == unix.ENOATTR || os.IsNotExist(err) {
		return false, 0
	}

	if err != nil {
		return false, fs.ToErrno(err)
	}

	// If we got data, check if it's valid
	if n > 0 && n <= len(buf) {
		return true, 0
	}

	return false, 0
}

// Set sets an extended attribute on the given path
// This is the macOS-specific implementation using unix.Setxattr
func Set(path string, attr *XAttr) syscall.Errno {
	// macOS setxattr flags: 0 = create or replace
	return fs.ToErrno(unix.Setxattr(path, Name, ToBytes(attr), 0))
}

// Remove removes an extended attribute from the given path
// This is the macOS-specific implementation using unix.Removexattr
func Remove(path string) syscall.Errno {
	return fs.ToErrno(unix.Removexattr(path, Name))
}

