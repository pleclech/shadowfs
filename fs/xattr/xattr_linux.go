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
	buf := ToBytes(attr)
	n, err := syscall.Getxattr(path, Name, buf)

	// ENODATA means the xattr doesn't exist
	if err == syscall.ENODATA {
		return false, 0
	}

	// File doesn't exist
	if os.IsNotExist(err) {
		return false, 0
	}

	// ERANGE means buffer is too small - this shouldn't happen with our fixed-size buffer
	// but handle it gracefully
	if err == syscall.ERANGE {
		// Try again with a larger buffer
		// Get the required size first
		size, err2 := syscall.Getxattr(path, Name, nil)
		if err2 != nil {
			return false, fs.ToErrno(err2)
		}
		if size > len(buf) {
			// Buffer is indeed too small - this is unexpected
			return false, syscall.ERANGE
		}
		// Retry with original buffer
		n, err = syscall.Getxattr(path, Name, buf)
		if err != nil {
			return false, fs.ToErrno(err)
		}
	}

	// Other errors
	if err != nil {
		return false, fs.ToErrno(err)
	}

	// Success - xattr exists (no error from syscall.Getxattr means it exists)
	// Verify we got the expected amount of data
	// n should be exactly Size bytes for our XAttr struct
	if n == Size {
		return true, 0
	}

	// If n is between 0 and Size, it's partial data (corrupted or old format)
	// Zero out the rest and return exists=true (xattr exists, just incomplete)
	if n > 0 && n < Size {
		// Partial data - zero out the rest
		for i := n; i < len(buf); i++ {
			buf[i] = 0
		}
		return true, 0
	}

	// If n == 0, syscall.Getxattr succeeded but returned 0 bytes
	// This means the xattr exists but is empty (shouldn't happen with our struct)
	// However, if Getxattr succeeds with no error, the xattr EXISTS
	// So we return exists=true, but the attr will be zero-initialized
	if n == 0 {
		// Xattr exists but is empty - zero initialize the struct
		for i := range buf {
			buf[i] = 0
		}
		return true, 0
	}

	// n > Size shouldn't happen (buffer should be large enough)
	// But if it does, treat as error
	return false, syscall.EIO
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

// List lists all extended attribute names for a given path
// This is useful for debugging to verify xattrs are set correctly
func List(path string) ([]string, syscall.Errno) {
	// First, get the size needed
	size, err := syscall.Listxattr(path, nil)
	if err != nil {
		if err == syscall.ENODATA {
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
	n, err := syscall.Listxattr(path, buf)
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

