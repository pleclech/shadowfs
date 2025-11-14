package cache

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"

	"github.com/pleclech/shadowfs/fs/pathutil"
	"github.com/pleclech/shadowfs/fs/xattr"
)

// Manager provides cache state management with clean interface
type Manager struct {
	cachePath string
	srcDir    string
	xattrMgr  *xattr.Manager
}

// NewManager creates a new cache manager
func NewManager(cachePath, srcDir string) *Manager {
	return &Manager{
		cachePath: cachePath,
		srcDir:    srcDir,
		xattrMgr:  xattr.NewManager(),
	}
}

// IsInCache checks if a path exists in cache (not just xattr, actual file/dir)
func (m *Manager) IsInCache(sourcePath string) bool {
	cachePath := pathutil.RebaseToCache(sourcePath, m.cachePath, m.srcDir)
	var st syscall.Stat_t
	err := syscall.Lstat(cachePath, &st)
	return err == nil
}

// IsDeleted checks if a path is marked as deleted in cache
func (m *Manager) IsDeleted(sourcePath string) (bool, syscall.Errno) {
	cachePath := pathutil.RebaseToCache(sourcePath, m.cachePath, m.srcDir)
	return m.xattrMgr.IsDeleted(cachePath)
}

// MarkDeleted marks a source path as deleted in cache
func (m *Manager) MarkDeleted(sourcePath string, originalType uint32) syscall.Errno {
	cachePath := pathutil.RebaseToCache(sourcePath, m.cachePath, m.srcDir)
	
	// Ensure cache directory exists for the deletion marker
	dir := filepath.Dir(cachePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fs.ToErrno(err)
	}
	
	// Create empty file as deletion marker if it doesn't exist
	var st syscall.Stat_t
	if err := syscall.Lstat(cachePath, &st); err != nil {
		if os.IsNotExist(err) {
			// Create empty file as marker
			fd, err := syscall.Open(cachePath, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_TRUNC, 0644)
			if err != nil {
				return fs.ToErrno(err)
			}
			syscall.Close(fd)
		} else {
			return fs.ToErrno(err)
		}
	}
	
	return m.xattrMgr.SetDeleted(cachePath, originalType)
}

// EnsurePath ensures a directory path exists in cache, creating partial arborescence
// This implements Phase 2.3: Partial Cache Path Creation
// sourcePath MUST be a source path (relative to srcDir or absolute source path)
// Callers are responsible for converting mount point or cache paths to source paths
func (m *Manager) EnsurePath(sourcePath string) syscall.Errno {
	// Normalize: ensure sourcePath is absolute and within srcDir
	if !strings.HasPrefix(sourcePath, m.srcDir) {
		// Path is relative or doesn't start with srcDir - treat as relative to srcDir
		sourcePath = filepath.Join(m.srcDir, sourcePath)
	}
	
	// Get relative path from srcDir (this is the path we need to create)
	relPath, err := filepath.Rel(m.srcDir, sourcePath)
	if err != nil {
		return fs.ToErrno(err)
	}
	
	// If relative path is "." or empty, we're at root - nothing to do
	if relPath == "." || relPath == "" {
		return 0
	}
	
	// Split path into components
	parts := strings.Split(relPath, string(filepath.Separator))
	
	// Build path incrementally, creating each level if needed
	currentPath := m.srcDir
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		currentPath = filepath.Join(currentPath, part)
		
		// Check if already cached
		if !m.IsInCache(currentPath) {
			// Check if directory exists in source AND hasn't been detached
			var srcSt syscall.Stat_t
			if err := syscall.Lstat(currentPath, &srcSt); err == nil {
				// Directory exists in source - check if it's been detached
				cacheDirPath := pathutil.RebaseToCache(currentPath, m.cachePath, m.srcDir)
				attr, exists, errno := m.xattrMgr.GetStatus(cacheDirPath)
				
				// Only copy from source if:
				// 1. Directory exists in source (we already checked)
				// 2. Not marked as permanently independent (CacheIndependent = false)
				// 3. Not marked as deleted
				shouldCopyFromSource := true
				if errno == 0 && exists && attr != nil {
					if attr.CacheIndependent {
						// Directory has been detached - don't copy from source
						// This handles Edge Case 4: Deleted Content Independence
						shouldCopyFromSource = false
					}
					if xattr.IsPathDeleted(*attr) {
						// Directory is marked as deleted - don't copy from source
						shouldCopyFromSource = false
					}
				}
				
				if shouldCopyFromSource {
					// Directory exists in source and hasn't been detached - copy with COW
					// This handles Edge Case 1: Move into Existing Source Directory
					_, err := CreateMirroredDir(currentPath, m.cachePath, m.srcDir)
					if err != nil {
						return fs.ToErrno(err)
					}
				} else {
					// Directory exists in source but has been detached - create in cache only
					// This handles Edge Case 4: Deleted Content Independence
					if err := os.MkdirAll(cacheDirPath, 0755); err != nil {
						return fs.ToErrno(err)
					}
				}
			} else if os.IsNotExist(err) {
				// Directory doesn't exist in source - create it in cache only
				// This is partial cache path creation (Edge Case 1: Move into non-existent dir)
				cacheDirPath := pathutil.RebaseToCache(currentPath, m.cachePath, m.srcDir)
				if err := os.MkdirAll(cacheDirPath, 0755); err != nil {
					return fs.ToErrno(err)
				}
			} else {
				return fs.ToErrno(err)
			}
		}
	}
	
	return 0
}

// ResolveCachePath converts a source path to cache path
func (m *Manager) ResolveCachePath(sourcePath string) string {
	return pathutil.RebaseToCache(sourcePath, m.cachePath, m.srcDir)
}

// ResolveSourcePath converts a cache path to source path
func (m *Manager) ResolveSourcePath(cachePath string) string {
	return pathutil.RebaseToSource(cachePath, m.srcDir, m.cachePath)
}

