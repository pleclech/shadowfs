package fs

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/pleclech/shadowfs/fs/cache"
	"github.com/pleclech/shadowfs/fs/operations"
	"github.com/pleclech/shadowfs/fs/pathutil"
	"github.com/pleclech/shadowfs/fs/utils"
	"github.com/pleclech/shadowfs/fs/xattr"
)

func (n *ShadowNode) Opendir(ctx context.Context) syscall.Errno {
	mountPointPath := n.FullPath(true)

	// Use PathResolver to resolve paths
	cachePath, sourcePath, isIndependent, errno := n.pathResolver.ResolveForRead(mountPointPath)
	if errno == syscall.ENOENT {
		return syscall.ENOENT
	}
	if errno != 0 {
		return errno
	}

	// Check cache first
	if fd, err := syscall.Open(cachePath, syscall.O_DIRECTORY, 0755); err == nil {
		utils.PreserveOwner(ctx, cachePath)
		syscall.Close(fd)
		return fs.OK
	}

	// If independent, don't check source
	if isIndependent {
		return syscall.ENOENT
	}

	// Fall back to source path (resolved if renamed)
	fd, err := syscall.Open(sourcePath, syscall.O_DIRECTORY, 0755)
	if err != nil {
		return fs.ToErrno(err)
	}
	utils.PreserveOwner(ctx, sourcePath)
	syscall.Close(fd)
	return fs.OK
}

// OpendirHandle is called by go-fuse for READDIRPLUS operations
// This is the method that actually returns the DirStream, not Readdir
func (n *ShadowNode) OpendirHandle(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	mountPointPath := n.FullPath(true)

	// Use PathResolver to resolve paths
	cachePath, sourcePath, isIndependent, errno := n.pathResolver.ResolveForRead(mountPointPath)
	if errno == syscall.ENOENT {
		return nil, 0, syscall.ENOENT
	}
	if errno != 0 {
		return nil, 0, errno
	}

	// Preserve ownership of the directory (important when running as root)
	// Use cache path if exists, otherwise use resolved source path
	pathToPreserve := cachePath
	var st syscall.Stat_t
	if err := syscall.Lstat(cachePath, &st); err != nil && !isIndependent {
		pathToPreserve = sourcePath
	}
	utils.PreserveOwner(ctx, pathToPreserve)

	// Use clean directory operation per Phase 4.1
	// This resolves renamed paths and ensures source entries come from correct location
	if n.directoryOp == nil {
		// Fallback if not initialized (shouldn't happen)
		n.directoryOp = operations.NewDirectoryOperation(n.cacheMgr, n.srcDir, n.renameTracker)
	}

	currentPath := n.FullPath(false)

	// Use directoryOp.Readdir which handles rename resolution per Phase 4.1
	ds, errno := n.directoryOp.Readdir(ctx, currentPath, func(cachePath, sourcePath string) (fs.DirStream, syscall.Errno) {
		// Create DirStream with both resolved paths
		// cachePath: resolved cache path for current directory
		// sourcePath: resolved original source path (for renamed directories)
		return NewShadowDirStreamWithPaths(n, cachePath, sourcePath)
	})
	if errno != 0 {
		return nil, 0, errno
	}
	return ds, 0, fs.OK
}

func (n *ShadowNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Use clean directory operation per Phase 4.1
	// This resolves renamed paths and ensures source entries come from correct location
	if n.directoryOp == nil {
		// Fallback if not initialized (shouldn't happen)
		n.directoryOp = operations.NewDirectoryOperation(n.cacheMgr, n.srcDir, n.renameTracker)
	}

	currentPath := n.FullPath(false)

	// Use directoryOp.Readdir which handles rename resolution per Phase 4.1
	return n.directoryOp.Readdir(ctx, currentPath, func(cachePath, sourcePath string) (fs.DirStream, syscall.Errno) {
		// Create DirStream with both resolved paths
		// cachePath: resolved cache path for current directory
		// sourcePath: resolved original source path (for renamed directories)
		return NewShadowDirStreamWithPaths(n, cachePath, sourcePath)
	})
}

// checkSourceDirectory checks if directory exists in source and returns its mode
// Returns: (sourceExists bool, dirMode uint32, sourceSt syscall.Stat_t)
func (n *ShadowNode) checkSourceDirectory(sourcePath string, defaultMode uint32) (bool, uint32, syscall.Stat_t) {
	var sourceSt syscall.Stat_t
	sourceExists := syscall.Lstat(sourcePath, &sourceSt) == nil
	dirMode := defaultMode
	if sourceExists {
		dirMode = uint32(sourceSt.Mode)
	}
	return sourceExists, dirMode, sourceSt
}

// cleanupDeletedChildren removes deletion marker files from a directory
// This is used when recreating a deleted directory (type replacement)
func cleanupDeletedChildren(cachePath string) syscall.Errno {
	dirFd, err := syscall.Open(cachePath, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return 0 // Directory doesn't exist or can't be opened, nothing to clean
	}
	defer syscall.Close(dirFd)

	buf := make([]byte, 4096)
	for {
		n, err := syscall.Getdents(dirFd, buf)
		if n <= 0 {
			break
		}
		todo := buf[:n]
		for len(todo) > 0 {
			type dirent struct {
				Ino    uint64
				Off    int64
				Reclen uint16
				Type   uint8
				Name   [1]uint8
			}
			de := (*dirent)(unsafe.Pointer(&todo[0]))
			minSize := int(unsafe.Offsetof(dirent{}.Name))
			if de.Reclen < uint16(minSize) || int(de.Reclen) > len(todo) {
				break
			}
			nameBytes := todo[minSize:de.Reclen]
			end := 0
			for i, b := range nameBytes {
				if b == 0 {
					end = i
					break
				}
			}
			if end == 0 {
				todo = todo[de.Reclen:]
				continue
			}
			nameStr := string(nameBytes[:end])
			if nameStr != "." && nameStr != ".." {
				entryPath := filepath.Join(cachePath, nameStr)
				// Check if this is a deletion marker
				entryAttr := xattr.XAttr{}
				if exists, errno := xattr.Get(entryPath, &entryAttr); errno == 0 && exists && xattr.IsPathDeleted(entryAttr) {
					// Remove deletion marker file
					syscall.Unlink(entryPath)
					xattr.Remove(entryPath)
				}
			}
			todo = todo[de.Reclen:]
		}
		if err != nil {
			break
		}
	}
	return 0
}

// handleDeletedEntryInCache cleans up a deleted entry (file or directory) and prepares for recreation
// Returns: (typeReplacement bool, deletedAttr xattr.XAttr, errno syscall.Errno)
func (n *ShadowNode) handleDeletedEntryInCache(cachePath string, cacheSt syscall.Stat_t) (bool, xattr.XAttr, syscall.Errno) {
	attr := xattr.XAttr{}
	attrExists, errno := xattr.Get(cachePath, &attr)
	if errno != 0 && errno != syscall.ENODATA {
		return false, attr, errno
	}

	if !attrExists || !xattr.IsPathDeleted(attr) {
		return false, attr, 0
	}

	// Path is marked as deleted - clean up and recreate (type replacement)
	typeReplacement := true
	deletedAttr := attr

	if cacheSt.Mode&syscall.S_IFDIR != 0 {
		// It's a directory - clean up deleted children first
		if errno := cleanupDeletedChildren(cachePath); errno != 0 {
			return false, deletedAttr, errno
		}
		// Try to remove the directory - if it fails (not empty after cleanup), return error
		if err := syscall.Rmdir(cachePath); err != nil && !os.IsNotExist(err) {
			return false, deletedAttr, fs.ToErrno(err)
		}
	} else {
		// It's a file - unlink it
		if err := syscall.Unlink(cachePath); err != nil && !os.IsNotExist(err) {
			return false, deletedAttr, fs.ToErrno(err)
		}
	}

	// Remove xattr after cleanup
	xattr.Remove(cachePath)
	// Mark as independent (recreated path is not linked to source)
	n.setCacheIndependent(cachePath)

	return typeReplacement, deletedAttr, 0
}

// handleExistingDirectory handles the idempotent case when directory already exists
func (n *ShadowNode) handleExistingDirectory(ctx context.Context, name string, cachePath string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Ensure it has proper permissions
	if err := utils.EnsureDirPermissions(cachePath); err != nil {
		return nil, fs.ToErrno(err)
	}

	// Stat it and return
	var cacheSt syscall.Stat_t
	if err := syscall.Lstat(cachePath, &cacheSt); err != nil {
		return nil, fs.ToErrno(err)
	}

	// CRITICAL: Ensure mode has directory type bit set
	// This prevents panic in go-fuse bridge which validates mode must be S_IFDIR
	if cacheSt.Mode&syscall.S_IFDIR == 0 {
		// This shouldn't happen if we checked correctly, but be defensive
		// Force directory type bit
		cacheSt.Mode = (cacheSt.Mode &^ syscall.S_IFMT) | syscall.S_IFDIR
		// Ensure execute bits are set
		permBits := cacheSt.Mode & 0777
		if permBits&0111 == 0 {
			cacheSt.Mode = (cacheSt.Mode &^ 0777) | (permBits | 0755)
		}
	}

	// Set up output attributes
	out.Attr.FromStat(&cacheSt)

	// Create node for existing directory
	node := newShadowNode(n.RootData, n.EmbeddedInode(), name, &cacheSt)
	node.(*ShadowNode).mirrorPath = cachePath
	stableAttr := n.idFromStat(&cacheSt)

	// Double-check StableAttr has correct directory type
	if stableAttr.Mode&0170000 != 0040000 {
		stableAttr.Mode = (stableAttr.Mode &^ 0170000) | 0040000
	}
	if stableAttr.Mode&0111 == 0 {
		stableAttr.Mode |= 0755
	}

	ch := n.NewInode(ctx, node, stableAttr)
	if chOps := ch.Operations(); chOps != nil {
		if chNode, ok := chOps.(*ShadowNode); ok {
			chNode.mirrorPath = cachePath
		}
	}
	return ch, 0
}

// handleLazyCOW handles lazy COW when directory exists in source but not cache
func (n *ShadowNode) handleLazyCOW(ctx context.Context, name string, sourceSt syscall.Stat_t, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Directory exists in source - lazy COW (delegate to source)
	// Use source permissions, don't create in cache yet
	// Directory will be created in cache when content is written
	// Return success with source directory attributes
	out.Attr.FromStat(&sourceSt)

	// Create node pointing to source (lazy COW)
	node := newShadowNode(n.RootData, n.EmbeddedInode(), name, &sourceSt)
	stableAttr := n.idFromStat(&sourceSt)
	ch := n.NewInode(ctx, node, stableAttr)
	return ch, 0
}

// ensureParentDirectoryExists checks if parent directory exists and creates it if needed
func (n *ShadowNode) ensureParentDirectoryExists(cachePath, sourcePath string) syscall.Errno {
	parentPath := filepath.Dir(cachePath)
	var parentSt syscall.Stat_t
	parentExistsInCache := syscall.Lstat(parentPath, &parentSt) == nil

	if parentExistsInCache {
		// Parent exists in cache - ensure it has execute permissions
		if err := utils.EnsureDirPermissions(parentPath); err != nil {
			// Continue anyway - parent might already have correct permissions
		}
		return 0
	}

	// Parent doesn't exist in cache - check if it exists in source
	parentSourcePath := filepath.Dir(sourcePath)
	var parentSourceSt syscall.Stat_t
	parentExistsInSource := syscall.Lstat(parentSourcePath, &parentSourceSt) == nil

	if !parentExistsInSource {
		// Parent doesn't exist - return ENOENT (normal mkdir behavior)
		return syscall.ENOENT
	}

	// Parent exists in source but not cache - check if it's marked as deleted
	parentAttr := xattr.XAttr{}
	parentAttrExists, errno := xattr.Get(parentPath, &parentAttr)
	if errno != 0 && errno != syscall.ENODATA {
		return errno
	}

	if parentAttrExists && xattr.IsPathDeleted(parentAttr) {
		// Parent is marked as deleted - return ENOENT (don't fix deleted chains)
		return syscall.ENOENT
	}

	// Create parent in cache using source permissions
	_, err := cache.CreateMirroredDir(parentSourcePath, n.cachePath, n.srcDir)
	if err != nil {
		return fs.ToErrno(err)
	}

	// Ensure parent has execute permissions
	if err := utils.EnsureDirPermissions(parentPath); err != nil {
		// Continue anyway - parent might already have correct permissions
	}

	return 0
}

// createDirectoryInCache creates a directory in cache with proper permissions and metadata
// Returns: (st syscall.Stat_t, errno syscall.Errno)
func (n *ShadowNode) createDirectoryInCache(ctx context.Context, cachePath string, dirMode uint32) (syscall.Stat_t, syscall.Errno) {
	// Ensure directories have execute permissions
	// Temporarily disable umask to ensure permissions are set correctly
	oldUmask := syscall.Umask(0)
	defer syscall.Umask(oldUmask)

	// Use the mode from source if available, otherwise use provided mode
	// Ensure execute bits are set (0755) for directories to be traversable
	fileMode := os.FileMode(dirMode)
	if fileMode&0111 == 0 {
		// Missing execute bits - ensure at least 0755
		fileMode = os.FileMode(0755)
	}

	// Stat variable for final attributes
	var st syscall.Stat_t

	// Create directory in cache
	err := os.Mkdir(cachePath, fileMode)
	if err != nil {
		// Handle case where directory already exists (idempotent operation)
		if os.IsExist(err) {
			// Directory exists, verify it's actually a directory
			if err2 := syscall.Lstat(cachePath, &st); err2 != nil {
				return st, fs.ToErrno(err2)
			}
			if st.Mode&syscall.S_IFDIR == 0 {
				// Path exists but is not a directory
				return st, syscall.EEXIST
			}
			// It's a directory, ensure it has proper permissions
			if err := utils.EnsureDirPermissions(cachePath); err != nil {
				return st, fs.ToErrno(err)
			}
			// Re-stat after permission fix to get final attributes
			if err := syscall.Lstat(cachePath, &st); err != nil {
				return st, fs.ToErrno(err)
			}
			// Continue with permission fix and preserveOwner below
		} else {
			return syscall.Stat_t{}, fs.ToErrno(err)
		}
	} else {
		// Directory was created, ensure it has proper permissions immediately
		if err := utils.EnsureDirPermissions(cachePath); err != nil {
			syscall.Rmdir(cachePath)
			return syscall.Stat_t{}, fs.ToErrno(err)
		}
		// Stat immediately after creation and permission fix to get attributes
		if err := syscall.Lstat(cachePath, &st); err != nil {
			syscall.Rmdir(cachePath)
			return st, fs.ToErrno(err)
		}
	}

	// Ensure st.Mode has correct directory type and execute bits BEFORE preserveOwner
	// This ensures preserveOwner sees correct permissions
	if st.Mode&syscall.S_IFDIR != 0 {
		permBits := st.Mode & 0777
		if permBits&0111 == 0 {
			// Missing execute bits - fix them in filesystem first
			if err := syscall.Chmod(cachePath, uint32(permBits|0755)); err != nil {
				// Continue anyway
			}
			// Update stat to reflect fixed permissions
			if err := syscall.Lstat(cachePath, &st); err != nil {
				syscall.Rmdir(cachePath)
				return st, fs.ToErrno(err)
			}
		}
	} else {
		// This shouldn't happen - directory should have S_IFDIR
		syscall.Rmdir(cachePath)
		return st, syscall.ENOTDIR
	}

	// Now call preserveOwner - it shouldn't affect permissions, only ownership
	utils.PreserveOwner(ctx, cachePath)

	// Final stat after preserveOwner to get final attributes
	if err := syscall.Lstat(cachePath, &st); err != nil {
		syscall.Rmdir(cachePath)
		return st, fs.ToErrno(err)
	}

	// Ensure st.Mode still has correct directory type and execute bits
	// preserveOwner shouldn't change permissions, but be safe
	if st.Mode&syscall.S_IFDIR != 0 {
		permBits := st.Mode & 0777
		if permBits&0111 == 0 {
			// Fix execute bits in stat structure for FromStat
			st.Mode = (st.Mode &^ 0777) | (permBits | 0755)
		}
	} else {
		// This shouldn't happen
		st.Mode = (st.Mode &^ syscall.S_IFMT) | syscall.S_IFDIR | 0755
	}

	return st, 0
}

// setupDirectoryInode sets up FUSE inode with correct attributes and type replacement handling
func (n *ShadowNode) setupDirectoryInode(ctx context.Context, name string, cachePath string, sourcePath string, sourceExists bool, st syscall.Stat_t, typeReplacement bool, deletedAttr xattr.XAttr, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Set up output attributes
	out.Attr.FromStat(&st)

	// Create node
	node := newShadowNode(n.RootData, n.EmbeddedInode(), name, &st)
	node.(*ShadowNode).mirrorPath = cachePath

	// Ensure StableAttr has correct mode - idFromStat uses st.Mode directly
	// We've already fixed st.Mode above, so this should be correct
	stableAttr := n.idFromStat(&st)

	// Double-check that StableAttr.Mode has correct directory type and execute bits
	if stableAttr.Mode&0170000 != 0040000 {
		// Force directory type
		stableAttr.Mode = (stableAttr.Mode &^ 0170000) | 0040000
	}
	if stableAttr.Mode&0111 == 0 {
		// Force execute bits
		stableAttr.Mode |= 0755
	}

	// CRITICAL: For type replacement, use a completely different inode number
	// This ensures kernel treats it as a new inode, not a cached version of the old file inode
	if typeReplacement {
		// Use type-specific magic number: directory type gets different XOR than file type
		// This ensures even if underlying filesystem reuses inode numbers, we get unique inodes
		stableAttr.Ino = stableAttr.Ino ^ 0x2000000000000000 // Type-specific magic for directory
		stableAttr.Gen = stableAttr.Gen + 1000               // Large generation increment to force invalidation

		// Use type replacement operation per Phase 3.2
		if n.typeReplacement == nil {
			n.typeReplacement = operations.NewTypeReplacement()
		}
		// Get original type from source (if exists) or from deleted xattr
		var originalType uint32 = syscall.S_IFREG // Default to file
		if deletedAttr.OriginalType != 0 {
			originalType = deletedAttr.OriginalType
		} else {
			// Check source to get original type
			var srcSt syscall.Stat_t
			if err := syscall.Lstat(sourcePath, &srcSt); err == nil {
				originalType = uint32(srcSt.Mode & syscall.S_IFMT)
			}
		}
		// Set all required xattr fields per Phase 3.2 constraints
		if errno := n.typeReplacement.HandleTypeReplacement(cachePath, originalType, syscall.S_IFDIR); errno != 0 {
			// If setting fails, try to remove the old xattr
			xattr.Remove(cachePath)
		}

		existingChild := n.GetChild(name)
		var oldChildIno uint64
		if existingChild != nil {
			// Get old child inode number before removing it
			oldChildIno = existingChild.StableAttr().Ino
			// Remove the existing child inode completely from FUSE tree
			// This ensures NewInode will create a new inode, not reuse the old one
			n.RmChild(name)
		}
		// Invalidate the old entry name BEFORE creating new inode
		// This tells kernel to forget cached dentry for the old file
		n.InvalidateEntry(ctx, name)
		// Use DeleteNotify to invalidate the old child inode
		if oldChildIno != 0 {
			server := n.getServer()
			if server != nil {
				parentIno := n.EmbeddedInode().StableAttr().Ino
				server.DeleteNotify(parentIno, oldChildIno, name)
			}
		}
	} else {
		// Regular directory creation - ensure no old xattr exists
		xattr.Remove(cachePath)
		// Mark as independent if directory doesn't exist in source
		if !sourceExists {
			n.setCacheIndependent(cachePath)
		}
	}

	ch := n.NewInode(ctx, node, stableAttr)

	// CRITICAL: Ensure mirrorPath is set on the returned inode
	// NewInode may return a different inode than the one we created, so we need to set mirrorPath
	// on the actual inode that was added to the tree
	if chOps := ch.Operations(); chOps != nil {
		if chNode, ok := chOps.(*ShadowNode); ok {
			chNode.mirrorPath = cachePath
		}
	}

	// For type replacement, invalidate entry AFTER creating new inode
	// This ensures kernel sees the new directory entry immediately
	if typeReplacement {
		n.InvalidateEntry(ctx, name)
	}

	return ch, 0
}

func (n *ShadowNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate name parameter to prevent path traversal
	if !utils.ValidateName(name) {
		return nil, syscall.EPERM
	}

	// Get source path and cache path
	sourcePath := filepath.Join(n.FullPath(false), name)
	cachePath := n.RebasePathUsingCache(sourcePath)

	// Check if directory exists in cache
	var cacheSt syscall.Stat_t
	cacheExists := syscall.Lstat(cachePath, &cacheSt) == nil

	var typeReplacement bool
	var deletedAttr xattr.XAttr

	if cacheExists {
		// Exists in cache - first check if it's already a directory (fast path for idempotent mkdir)
		// Re-stat to ensure we have fresh data (avoid stale cache)
		if err := syscall.Lstat(cachePath, &cacheSt); err != nil {
			// Path no longer exists - treat as if it doesn't exist
			cacheExists = false
			// Fall through to creation logic below (skip the rest of this if block)
		} else if cacheSt.Mode&syscall.S_IFDIR != 0 {
			// It's a directory - check if it's marked as deleted
			attr := xattr.XAttr{}
			attrExists, errno := xattr.Get(cachePath, &attr)
			if errno != 0 && errno != syscall.ENODATA {
				return nil, errno
			}
			if !attrExists || !xattr.IsPathDeleted(attr) {
				// Directory exists and is not marked as deleted - idempotent mkdir
				return n.handleExistingDirectory(ctx, name, cachePath, out)
			}
			// Directory is marked as deleted - clean up and prepare for recreation
			var errno2 syscall.Errno
			typeReplacement, deletedAttr, errno2 = n.handleDeletedEntryInCache(cachePath, cacheSt)
			if errno2 != 0 {
				return nil, errno2
			}
			// typeReplacement is true - continue to create directory below
		} else {
			// Path exists but is not a directory - check if it's marked as deleted
			// If not deleted, this is an error (can't create directory where file exists)
			attr := xattr.XAttr{}
			attrExists, errno := xattr.Get(cachePath, &attr)
			if errno != 0 && errno != syscall.ENODATA {
				return nil, errno
			}
			if !attrExists || !xattr.IsPathDeleted(attr) {
				// Path exists and is not deleted - this is an error
				return nil, syscall.EEXIST
			}
			// Path is marked as deleted - clean up and prepare for recreation
			var errno2 syscall.Errno
			typeReplacement, deletedAttr, errno2 = n.handleDeletedEntryInCache(cachePath, cacheSt)
			if errno2 != 0 {
				return nil, errno2
			}
			// typeReplacement is true - continue to create directory below
		}
	}

	// Check if directory exists in source (for permissions and lazy COW)
	sourceExists, dirMode, sourceSt := n.checkSourceDirectory(sourcePath, mode)

	// If cache doesn't exist (or was deleted and needs recreation), create it
	if !cacheExists || typeReplacement {
		// Either doesn't exist in cache, or was deleted and needs recreation
		// If typeReplacement is true, path is already marked as independent - don't check source
		// Otherwise, check source for lazy COW - but only if source is actually a directory
		if !typeReplacement && sourceExists && sourceSt.Mode&syscall.S_IFDIR != 0 {
			// Directory exists in source - lazy COW (delegate to source)
			return n.handleLazyCOW(ctx, name, sourceSt, out)
		}

		dirMode = mode

		// Directory doesn't exist in source (or source is a file, or typeReplacement) - create in cache (independent)
		// BUT: Check if parent exists first (like normal mkdir)
		if errno := n.ensureParentDirectoryExists(cachePath, sourcePath); errno != 0 {
			return nil, errno
		}
	}

	// Safety check: if directory already exists, handle idempotent case
	// This can happen if there was a race condition or if we didn't detect it earlier
	var finalCacheSt syscall.Stat_t
	if err := syscall.Lstat(cachePath, &finalCacheSt); err == nil {
		if finalCacheSt.Mode&syscall.S_IFDIR != 0 {
			// Directory exists - idempotent mkdir
			return n.handleExistingDirectory(ctx, name, cachePath, out)
		} else {
			// Path exists but is not a directory - this is an error
			return nil, syscall.EEXIST
		}
	}

	// Create directory in cache with proper permissions and metadata
	st, errno := n.createDirectoryInCache(ctx, cachePath, dirMode)
	if errno != 0 {
		return nil, errno
	}

	// Set up FUSE inode with correct attributes and type replacement handling
	retInode, errno := n.setupDirectoryInode(ctx, name, cachePath, sourcePath, sourceExists, st, typeReplacement, deletedAttr, out)
	return retInode, errno
}

func (n *ShadowNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	// Validate name parameter to prevent path traversal
	if !utils.ValidateName(name) {
		return syscall.EPERM
	}

	dirPath := filepath.Join(n.FullPath(false), name)

	ds, err := NewShadowDirStream(n, dirPath)
	if err != 0 {
		return err
	}
	defer ds.Close()

	cnt := 0
	for ds.HasNext() && cnt == 0 {
		de, err := ds.Next()
		if err != 0 {
			return err
		}
		if de.Name == "." || de.Name == ".." {
			continue
		}
		cnt++
	}

	if cnt != 0 {
		return syscall.ENOTEMPTY
	}

	cacheName := n.RebasePathUsingCache(dirPath)

	// Get original directory type before marking as deleted
	var originalType uint32
	var srcSt syscall.Stat_t
	if err := syscall.Lstat(dirPath, &srcSt); err == nil {
		originalType = uint32(srcSt.Mode & syscall.S_IFMT)
	} else {
		// If source doesn't exist, check cache
		var cacheSt syscall.Stat_t
		if err := syscall.Lstat(cacheName, &cacheSt); err == nil {
			originalType = uint32(cacheSt.Mode & syscall.S_IFMT)
		}
		// If neither exists, originalType remains 0 (unknown)
	}

	// Check if directory exists in cache
	var cacheSt syscall.Stat_t
	dirExistsInCache := syscall.Lstat(cacheName, &cacheSt) == nil

	// Use permanent independence operation per Phase 3.3
	if n.deletion == nil {
		n.deletion = operations.NewDeletion()
	}
	// Mark as deleted with permanent independence
	if errno := n.deletion.HandlePermanentIndependence(cacheName); errno != 0 {
		return errno
	}
	// Update trie to mark path as independent
	if n.renameTracker != nil {
		// Convert cache path to mount point relative path
		mountPointPath := pathutil.RebaseToMountPoint(cacheName, n.mountPoint, n.cachePath)
		mountPointPath = strings.TrimPrefix(mountPointPath, n.mountPoint)
		mountPointPath = strings.TrimPrefix(mountPointPath, "/")
		if mountPointPath != "" {
			n.renameTracker.SetIndependent(mountPointPath)
		}
	}
	// Update original type in xattr (preserving PathStatusDeleted and CacheIndependent)
	attr := xattr.XAttr{}
	_, errno := xattr.Get(cacheName, &attr)
	if errno == 0 {
		attr.OriginalType = originalType
		// Ensure PathStatusDeleted is still set
		if attr.PathStatus != xattr.PathStatusDeleted {
			attr.PathStatus = xattr.PathStatusDeleted
		}
		// Ensure CacheIndependent is still set (all deleted items are permanently independent)
		if !attr.CacheIndependent {
			attr.CacheIndependent = true
		}
		if setErrno := xattr.Set(cacheName, &attr); setErrno != 0 {
			return setErrno
		}
	} else {
		// If xattr doesn't exist, set it with PathStatusDeleted, OriginalType, and CacheIndependent
		attr = xattr.XAttr{
			PathStatus:       xattr.PathStatusDeleted,
			OriginalType:     originalType,
			CacheIndependent: true, // All deleted items are permanently independent
		}
		if setErrno := xattr.Set(cacheName, &attr); setErrno != 0 {
			return setErrno
		}
	}

	// Commit directory deletion to git BEFORE removing it (best-effort, don't fail rmdir if commit fails)
	mountPointPath := pathutil.RebaseToMountPoint(cacheName, n.mountPoint, n.cachePath)
	if err := n.commitDirectoryDeletionRecursive(mountPointPath); err != nil {
		log.Printf("Rmdir: Failed to commit directory deletion for %s: %v (rmdir will continue)", mountPointPath, err)
		// Don't fail rmdir operation - commit is best-effort
	}

	// CRITICAL: Remove deletion marker files from directory before removing it
	// The kernel sees these files and considers the directory non-empty
	if dirExistsInCache {
		// List directory entries and remove any deletion marker files
		dirFd, err := syscall.Open(cacheName, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
		if err == nil {
			defer syscall.Close(dirFd)
			buf := make([]byte, 4096)
			for {
				n, err := syscall.Getdents(dirFd, buf)
				if n <= 0 {
					break
				}
				todo := buf[:n]
				for len(todo) > 0 {
					// dirent structure (same as in dirstream_linux.go)
					type dirent struct {
						Ino    uint64
						Off    int64
						Reclen uint16
						Type   uint8
						Name   [1]uint8
					}
					de := (*dirent)(unsafe.Pointer(&todo[0]))
					minSize := int(unsafe.Offsetof(dirent{}.Name))
					if de.Reclen < uint16(minSize) || int(de.Reclen) > len(todo) {
						break
					}
					nameBytes := todo[minSize:de.Reclen]
					end := 0
					for i, b := range nameBytes {
						if b == 0 {
							end = i
							break
						}
					}
					if end == 0 {
						todo = todo[de.Reclen:]
						continue
					}
					nameStr := string(nameBytes[:end])
					if nameStr != "." && nameStr != ".." {
						entryPath := filepath.Join(cacheName, nameStr)
						// Check if this is a deletion marker
						attr := xattr.XAttr{}
						if exists, errno := xattr.Get(entryPath, &attr); errno == 0 && exists && xattr.IsPathDeleted(attr) {
							// Remove deletion marker file
							syscall.Unlink(entryPath)
							xattr.Remove(entryPath)
						}
					}
					todo = todo[de.Reclen:]
				}
				if err != nil {
					break
				}
			}
		}
		// Now remove the directory
		if err := syscall.Rmdir(cacheName); err != nil && !os.IsNotExist(err) {
			return fs.ToErrno(err)
		}
	}

	n.InvalidateEntry(ctx, name)

	// CRITICAL: Remove child inode from FUSE tree to prevent stale entries
	n.RmChild(name)

	return 0
}
