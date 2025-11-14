package operations

import (
	"context"
	"path/filepath"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"

	"github.com/pleclech/shadowfs/fs/cache"
	"github.com/pleclech/shadowfs/fs/utils"
	"github.com/pleclech/shadowfs/fs/xattr"
)

// LookupOperation handles cache-first lookup with all constraints
type LookupOperation struct {
	cacheMgr *cache.Manager
	srcDir   string
}

// NewLookupOperation creates a new lookup operation handler
func NewLookupOperation(cacheMgr *cache.Manager, srcDir string) *LookupOperation {
	return &LookupOperation{
		cacheMgr: cacheMgr,
		srcDir:   srcDir,
	}
}

// Lookup performs cache-first lookup per Phase 1.2 constraints
// 1. Check cache first (CACHE PRIORITY)
// 2. Check if path is marked as deleted in cache
// 3. Fall back to source (read-only access)
func (op *LookupOperation) Lookup(
	ctx context.Context,
	name string,
	parentPath string,
	rootData *fs.LoopbackRoot,
	createInode func(name string, st *syscall.Stat_t) (*fs.Inode, syscall.Errno),
) (*fs.Inode, syscall.Errno) {
	// 1. Validate input
	if !utils.ValidateName(name) {
		return nil, syscall.EPERM
	}

	// 2. Resolve paths
	sourcePath := filepath.Join(parentPath, name)
	cachePath := op.cacheMgr.ResolveCachePath(sourcePath)

	// 3. Check cache first (CACHE PRIORITY - Principle 2)
	if entry, errno := op.lookupInCache(cachePath, sourcePath, name, rootData, createInode); errno == 0 && entry != nil {
		return entry, 0
	} else if errno != 0 && errno != syscall.ENOENT {
		return nil, errno
	}

	// 4. Check if path is marked as deleted in cache
	deleted, errno := op.cacheMgr.IsDeleted(sourcePath)
	if errno != 0 && errno != syscall.ENODATA {
		return nil, errno
	}
	if deleted {
		return nil, syscall.ENOENT
	}

	// 5. Fall back to source (read-only access)
	return op.lookupInSource(sourcePath, name, rootData, createInode)
}

// lookupInCache checks cache and handles type replacement
func (op *LookupOperation) lookupInCache(
	cachePath string,
	sourcePath string,
	name string,
	rootData *fs.LoopbackRoot,
	createInode func(name string, st *syscall.Stat_t) (*fs.Inode, syscall.Errno),
) (*fs.Inode, syscall.Errno) {
	var st syscall.Stat_t
	if err := syscall.Lstat(cachePath, &st); err != nil {
		return nil, syscall.ENOENT
	}

	// Check xattr for type replacement or other metadata
	xattrMgr := xattr.NewManager()
	attr, exists, errno := xattrMgr.GetStatus(cachePath)
	if errno != 0 && errno != syscall.ENODATA {
		return nil, errno
	}

	// Handle type replacement scenario
	if exists && attr.TypeReplaced {
		// Type was replaced - cache entry takes absolute priority
		// Source entry (if exists) is hidden
		return op.createInodeWithPermissions(cachePath, &st, name, rootData, createInode)
	}

	// Regular cache entry - check for conflicts with source
	var srcSt syscall.Stat_t
	if err := syscall.Lstat(sourcePath, &srcSt); err == nil {
		// Source exists - check for type conflict
		cacheIsDir := st.Mode&syscall.S_IFDIR != 0
		srcIsDir := srcSt.Mode&syscall.S_IFDIR != 0

		if cacheIsDir != srcIsDir {
			// Type conflict - cache takes priority (already handled by cache-first check)
			// This is a type replacement scenario
		}
	}

	return op.createInodeWithPermissions(cachePath, &st, name, rootData, createInode)
}

// lookupInSource checks source and creates inode
func (op *LookupOperation) lookupInSource(
	sourcePath string,
	name string,
	rootData *fs.LoopbackRoot,
	createInode func(name string, st *syscall.Stat_t) (*fs.Inode, syscall.Errno),
) (*fs.Inode, syscall.Errno) {
	var st syscall.Stat_t
	if err := syscall.Lstat(sourcePath, &st); err != nil {
		return nil, fs.ToErrno(err)
	}

	return op.createInodeWithPermissions(sourcePath, &st, name, rootData, createInode)
}

// createInodeWithPermissions creates inode with proper permissions
func (op *LookupOperation) createInodeWithPermissions(
	path string,
	st *syscall.Stat_t,
	name string,
	rootData *fs.LoopbackRoot,
	createInode func(name string, st *syscall.Stat_t) (*fs.Inode, syscall.Errno),
) (*fs.Inode, syscall.Errno) {
	// Fix directory permissions if needed
	if st.Mode&syscall.S_IFDIR != 0 {
		if err := utils.EnsureDirPermissions(path); err != nil {
			// Continue - we'll fix st.Mode below
		} else {
			// Re-stat after fixing to get correct permissions
			if err2 := syscall.Lstat(path, st); err2 == nil {
				// st now has correct permissions
			}
		}

		// Ensure st.Mode has correct execute bits
		permBits := st.Mode & 0777
		if permBits&0111 == 0 {
			st.Mode = (st.Mode &^ 0777) | (permBits | 0755)
		}
	}

	return createInode(name, st)
}
