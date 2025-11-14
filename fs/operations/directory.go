package operations

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/pleclech/shadowfs/fs/cache"
)

// DirectoryOperation handles directory operations per Phase 4 constraints
type DirectoryOperation struct {
	cacheMgr      *cache.Manager
	srcDir        string
	renameTracker *RenameTracker
}

// NewDirectoryOperation creates a new directory operation handler
func NewDirectoryOperation(cacheMgr *cache.Manager, srcDir string, renameTracker *RenameTracker) *DirectoryOperation {
	return &DirectoryOperation{
		cacheMgr:      cacheMgr,
		srcDir:        srcDir,
		renameTracker: renameTracker,
	}
}

// Readdir performs directory listing per Phase 4.1 constraints
// 1. List all cache entries first
// 2. Resolve current directory's original source path
// 3. Check if directory is renamed using resolveRenamedPath()
// 4. List source entries from resolved path (not current path)
// 5. Merge results with cache taking priority
func (op *DirectoryOperation) Readdir(
	ctx context.Context,
	currentPath string,
	createDirStream func(cachePath, sourcePath string) (fs.DirStream, syscall.Errno),
) (fs.DirStream, syscall.Errno) {
	// 1. List all cache entries first
	cachePath := op.cacheMgr.ResolveCachePath(currentPath)

	// 2. Convert absolute path to mount point relative path for rename resolution
	// ResolveRenamedPath expects a mount point relative path (e.g., "foo/baz")
	// but currentPath is absolute (e.g., "/tmp/.../002/foo/baz")
	mountPointRelativePath := strings.TrimPrefix(currentPath, op.srcDir)
	mountPointRelativePath = strings.TrimPrefix(mountPointRelativePath, "/")
	mountPointRelativePath = strings.Trim(mountPointRelativePath, "/")

	// 3. Resolve current directory's original source path using relative path
	resolvedRelativePath, isIndependent, isRenamed := op.renameTracker.ResolveRenamedPath(mountPointRelativePath)

	// 4. List source entries from resolved path
	// If renamed, use original source path; otherwise use current path
	// If independent, don't check source (but we still need to list cache)
	sourcePath := currentPath
	if isRenamed && !isIndependent {
		// Reconstruct absolute source path from resolved relative path
		sourcePath = filepath.Join(op.srcDir, resolvedRelativePath)
	} else if isIndependent {
		// Independent path - only list cache, not source
		sourcePath = "" // Empty sourcePath signals to DirStream to skip source
	}

	// 5. Merge results (cache takes priority - Principle 2)
	// This is handled by the DirStream implementation which processes cache first
	return createDirStream(cachePath, sourcePath)
}

// ListCacheEntries lists all entries in cache directory
func (op *DirectoryOperation) ListCacheEntries(cachePath string) ([]fuse.DirEntry, syscall.Errno) {
	// This will be implemented by the DirStream
	// For now, return empty - DirStream handles the actual listing
	return nil, 0
}

// ListSourceEntries lists all entries in source directory
func (op *DirectoryOperation) ListSourceEntries(sourcePath string) ([]fuse.DirEntry, syscall.Errno) {
	// This will be implemented by the DirStream
	// For now, return empty - DirStream handles the actual listing
	return nil, 0
}

// MergeDirectoryEntries merges cache and source entries with cache priority
func (op *DirectoryOperation) MergeDirectoryEntries(cacheEntries, sourceEntries []fuse.DirEntry) []fuse.DirEntry {
	// Cache entries take absolute priority
	// This is handled by DirStream which processes cache first, then source
	// Source entries are only included if not in cache
	merged := make([]fuse.DirEntry, 0, len(cacheEntries)+len(sourceEntries))

	// Add all cache entries
	merged = append(merged, cacheEntries...)

	// Add source entries that don't conflict with cache
	cacheNames := make(map[string]bool)
	for _, entry := range cacheEntries {
		cacheNames[entry.Name] = true
	}

	for _, entry := range sourceEntries {
		if !cacheNames[entry.Name] {
			merged = append(merged, entry)
		}
	}

	return merged
}
