package cache

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"

	"github.com/pleclech/shadowfs/fs/pathutil"
	"github.com/pleclech/shadowfs/fs/utils"
)

// bufferPool provides reusable 64KB buffers to reduce allocations
var bufferPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 64*1024) // 64KB buffer
		return &buf
	},
}

// GetBufferPool returns the shared buffer pool for file operations
func GetBufferPool() *sync.Pool {
	return &bufferPool
}

// CreateMirroredDir creates a directory in the cache mirroring the source structure
func CreateMirroredDir(path, cachePath, srcDir string) (string, error) {
	// Check if path is already a cache path - if so, use it directly
	if strings.HasPrefix(path, cachePath) {
		// Path is already in cache, check if it exists and respect its permissions
		var st syscall.Stat_t
		if err := syscall.Lstat(path, &st); err != nil {
			if err == syscall.ENOENT {
				// Create with default permissions if it doesn't exist
				err := os.MkdirAll(path, 0755)
				if err != nil {
					return path, err
				}
			} else {
				// Other error (likely permission denied accessing parent)
				return path, err
			}
		} else {
			// Directory exists - verify it's actually a directory
			if st.Mode&syscall.S_IFDIR == 0 {
				// Path exists but is not a directory
				return path, syscall.ENOTDIR
			}
			// Check if we have access to it
			// This respects existing permissions for security tests
			if err := syscall.Access(path, 0x2|0x1); err != nil { // W_OK | X_OK
				return path, err
			}
		}
		return path, nil
	}

	// Original logic for source paths
	path = pathutil.RebaseToSource(path, srcDir, cachePath)

	dstDir := cachePath

	path = strings.TrimPrefix(path, srcDir)
	paths := strings.Split(path, string(os.PathSeparator))

	last := len(paths) - 1

	if last >= 0 && paths[0] == "" {
		paths = paths[1:]
		last--
	}

	// Optimization: use os.MkdirAll for bulk creation, then fix permissions
	fullCachePath := dstDir
	for _, dir := range paths {
		// Validate each directory component doesn't contain path traversal
		if dir == ".." || dir == "." {
			return fullCachePath, syscall.EPERM
		}
		fullCachePath = filepath.Join(fullCachePath, dir)
	}

	// Check if directory already exists to respect existing permissions
	if _, err := os.Stat(fullCachePath); err != nil {
		if os.IsNotExist(err) {
			// Directory doesn't exist, create with default permissions
			err := os.MkdirAll(fullCachePath, 0755)
			if err != nil {
				return fullCachePath, err
			}
		} else {
			// Other error accessing directory
			return fullCachePath, err
		}
	}

	// Now fix permissions for each directory level to ensure they're traversable
	// This is important because os.MkdirAll may preserve existing permissions
	// which could be missing execute bits
	currentPath := dstDir
	for _, dir := range paths {
		currentPath = filepath.Join(currentPath, dir)
		// Ensure each directory component has proper execute permissions
		if err := utils.EnsureDirPermissions(currentPath); err != nil {
			return currentPath, err
		}
	}

	return fullCachePath, nil
}

// CreateMirroredFileOrDir creates a file or directory in the cache mirroring the source
func CreateMirroredFileOrDir(srcPath, cachePath, srcDir string) (string, error) {
	// Always use original implementation for Create method to avoid initialization issues
	srcPath = pathutil.RebaseToSource(srcPath, srcDir, cachePath)
	cachePathResult := pathutil.RebaseToCache(srcPath, cachePath, srcDir)

	// get src dir mode using syscall
	var st syscall.Stat_t
	err := syscall.Lstat(srcPath, &st)
	if err != nil {
		return cachePathResult, err
	}

	// check if file is a directory
	if st.Mode&syscall.S_IFDIR != 0 {
		// create directory in cache using same permissions
		return CreateMirroredDir(srcPath, cachePath, srcDir)
	}

	// if file is a regular file create it in cache using same permissions
	if st.Mode&syscall.S_IFREG != 0 {
		// create directory if not exists recursively using same permissions
		newDir, err := CreateMirroredDir(filepath.Dir(srcPath), cachePath, srcDir)
		if err != nil {
			return newDir, err
		}

		// create empty file using syscall.Open (handles existing files gracefully)
		fd, err := syscall.Open(cachePathResult, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_TRUNC, st.Mode)
		if err != nil {
			return cachePathResult, err
		}
		syscall.Close(fd)

		// Copy ownership
		if err := syscall.Chown(cachePathResult, int(st.Uid), int(st.Gid)); err != nil {
			return cachePathResult, err
		}

		// Copy timestamps
		var times [2]syscall.Timespec
		times[0] = syscall.Timespec{Sec: st.Atim.Sec, Nsec: st.Atim.Nsec}
		times[1] = syscall.Timespec{Sec: st.Mtim.Sec, Nsec: st.Mtim.Nsec}
		if err := syscall.UtimesNano(cachePathResult, times[:]); err != nil {
			return cachePathResult, err
		}

		return cachePathResult, nil
	}

	return cachePathResult, syscall.ENOTSUP
}

// CopyFileSimple copies a file from source to destination (fallback when helpers not available)
// Note: Parent directory must exist - caller is responsible for ensuring it
func CopyFileSimple(srcPath, destPath string, mode uint32) syscall.Errno {
	// Ensure parent directory exists (defensive check)
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fs.ToErrno(err)
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
	times[0] = syscall.Timespec{Sec: stat.Atim.Sec, Nsec: stat.Atim.Nsec}
	times[1] = syscall.Timespec{Sec: stat.Mtim.Sec, Nsec: stat.Mtim.Nsec}
	if err := syscall.UtimesNano(destPath, times[:]); err != nil {
		return fs.ToErrno(err)
	}

	return 0
}
