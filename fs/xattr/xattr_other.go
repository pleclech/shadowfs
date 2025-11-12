//go:build !linux && !darwin
// +build !linux,!darwin

package xattr

import (
	"syscall"
)

// Get retrieves an extended attribute from the given path
// This is a stub implementation for non-Linux platforms
// Extended attributes are not supported on this platform
func Get(path string, attr *XAttr) (exists bool, errno syscall.Errno) {
	// Extended attributes not supported on this platform
	return false, syscall.ENOTSUP
}

// Set sets an extended attribute on the given path
// This is a stub implementation for non-Linux platforms
// Extended attributes are not supported on this platform
func Set(path string, attr *XAttr) syscall.Errno {
	// Extended attributes not supported on this platform
	return syscall.ENOTSUP
}

// Remove removes an extended attribute from the given path
// This is a stub implementation for non-Linux platforms
// Extended attributes are not supported on this platform
func Remove(path string) syscall.Errno {
	// Extended attributes not supported on this platform
	return syscall.ENOTSUP
}

