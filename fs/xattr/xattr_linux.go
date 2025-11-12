//go:build linux
// +build linux

package xattr

import (
	"os"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
)

// Get retrieves an extended attribute from the given path
// This is the Linux-specific implementation using syscall.Getxattr
func Get(path string, attr *XAttr) (exists bool, errno syscall.Errno) {
	_, err := syscall.Getxattr(path, Name, ToBytes(attr))

	// skip if xattr not found
	if os.IsNotExist(err) {
		return false, 0
	}

	if err != nil && err != syscall.ENODATA {
		return false, fs.ToErrno(err)
	}

	return true, 0
}

// Set sets an extended attribute on the given path
// This is the Linux-specific implementation using syscall.Setxattr
func Set(path string, attr *XAttr) syscall.Errno {
	return fs.ToErrno(syscall.Setxattr(path, Name, ToBytes(attr), 0))
}

// Remove removes an extended attribute from the given path
// This is the Linux-specific implementation using syscall.Removexattr
func Remove(path string) syscall.Errno {
	return fs.ToErrno(syscall.Removexattr(path, Name))
}

