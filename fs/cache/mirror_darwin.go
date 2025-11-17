//go:build darwin
// +build darwin

package cache

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
)

// copyTimestamps copies timestamps from stat to a file path
// Darwin-specific implementation
func copyTimestamps(path string, st *syscall.Stat_t) error {
	var times [2]syscall.Timespec
	// Darwin uses Atimespec and Mtimespec
	times[0] = st.Atimespec
	times[1] = st.Mtimespec
	return syscall.UtimesNano(path, times[:])
}

// CopyFileSimple copies a file from source to destination (fallback when helpers not available)
// Creates parent directory if needed and preserves its metadata from source
// Darwin-specific implementation
func CopyFileSimple(srcPath, destPath string, mode uint32) syscall.Errno {
	destDir := filepath.Dir(destPath)

	// Check if parent directory exists
	var destDirSt syscall.Stat_t
	if err := syscall.Lstat(destDir, &destDirSt); err != nil {
		// Parent doesn't exist - create it with metadata from source
		srcDir := filepath.Dir(srcPath)
		var srcDirSt syscall.Stat_t
		if err := syscall.Lstat(srcDir, &srcDirSt); err == nil {
			// Use source directory permissions
			dirMode := srcDirSt.Mode & 0777
			// Ensure execute bits are set (needed for directory traversal)
			if dirMode&0111 == 0 {
				dirMode = 0755
			}
			if err := os.MkdirAll(destDir, os.FileMode(dirMode)); err != nil {
				return fs.ToErrno(err)
			}
			// Preserve ownership and timestamps (best-effort)
			syscall.Chown(destDir, int(srcDirSt.Uid), int(srcDirSt.Gid))
			var times [2]syscall.Timespec
			// Darwin uses Atimespec and Mtimespec
			times[0] = syscall.Timespec{Sec: srcDirSt.Atimespec.Sec, Nsec: srcDirSt.Atimespec.Nsec}
			times[1] = syscall.Timespec{Sec: srcDirSt.Mtimespec.Sec, Nsec: srcDirSt.Mtimespec.Nsec}
			syscall.UtimesNano(destDir, times[:])
		} else {
			// Fallback to default permissions
			if err := os.MkdirAll(destDir, 0755); err != nil {
				return fs.ToErrno(err)
			}
		}
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

	// Get file size
	var stat syscall.Stat_t
	err = syscall.Fstat(srcFd, &stat)
	if err != nil {
		return fs.ToErrno(err)
	}

	// Copy file content using buffer pool
	bufPtr, ok := bufferPool.Get().(*[]byte)
	if !ok {
		return syscall.ENOMEM
	}
	defer bufferPool.Put(bufPtr)
	buf := *bufPtr

	for {
		n, err := syscall.Read(srcFd, buf)
		if err != nil && err != syscall.EINTR && err != syscall.EAGAIN {
			return fs.ToErrno(err)
		}
		if n == 0 {
			break
		}

		offset := 0
		for offset < n {
			written, err := syscall.Write(destFd, buf[offset:n])
			if err != nil {
				return fs.ToErrno(err)
			}
			offset += written
		}
	}

	// Copy metadata
	if err := syscall.Chown(destPath, int(stat.Uid), int(stat.Gid)); err != nil {
		return fs.ToErrno(err)
	}

	var times [2]syscall.Timespec
	// Darwin uses Atimespec and Mtimespec
	times[0] = stat.Atimespec
	times[1] = stat.Mtimespec
	if err := syscall.UtimesNano(destPath, times[:]); err != nil {
		return fs.ToErrno(err)
	}

	return 0
}

