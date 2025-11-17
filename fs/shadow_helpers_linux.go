//go:build linux
// +build linux

package fs

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
)

// ensureParentDirectory creates parent directory if it doesn't exist,
// preserving permissions and timestamps from source parent directory
// Linux-specific implementation
func (h *ShadowNodeHelpers) ensureParentDirectory(srcPath, destPath string) syscall.Errno {
	destDir := filepath.Dir(destPath)

	// Check if parent directory already exists
	var destDirSt syscall.Stat_t
	if err := syscall.Lstat(destDir, &destDirSt); err == nil {
		// Directory exists - no need to create
		return 0
	}

	// Get source parent directory path
	srcDir := filepath.Dir(srcPath)

	// Get source parent directory metadata
	var srcDirSt syscall.Stat_t
	if err := syscall.Lstat(srcDir, &srcDirSt); err != nil {
		// Source parent doesn't exist or can't be accessed
		// Use default permissions (0755) - ensure execute bits for traversal
		if err := os.MkdirAll(destDir, 0755); err != nil {
			return fs.ToErrno(err)
		}
		return 0
	}

	// Extract permission bits from source directory
	// Ensure execute bits are set (needed for directory traversal)
	dirMode := srcDirSt.Mode & 0777
	if dirMode&0111 == 0 {
		// Missing execute bits - ensure at least 0755
		dirMode = 0755
	}

	// Create parent directory with source permissions
	// os.MkdirAll handles nested directory creation automatically
	if err := os.MkdirAll(destDir, os.FileMode(dirMode)); err != nil {
		return fs.ToErrno(err)
	}

	// Preserve ownership (best-effort, don't fail on error)
	if err := syscall.Chown(destDir, int(srcDirSt.Uid), int(srcDirSt.Gid)); err != nil {
		// Log but don't fail - metadata is best-effort
	}

	// Preserve timestamps (best-effort, don't fail on error)
	// Linux uses Atim and Mtim
	var times [2]syscall.Timespec
	times[0] = syscall.Timespec{Sec: srcDirSt.Atim.Sec, Nsec: srcDirSt.Atim.Nsec}
	times[1] = syscall.Timespec{Sec: srcDirSt.Mtim.Sec, Nsec: srcDirSt.Mtim.Nsec}
	if err := syscall.UtimesNano(destDir, times[:]); err != nil {
		// Log but don't fail - metadata is best-effort
	}

	return 0
}

// CopyFile copies a file from source to destination with zero-copy sendfile optimization
// This is the Linux-specific implementation using syscall.Sendfile
// Preserves file mode, ownership, and timestamps
func (h *ShadowNodeHelpers) CopyFile(srcPath, destPath string, mode uint32) syscall.Errno {
	// Ensure parent directory exists with preserved metadata
	if errno := h.ensureParentDirectory(srcPath, destPath); errno != 0 {
		return errno
	}

	srcFd, err := syscall.Open(srcPath, syscall.O_RDONLY, 0)
	if err != nil {
		return fs.ToErrno(err)
	}
	defer syscall.Close(srcFd)

	destFd, err := syscall.Open(destPath, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_TRUNC, mode)
	if err != nil {
		return fs.ToErrno(err)
	}
	defer syscall.Close(destFd)

	// Get file size and metadata for sendfile and metadata preservation
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
			if errno := h.copyFileFallback(srcFd, destFd); errno != 0 {
				return errno
			}
			break
		}

		if n == 0 {
			break
		}

		fileSize -= int64(n)
	}

	// Copy metadata (ownership and timestamps)
	// Best-effort: log errors but don't fail the copy operation
	if err := syscall.Chown(destPath, int(stat.Uid), int(stat.Gid)); err != nil {
		// Log but don't fail - metadata is best-effort
	}

	var times [2]syscall.Timespec
	times[0] = syscall.Timespec{Sec: stat.Atim.Sec, Nsec: stat.Atim.Nsec}
	times[1] = syscall.Timespec{Sec: stat.Mtim.Sec, Nsec: stat.Mtim.Nsec}
	if err := syscall.UtimesNano(destPath, times[:]); err != nil {
		// Log but don't fail - metadata is best-effort
	}

	return 0
}
