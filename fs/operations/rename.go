package operations

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"

	"github.com/pleclech/shadowfs/fs/cache"
	"github.com/pleclech/shadowfs/fs/pathutil"
)

// RenameOperation handles rename operations with all constraints per Phase 2
type RenameOperation struct {
	cacheMgr     *cache.Manager
	srcDir       string
	cachePath    string
	renameTracker *RenameTracker
}

// NewRenameOperation creates a new rename operation handler
func NewRenameOperation(cacheMgr *cache.Manager, srcDir, cachePath string, renameTracker *RenameTracker) *RenameOperation {
	if renameTracker == nil {
		renameTracker = NewRenameTracker()
	}
	return &RenameOperation{
		cacheMgr:      cacheMgr,
		srcDir:        srcDir,
		cachePath:     cachePath,
		renameTracker: renameTracker,
	}
}

// Rename performs rename operation per Phase 2.1 constraints
// 1. Validate paths
// 2. Determine if source is in cache
// 3. Handle cache-only rename separately from COW rename
// NO source directory modifications allowed
func (op *RenameOperation) Rename(
	sourcePath string,
	destPath string,
	flags uint32,
	copyFile func(srcPath, destPath string, mode uint32) syscall.Errno,
) syscall.Errno {
	// 1. Validate paths (already validated by caller)
	
	// 2. Determine if source is in cache
	sourceInCache := op.cacheMgr.IsInCache(sourcePath)
	
	if sourceInCache {
		// Simple cache-only rename (no source touch)
		return op.renameInCache(sourcePath, destPath, flags)
	}
	
	// Copy-on-Write rename from source
	return op.copyOnWriteRename(sourcePath, destPath, copyFile)
}

// renameInCache performs cache-to-cache rename
func (op *RenameOperation) renameInCache(sourcePath, destPath string, flags uint32) syscall.Errno {
	sourceCachePath := op.cacheMgr.ResolveCachePath(sourcePath)
	destCachePath := op.cacheMgr.ResolveCachePath(destPath)
	
	// Handle RENAME_EXCHANGE if needed
	if flags&fs.RENAME_EXCHANGE != 0 {
		// Exchange requires both files to exist - handle separately
		return op.RenameExchange(sourceCachePath, destCachePath)
	}
	
	// Ensure destination directory exists in cache
	// EnsurePath expects a source path, so convert cache path back to source
	destDirSource := pathutil.RebaseToSource(filepath.Dir(destPath), op.srcDir, op.cachePath)
	if errno := op.cacheMgr.EnsurePath(destDirSource); errno != 0 {
		return errno
	}
	
	// Perform cache-to-cache rename
	if err := syscall.Rename(sourceCachePath, destCachePath); err != nil {
		return fs.ToErrno(err)
	}
	
	// Get file type before marking as deleted
	var st syscall.Stat_t
	if err := syscall.Lstat(destCachePath, &st); err != nil {
		return fs.ToErrno(err)
	}
	
	// Mark source location as deleted IN CACHE (not in source!)
	// This ensures the old path is hidden even though source still exists
	originalType := uint32(st.Mode & syscall.S_IFMT)
	if errno := op.cacheMgr.MarkDeleted(sourcePath, originalType); errno != 0 {
		return errno
	}
	
	// Update rename tracking if this is a directory rename
	if st.Mode&syscall.S_IFDIR != 0 {
		// Store rename mapping for directory
		op.renameTracker.StoreRenameMapping(sourcePath, destPath)
		// Store in xattr for persistence
		StoreRenameInXAttr(destCachePath, sourcePath, sourcePath, op.renameTracker.calculateDepth(sourcePath))
	}
	
	return 0
}

// RenameExchange handles RENAME_EXCHANGE flag
// Note: This is a simplified version - full implementation requires file descriptors
// For now, return error to indicate exchange needs special handling
func (op *RenameOperation) RenameExchange(sourcePath, destPath string) syscall.Errno {
	// RENAME_EXCHANGE requires special handling with file descriptors
	// This should be handled by the caller using the platform-specific implementation
	return syscall.ENOTSUP
}

// copyOnWriteRename implements COW rename per Phase 2.2 constraints
// 1. Ensure cache path exists (creates partial arborescence)
// 2. Copy file/directory from source to cache destination
// 3. Mark source location as deleted IN CACHE (not in source!)
// 4. New cache entry is now independent
func (op *RenameOperation) copyOnWriteRename(
	sourcePath string,
	destPath string,
	copyFile func(srcPath, destPath string, mode uint32) syscall.Errno,
) syscall.Errno {
	// 1. Ensure cache path exists (creates partial arborescence)
	destDir := filepath.Dir(destPath)
	if errno := op.cacheMgr.EnsurePath(destDir); errno != 0 {
		return errno
	}
	
	// Verify parent directory was created (defensive check)
	destDirCachePath := op.cacheMgr.ResolveCachePath(destDir)
	var dirSt syscall.Stat_t
	if err := syscall.Lstat(destDirCachePath, &dirSt); err != nil {
		// Parent directory doesn't exist - create it now
		if err := os.MkdirAll(destDirCachePath, 0755); err != nil {
			return fs.ToErrno(err)
		}
		// Ensure it's actually a directory
		if err := syscall.Lstat(destDirCachePath, &dirSt); err != nil {
			return fs.ToErrno(err)
		}
		if dirSt.Mode&syscall.S_IFDIR == 0 {
			return syscall.ENOTDIR
		}
	}
	
	// Get source file info
	var srcSt syscall.Stat_t
	if err := syscall.Lstat(sourcePath, &srcSt); err != nil {
		return fs.ToErrno(err)
	}
	
	destCachePath := op.cacheMgr.ResolveCachePath(destPath)
	
	// 2. Copy file/directory from source to cache destination
	if srcSt.Mode&syscall.S_IFDIR != 0 {
		// Directory: use CreateMirroredDir which handles COW
		createdPath, err := cache.CreateMirroredDir(sourcePath, op.cachePath, op.srcDir)
		if err != nil {
			return fs.ToErrno(err)
		}
		// Update destination path to actual cache path
		destCachePath = pathutil.RebaseToCache(sourcePath, op.cachePath, op.srcDir)
		// If destination is different, rename it
		actualDestCachePath := op.cacheMgr.ResolveCachePath(destPath)
		if destCachePath != actualDestCachePath {
			// Ensure parent exists
			if err := os.MkdirAll(filepath.Dir(actualDestCachePath), 0755); err != nil {
				return fs.ToErrno(err)
			}
			if err := syscall.Rename(destCachePath, actualDestCachePath); err != nil {
				return fs.ToErrno(err)
			}
			destCachePath = actualDestCachePath
		} else {
			destCachePath = createdPath
		}
		
		// Store rename mapping for directory
		op.renameTracker.StoreRenameMapping(sourcePath, destPath)
		// Store in xattr
		StoreRenameInXAttr(destCachePath, sourcePath, sourcePath, op.renameTracker.calculateDepth(sourcePath))
	} else {
		// File: copy using provided copy function
		if errno := copyFile(sourcePath, destCachePath, uint32(srcSt.Mode)); errno != 0 {
			return errno
		}
		// Copy ownership and timestamps
		if err := syscall.Chown(destCachePath, int(srcSt.Uid), int(srcSt.Gid)); err != nil {
			return fs.ToErrno(err)
		}
		var times [2]syscall.Timespec
		times[0] = syscall.Timespec{Sec: srcSt.Atim.Sec, Nsec: srcSt.Atim.Nsec}
		times[1] = syscall.Timespec{Sec: srcSt.Mtim.Sec, Nsec: srcSt.Mtim.Nsec}
		if err := syscall.UtimesNano(destCachePath, times[:]); err != nil {
			return fs.ToErrno(err)
		}
	}
	
	// 3. Mark source location as deleted IN CACHE (not in source!)
	originalType := uint32(srcSt.Mode & syscall.S_IFMT)
	if errno := op.cacheMgr.MarkDeleted(sourcePath, originalType); errno != 0 {
		return errno
	}
	
	// 4. New cache entry is now independent
	return 0
}

// ResolveRenamedPath resolves renamed paths for directory operations
func (op *RenameOperation) ResolveRenamedPath(requestedPath string) (string, bool) {
	return op.renameTracker.ResolveRenamedPath(requestedPath)
}

