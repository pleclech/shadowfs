//go:build darwin
// +build darwin

package fs

// UNTESTED: macOS implementation - requires testing on macOS hardware

import (
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"golang.org/x/sys/unix"
)

// GetXattr gets an extended attribute
// This is the macOS-specific implementation using unix.Getxattr
func (fo *FileOperationImpl) GetXattr(name string, dest []byte) (int, syscall.Errno) {
	path := fo.GetCachePath()
	if !fo.IsCached() {
		path = fo.GetSourcePath()
	}

	size, err := unix.Getxattr(path, name, dest)
	if err != nil {
		return 0, fs.ToErrno(err)
	}

	return size, 0
}

// SetXattr sets an extended attribute
// This is the macOS-specific implementation using unix.Setxattr
func (fo *FileOperationImpl) SetXattr(name string, value []byte, flags int) syscall.Errno {
	path := fo.GetCachePath()
	if !fo.IsCached() {
		path = fo.GetSourcePath()
	}

	// macOS setxattr flags: 0 = create or replace
	err := unix.Setxattr(path, name, value, 0)
	if err != nil {
		return fs.ToErrno(err)
	}

	return 0
}

// RemoveXattr removes an extended attribute
// This is the macOS-specific implementation using unix.Removexattr
func (fo *FileOperationImpl) RemoveXattr(name string) syscall.Errno {
	path := fo.GetCachePath()
	if !fo.IsCached() {
		path = fo.GetSourcePath()
	}

	err := unix.Removexattr(path, name)
	if err != nil {
		return fs.ToErrno(err)
	}

	return 0
}

