//go:build !linux && !darwin
// +build !linux,!darwin

package fs

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
)

// ensureParentDirectory creates parent directory if it doesn't exist,
// preserving permissions and timestamps from source parent directory
// Other platform implementation
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
	var times [2]syscall.Timespec
	times[0] = syscall.Timespec{Sec: srcDirSt.Atim.Sec, Nsec: srcDirSt.Atim.Nsec}
	times[1] = syscall.Timespec{Sec: srcDirSt.Mtim.Sec, Nsec: srcDirSt.Mtim.Nsec}
	if err := syscall.UtimesNano(destDir, times[:]); err != nil {
		// Log but don't fail - metadata is best-effort
	}

	return 0
}

// CopyFile copies a file from source to destination using portable read/write method
// This is the non-Linux implementation that uses the fallback method
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

	// Get metadata for preservation
	var stat syscall.Stat_t
	err = syscall.Fstat(srcFd, &stat)
	if err != nil {
		return fs.ToErrno(err)
	}

	// Use fallback method for non-Linux platforms
	if errno := h.copyFileFallback(srcFd, destFd); errno != 0 {
		return errno
	}

	// Copy metadata (ownership and timestamps)
	// Best-effort: log errors but don't fail the copy operation
	if err := syscall.Chown(destPath, int(stat.Uid), int(stat.Gid)); err != nil {
		// Log but don't fail - metadata is best-effort
	}

	var times [2]syscall.Timespec
	times[0] = syscall.Timespec{Sec: stat.Atim.Sec, Nsec: stat.Atim.Nsec}
	times[1] = syscall.Timespec{Sec: stat.Mtim.Sec, Nsec: stat.Mtim.Sec}
	if err := syscall.UtimesNano(destPath, times[:]); err != nil {
		// Log but don't fail - metadata is best-effort
	}

	return 0
}
