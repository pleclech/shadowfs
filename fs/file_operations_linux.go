//go:build linux
// +build linux

package fs

import (
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
)

// GetXattr gets an extended attribute
// This is the Linux-specific implementation using syscall.Getxattr
func (fo *FileOperationImpl) GetXattr(name string, dest []byte) (int, syscall.Errno) {
	path := fo.GetCachePath()
	if !fo.IsCached() {
		path = fo.GetSourcePath()
	}

	size, err := syscall.Getxattr(path, name, dest)
	if err != nil {
		return 0, fs.ToErrno(err)
	}

	return size, 0
}

// SetXattr sets an extended attribute
// This is the Linux-specific implementation using syscall.Setxattr
func (fo *FileOperationImpl) SetXattr(name string, value []byte, flags int) syscall.Errno {
	path := fo.GetCachePath()
	if !fo.IsCached() {
		path = fo.GetSourcePath()
	}

	err := syscall.Setxattr(path, name, value, flags)
	if err != nil {
		return fs.ToErrno(err)
	}

	return 0
}

// RemoveXattr removes an extended attribute
// This is the Linux-specific implementation using syscall.Removexattr
func (fo *FileOperationImpl) RemoveXattr(name string) syscall.Errno {
	path := fo.GetCachePath()
	if !fo.IsCached() {
		path = fo.GetSourcePath()
	}

	err := syscall.Removexattr(path, name)
	if err != nil {
		return fs.ToErrno(err)
	}

	return 0
}

