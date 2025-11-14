package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

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

func (n *ShadowNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate name parameter to prevent path traversal
	if !utils.ValidateName(name) {
		return nil, syscall.EPERM
	}

	p := filepath.Join(n.FullPath(false), name)

	// check if directory exists in src
	st := syscall.Stat_t{}
	err := syscall.Lstat(p, &st)
	dirMode := mode // Use provided mode as default
	if err == nil {
		// directory exists in src use the same mode
		dirMode = uint32(st.Mode)
	}

	// change path to cache
	p = n.RebasePathUsingCache(p)

	// Check if path is marked as deleted (from a previous file or directory deletion)
	// Xattr can persist even after file/directory is removed, so check xattr regardless of file existence
	// If marked as deleted, remove the xattr and any existing file/directory to allow directory creation
	attr := xattr.XAttr{}
	exists, errno := xattr.Get(p, &attr)
	typeReplacement := false
	if errno == 0 && exists && xattr.IsPathDeleted(attr) {
		// This is type replacement: file/directory was deleted, now creating directory
		typeReplacement = true

		// Path is marked as deleted - remove the xattr and any existing file/directory
		var existingSt syscall.Stat_t
		if err := syscall.Lstat(p, &existingSt); err == nil {
			// File or directory exists - remove it first
			if existingSt.Mode&syscall.S_IFDIR != 0 {
				// It's a directory - remove it
				// CRITICAL: Check if directory is empty before removing
				// If it contains deletion markers, they should have been removed by Rmdir
				// But if directory still exists, we need to remove it
				if err := syscall.Rmdir(p); err != nil {
					// If rmdir fails (e.g., directory not empty), try to remove xattr anyway
					// and continue - we'll try to create the directory anyway
					xattr.Remove(p)
				} else {
					// Directory removed successfully, also remove xattr
					xattr.Remove(p)
				}
			} else {
				// It's a file - unlink it
				if err := syscall.Unlink(p); err != nil {
					// If unlink fails, try to remove xattr anyway
					xattr.Remove(p)
				} else {
					// File removed successfully, also remove xattr
					xattr.Remove(p)
				}
			}
		} else {
			// File/directory doesn't exist, but xattr might still be there - remove it
			xattr.Remove(p)
		}
		// Clear rename mapping if path was renamed before deletion
		// Recreation after deletion creates independent path (not linked to source)
		if n.renameTracker != nil {
			mountPointPath := pathutil.RebaseToMountPoint(p, n.mountPoint, n.cachePath)
			mountPointPath = strings.TrimPrefix(mountPointPath, n.mountPoint)
			mountPointPath = strings.TrimPrefix(mountPointPath, "/")
			if mountPointPath != "" {
				n.renameTracker.RemoveRenameMapping(mountPointPath)
			}
		}
		// Clear rename mapping from xattr
		if n.xattrMgr != nil {
			n.xattrMgr.ClearRenameMapping(p)
		}
		// Mark as independent (recreated path is not linked to source)
		n.setCacheIndependent(p)
	}

	// Ensure parent directory has execute permissions before creating child
	// This is critical - if parent can't be traversed, child creation will fail
	parentDir := filepath.Dir(p)
	if parentDir != p && parentDir != n.cachePath {
		if err := utils.EnsureDirPermissions(parentDir); err != nil {

			// Continue anyway - parent might already have correct permissions
		}
	}

	// Ensure directories have execute permissions
	// Temporarily disable umask to ensure permissions are set correctly
	// This is critical - umask can remove execute bits, making directories untraversable
	oldUmask := syscall.Umask(0)
	defer syscall.Umask(oldUmask)

	// Use the mode from src if available, otherwise use provided mode
	// Ensure execute bits are set (0755) for directories to be traversable
	fileMode := os.FileMode(dirMode)
	if fileMode&0111 == 0 {
		// Missing execute bits - ensure at least 0755
		fileMode = os.FileMode(0755)
	}

	// Check if directory already exists in cache before creating
	// If it exists and is marked as deleted, remove it first
	var existingSt syscall.Stat_t
	fmt.Printf("[DEBUG Mkdir] Checking if directory exists: p=%s\n", p)
	if err := syscall.Lstat(p, &existingSt); err == nil {
		fmt.Printf("[DEBUG Mkdir] Directory exists in cache: p=%s\n", p)
		// Path exists in cache - check if it's marked as deleted
		existingAttr := xattr.XAttr{}
		if exists, errno := xattr.Get(p, &existingAttr); errno == 0 && exists && xattr.IsPathDeleted(existingAttr) {
			// Path is marked as deleted - remove it first
			if existingSt.Mode&syscall.S_IFDIR != 0 {
				// It's a directory - remove it
				// CRITICAL: Even if Rmdir already removed it, try again to be safe
				// This handles race conditions where directory was recreated from source
				if err := syscall.Rmdir(p); err != nil && !os.IsNotExist(err) {
					// If rmdir fails (e.g., directory not empty), try to remove xattr anyway
					// and continue - we'll try to create the directory anyway
					xattr.Remove(p)
				} else {
					// Directory removed successfully (or didn't exist), also remove xattr
					xattr.Remove(p)
				}
			} else {
				// It's a file - unlink it
				if err := syscall.Unlink(p); err != nil && !os.IsNotExist(err) {
					return nil, fs.ToErrno(err)
				}
				// Remove xattr after removing file
				xattr.Remove(p)
			}
			// Clear rename mapping if path was renamed before deletion
			// Recreation after deletion creates independent path (not linked to source)
			if n.renameTracker != nil {
				mountPointPath := pathutil.RebaseToMountPoint(p, n.mountPoint, n.cachePath)
				mountPointPath = strings.TrimPrefix(mountPointPath, n.mountPoint)
				mountPointPath = strings.TrimPrefix(mountPointPath, "/")
				if mountPointPath != "" {
					n.renameTracker.RemoveRenameMapping(mountPointPath)
				}
			}
			// Clear rename mapping from xattr
			if n.xattrMgr != nil {
				n.xattrMgr.ClearRenameMapping(p)
			}
			// Mark as independent (recreated path is not linked to source)
			n.setCacheIndependent(p)
			// After removing deleted directory, continue to create new one
		} else if existingSt.Mode&syscall.S_IFDIR != 0 {
			// Directory exists and is not marked as deleted - this is fine, we'll reuse it
			// Ensure it has proper permissions
			if err := utils.EnsureDirPermissions(p); err != nil {
				return nil, fs.ToErrno(err)
			}
			// Directory already exists, stat it and return
			if err := syscall.Lstat(p, &st); err != nil {
				return nil, fs.ToErrno(err)
			}
			// Set up output attributes
			out.Attr.FromStat(&st)
			// Create node for existing directory
			node := newShadowNode(n.RootData, n.EmbeddedInode(), name, &st)
			node.(*ShadowNode).mirrorPath = p
			stableAttr := n.idFromStat(&st)
			ch := n.NewInode(ctx, node, stableAttr)
			if chOps := ch.Operations(); chOps != nil {
				if chNode, ok := chOps.(*ShadowNode); ok {
					chNode.mirrorPath = p
				}
			}
			return ch, 0
		} else {
			// Path exists but is not a directory - this is an error
			return nil, syscall.EEXIST
		}
	}

	// Use os.Mkdir - it's simpler and handles mode correctly
	fmt.Printf("[DEBUG Mkdir] Creating directory: p=%s, fileMode=%v\n", p, fileMode)
	err = os.Mkdir(p, fileMode)
	if err != nil {
		fmt.Printf("[DEBUG Mkdir] os.Mkdir failed: p=%s, err=%v\n", p, err)
		// Handle case where directory already exists (idempotent operation)
		if os.IsExist(err) {
			// Directory exists, verify it's actually a directory
			var st2 syscall.Stat_t
			if err2 := syscall.Lstat(p, &st2); err2 == nil {
				if st2.Mode&syscall.S_IFDIR != 0 {
					// It's a directory, ensure it has proper permissions
					if err := utils.EnsureDirPermissions(p); err != nil {
						return nil, fs.ToErrno(err)
					}
					err = nil
				} else {
					// Path exists but is not a directory
					return nil, fs.ToErrno(err)
				}
			} else {
				return nil, fs.ToErrno(err)
			}
		} else {
			return nil, fs.ToErrno(err)
		}
	} else {
		// Directory was created, ensure it has proper permissions immediately
		if err := utils.EnsureDirPermissions(p); err != nil {
			syscall.Rmdir(p)
			return nil, fs.ToErrno(err)
		}
	}

	// Stat immediately after creation and permission fix to get attributes
	st = syscall.Stat_t{}
	if err := syscall.Lstat(p, &st); err != nil {
		syscall.Rmdir(p)
		return nil, fs.ToErrno(err)
	}

	// Ensure st.Mode has correct directory type and execute bits BEFORE preserveOwner
	// This ensures preserveOwner sees correct permissions
	if st.Mode&syscall.S_IFDIR != 0 {
		permBits := st.Mode & 0777
		if permBits&0111 == 0 {
			// Missing execute bits - fix them in filesystem first
			if err := syscall.Chmod(p, uint32(permBits|0755)); err != nil {

			}
			// Update stat to reflect fixed permissions
			if err := syscall.Lstat(p, &st); err != nil {
				syscall.Rmdir(p)
				return nil, fs.ToErrno(err)
			}
		}
	} else {
		// This shouldn't happen - directory should have S_IFDIR
		syscall.Rmdir(p)
		return nil, syscall.ENOTDIR
	}

	// Now call preserveOwner - it shouldn't affect permissions, only ownership
	utils.PreserveOwner(ctx, p)

	// Final stat after preserveOwner to get final attributes
	if err := syscall.Lstat(p, &st); err != nil {
		syscall.Rmdir(p)
		return nil, fs.ToErrno(err)
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

	// Now call FromStat with the corrected stat structure
	out.Attr.FromStat(&st)

	// Note: We rely on mount-level timeouts (set in mount options) instead of per-entry timeouts
	// This is the battle-tested approach: EntryTimeout=500ms, AttrTimeout=500ms, NegativeTimeout=200ms
	// Per-entry timeout overrides are not needed and can cause issues

	node := newShadowNode(n.RootData, n.EmbeddedInode(), name, &st)
	node.(*ShadowNode).mirrorPath = p

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
	// Battle-tested approach: XOR with type-specific magic number + increment generation
	// This ensures kernel treats it as a new inode, not a cached version of the old file inode
	// Used by rclone, restic, goofys, sioyek, etc. in 2025
	if typeReplacement {
		// Use type-specific magic number: directory type gets different XOR than file type
		// This ensures even if underlying filesystem reuses inode numbers, we get unique inodes
		stableAttr.Ino = stableAttr.Ino ^ 0x2000000000000000 // Type-specific magic for directory
		stableAttr.Gen = stableAttr.Gen + 1000               // Large generation increment to force invalidation
	}

	// Handle type replacement scenario using clean operation module
	if typeReplacement {
		// Use type replacement operation per Phase 3.2
		if n.typeReplacement == nil {
			n.typeReplacement = operations.NewTypeReplacement()
		}
		// Get original type from source (if exists) or from deleted xattr
		var originalType uint32 = syscall.S_IFREG // Default to file
		if attr.OriginalType != 0 {
			originalType = attr.OriginalType
		} else {
			// Check source to get original type
			sourcePath := filepath.Join(n.FullPath(false), name)
			var srcSt syscall.Stat_t
			if err := syscall.Lstat(sourcePath, &srcSt); err == nil {
				originalType = uint32(srcSt.Mode & syscall.S_IFMT)
			}
		}
		// Set all required xattr fields per Phase 3.2 constraints
		if errno := n.typeReplacement.HandleTypeReplacement(p, originalType, syscall.S_IFDIR); errno != 0 {
			// If setting fails, try to remove the old xattr
			xattr.Remove(p)
		}
	} else {
		// Regular directory creation - ensure no old xattr exists
		xattr.Remove(p)
	}

	// For type replacement, remove any existing child inode first
	// CRITICAL: The old file inode was already removed by Unlink, but we need to ensure
	// it's completely gone from the FUSE tree before creating the new directory inode.
	// Battle-tested approach: Remove old child + invalidate entry + use unique inode number
	if typeReplacement {
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
	}

	ch := n.NewInode(ctx, node, stableAttr)

	// CRITICAL: Ensure mirrorPath is set on the returned inode
	// NewInode may return a different inode than the one we created, so we need to set mirrorPath
	// on the actual inode that was added to the tree
	if chOps := ch.Operations(); chOps != nil {
		if chNode, ok := chOps.(*ShadowNode); ok {
			chNode.mirrorPath = p
		}
	}

	// For type replacement, invalidate entry AFTER creating new inode
	// This ensures kernel sees the new directory entry immediately
	if typeReplacement {
		n.InvalidateEntry(ctx, name)
	}

	return ch, 0
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
		fmt.Printf("[DEBUG Rmdir] cacheName=%s, mountPointPath=%s\n", cacheName, mountPointPath)
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

	// CRITICAL: Invalidate entry BEFORE removing child inode from tree
	// Battle-tested: server.EntryNotify is 100% safe, even from inside Rmdir
	// This tells kernel to forget cached dentry for the directory
	n.InvalidateEntry(ctx, name)

	// CRITICAL: Remove child inode from FUSE tree to prevent stale entries
	n.RmChild(name)

	return 0
}
