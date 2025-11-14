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

// List lists all extended attribute names for a given path
// This is the macOS-specific implementation using unix.Listxattr
func List(path string) ([]string, syscall.Errno) {
	// First, get the size needed
	size, err := unix.Listxattr(path, nil)
	if err != nil {
		if err == unix.ENOATTR {
			// No xattrs
			return []string{}, 0
		}
		if os.IsNotExist(err) {
			return nil, 0
		}
		return nil, fs.ToErrno(err)
	}

	if size == 0 {
		return []string{}, 0
	}

	// Allocate buffer and get the actual list
	buf := make([]byte, size)
	n, err := unix.Listxattr(path, buf)
	if err != nil {
		return nil, fs.ToErrno(err)
	}

	// Parse null-separated list
	var attrs []string
	start := 0
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			if start < i {
				attrs = append(attrs, string(buf[start:i]))
			}
			start = i + 1
		}
	}
	if start < n {
		attrs = append(attrs, string(buf[start:n]))
	}

	return attrs, 0
}

