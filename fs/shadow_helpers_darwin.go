//go:build darwin
// +build darwin

package fs

// UNTESTED: macOS implementation - requires testing on macOS hardware

import (
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
)

// CopyFile copies a file from source to destination using portable read/write method
// macOS doesn't have syscall.Sendfile, so we use the fallback method
func (h *ShadowNodeHelpers) CopyFile(srcPath, destPath string) syscall.Errno {
	srcFd, err := syscall.Open(srcPath, syscall.O_RDONLY, 0)
	if err != nil {
		return fs.ToErrno(err)
	}
	defer syscall.Close(srcFd)

	destFd, err := syscall.Open(destPath, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_TRUNC, 0644)
	if err != nil {
		return fs.ToErrno(err)
	}
	defer syscall.Close(destFd)

	// Use fallback method for macOS (no sendfile support)
	return h.copyFileFallback(srcFd, destFd)
}

