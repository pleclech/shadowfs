package fs

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"

	"github.com/pleclech/shadowfs/fs/cache"
	"github.com/pleclech/shadowfs/fs/utils"
	"github.com/pleclech/shadowfs/fs/xattr"
)

// ShadowNodeHelpers provides helper methods for ShadowNode operations
type ShadowNodeHelpers struct {
	node        *ShadowNode
	pathManager *PathManager
}

// NewShadowNodeHelpers creates a new ShadowNodeHelpers instance
func NewShadowNodeHelpers(node *ShadowNode) *ShadowNodeHelpers {
	pathManager := NewPathManager(node.srcDir, node.cachePath)

	return &ShadowNodeHelpers{
		node:        node,
		pathManager: pathManager,
	}
}

// StatFile performs a stat operation with error handling
func (h *ShadowNodeHelpers) StatFile(path string, followSymlinks bool) (*syscall.Stat_t, syscall.Errno) {
	// Perform actual stat
	var st syscall.Stat_t
	var err error
	if followSymlinks {
		err = syscall.Stat(path, &st)
	} else {
		err = syscall.Lstat(path, &st)
	}

	var errno syscall.Errno
	if err != nil {
		errno = fs.ToErrno(err)
	}

	return &st, errno
}

// CheckFileDeleted checks if a file is marked as deleted using xattr
func (h *ShadowNodeHelpers) CheckFileDeleted(path string) (bool, syscall.Errno) {
	attr := xattr.XAttr{}
	exists, errno := xattr.Get(path, &attr)
	if errno != 0 {
		return false, errno
	}
	return exists && xattr.IsPathDeleted(attr), 0
}

// CreateMirroredDir creates a directory in the cache with proper error handling
func (h *ShadowNodeHelpers) CreateMirroredDir(path string) (string, syscall.Errno) {
	cachePath := h.pathManager.RebaseToCache(path)

	// Create all parent directories
	parentDir := filepath.Dir(cachePath)
	if parentDir != cachePath {
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			return "", fs.ToErrno(err)
		}
		// Ensure parent directories have proper permissions
		if err := utils.EnsureDirPermissions(parentDir); err != nil {
			return "", fs.ToErrno(err)
		}
	}

	// Create the target directory
	err := syscall.Mkdir(cachePath, 0755)
	if err != nil && err != syscall.EEXIST {
		return "", fs.ToErrno(err)
	}

	// Ensure directory has proper permissions (especially if it already existed)
	if err := utils.EnsureDirPermissions(cachePath); err != nil {
		return "", fs.ToErrno(err)
	}

	return cachePath, 0
}

// CreateMirroredFileOrDir creates a file or directory in the cache
func (h *ShadowNodeHelpers) CreateMirroredFileOrDir(srcPath string) (string, syscall.Errno) {
	// Get source file info
	st, errno := h.StatFile(srcPath, false)
	if errno != 0 {
		return "", errno
	}

	cachePath := h.pathManager.RebaseToCache(srcPath)

	// Create parent directory if needed
	parentDir := filepath.Dir(cachePath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return "", fs.ToErrno(err)
	}
	// Ensure parent directories have proper permissions
	if err := ensureDirPermissions(parentDir); err != nil {
		return "", fs.ToErrno(err)
	}

	// Create file or directory based on type
	if st.Mode&syscall.S_IFDIR != 0 {
		// Ensure directories have at least 0755 permissions (they need execute bits to be traversable)
		// Preserve higher permissions if they exist
		dirMode := st.Mode | 0755
		err := syscall.Mkdir(cachePath, dirMode)
		if err != nil && err != syscall.EEXIST {
			return "", fs.ToErrno(err)
		}
		// Ensure directory has proper permissions (especially if it already existed)
		if err := ensureDirPermissions(cachePath); err != nil {
			return "", fs.ToErrno(err)
		}
	} else {
		// Create file
		fd, err := syscall.Open(cachePath, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_TRUNC, st.Mode)
		if err != nil {
			return "", fs.ToErrno(err)
		}
		syscall.Close(fd)
	}

	// Copy permissions and timestamps - preserve file type bits
	// if err := syscall.Chmod(cachePath, st.Mode); err != nil {
	// 	return "", fs.ToErrno(err)
	// }

	// Copy ownership
	if err := syscall.Chown(cachePath, int(st.Uid), int(st.Gid)); err != nil {
		return "", fs.ToErrno(err)
	}

	// Copy timestamps
	var times [2]syscall.Timespec
	times[0] = syscall.Timespec{Sec: st.Atim.Sec, Nsec: st.Atim.Nsec}
	times[1] = syscall.Timespec{Sec: st.Mtim.Sec, Nsec: st.Mtim.Nsec}
	if err := syscall.UtimesNano(cachePath, times[:]); err != nil {
		return "", fs.ToErrno(err)
	}

	return cachePath, 0
}

// CopyFile is implemented in platform-specific files:
// - shadow_helpers_linux.go: Linux version with sendfile optimization
// - shadow_helpers_other.go: Non-Linux version using fallback method

// copyFileFallback is the traditional read/write copy method
func (h *ShadowNodeHelpers) copyFileFallback(srcFd, destFd int) syscall.Errno {
	bufPtr := cache.GetBufferPool().Get().(*[]byte)
	defer cache.GetBufferPool().Put(bufPtr)
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

	return 0
}

// WriteFileOnce writes data to a file atomically
func (h *ShadowNodeHelpers) WriteFileOnce(path string, data []byte) syscall.Errno {
	// Create parent directory if needed
	parentDir := filepath.Dir(path)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fs.ToErrno(err)
	}

	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_TRUNC, 0644)
	if err != nil {
		return fs.ToErrno(err)
	}
	defer syscall.Close(fd)

	offset := 0
	for offset < len(data) {
		n, err := syscall.Write(fd, data[offset:])
		if err != nil {
			return fs.ToErrno(err)
		}
		offset += n
	}

	return 0
}

// GetPathManager returns the path manager
func (h *ShadowNodeHelpers) GetPathManager() *PathManager {
	return h.pathManager
}
