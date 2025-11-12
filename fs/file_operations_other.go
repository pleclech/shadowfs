//go:build !linux && !darwin
// +build !linux,!darwin

package fs

import (
	"syscall"
)

// GetXattr gets an extended attribute
// This is a stub implementation for non-Linux platforms
// Extended attributes are not supported on this platform
func (fo *FileOperationImpl) GetXattr(name string, dest []byte) (int, syscall.Errno) {
	// Extended attributes not supported on this platform
	return 0, syscall.ENOTSUP
}

// SetXattr sets an extended attribute
// This is a stub implementation for non-Linux platforms
// Extended attributes are not supported on this platform
func (fo *FileOperationImpl) SetXattr(name string, value []byte, flags int) syscall.Errno {
	// Extended attributes not supported on this platform
	return syscall.ENOTSUP
}

// RemoveXattr removes an extended attribute
// This is a stub implementation for non-Linux platforms
// Extended attributes are not supported on this platform
func (fo *FileOperationImpl) RemoveXattr(name string) syscall.Errno {
	// Extended attributes not supported on this platform
	return syscall.ENOTSUP
}

