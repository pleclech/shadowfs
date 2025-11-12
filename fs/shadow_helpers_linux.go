//go:build linux
// +build linux

package fs

import (
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
)

// CopyFile copies a file from source to destination with zero-copy sendfile optimization
// This is the Linux-specific implementation using syscall.Sendfile
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

	// Get file size for sendfile
	var stat syscall.Stat_t
	err = syscall.Fstat(srcFd, &stat)
	if err != nil {
		return fs.ToErrno(err)
	}

	fileSize := stat.Size

	// Use sendfile for zero-copy transfer (more efficient than read/write loop)
	// sendfile transfers data directly between file descriptors without user space copying
	for fileSize > 0 {
		// sendfile returns number of bytes transferred
		n, err := syscall.Sendfile(destFd, srcFd, nil, int(fileSize))
		if err != nil {
			// Fallback to read/write method if sendfile fails
			return h.copyFileFallback(srcFd, destFd)
		}

		if n == 0 {
			break
		}

		fileSize -= int64(n)
	}

	return 0
}

