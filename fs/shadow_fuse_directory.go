package fs

import (
	"context"
	"os"
	"path/filepath"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/pleclech/shadowfs/fs/utils"
	"github.com/pleclech/shadowfs/fs/xattr"
)

func (n *ShadowNode) Opendir(ctx context.Context) syscall.Errno {
	fd, err := syscall.Open(n.FullPath(true), syscall.O_DIRECTORY, 0755)
	if err != nil {
		return fs.ToErrno(err)
	}
	utils.PreserveOwner(ctx, n.FullPath(true))
	syscall.Close(fd)
	return fs.OK
}

func (n *ShadowNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	return NewShadowDirStream(n, "")
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
	if err == nil {
		// directory exists in src use the same mode
		mode = uint32(st.Mode)
	}

	// change path to cache
	p = n.RebasePathUsingCache(p)

	// Check if path exists and is marked as deleted (from a previous file deletion)
	// If so, remove the deleted file and xattr to allow directory creation
	var existingSt syscall.Stat_t
	if err := syscall.Lstat(p, &existingSt); err == nil {
		// Path exists - check if it's marked as deleted
		attr := xattr.XAttr{}
		exists, errno := xattr.Get(p, &attr)
		if errno == 0 && exists && xattr.IsPathDeleted(attr) {
			// Path exists and is marked as deleted - remove it to allow directory creation
			// Remove the xattr first
			syscall.Removexattr(p, xattr.Name)
			// Remove the file
			if err := syscall.Unlink(p); err != nil {
				// If unlink fails, continue anyway - might be a directory already
				// The subsequent Mkdir will handle it
			}
		}
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
	// Temporarily disable umask to ensure 0755 permissions are set correctly
	// This is critical - umask can remove execute bits, making directories untraversable
	oldUmask := syscall.Umask(0)
	defer syscall.Umask(oldUmask)

	// Always use 0755 for directories to ensure they're traversable
	dirMode := os.FileMode(0755)

	// Use os.Mkdir - it's simpler and handles mode correctly
	err = os.Mkdir(p, dirMode)
	if err != nil {
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
			if err := syscall.Chmod(p, permBits|0755); err != nil {

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

	ch := n.NewInode(ctx, node, stableAttr)

	// Don't invalidate cache immediately after creation - this can cause issues
	// n.NotifyEntry(name)

	return ch, 0
}

func (n *ShadowNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	// Validate name parameter to prevent path traversal
	if !utils.ValidateName(name) {
		return syscall.EPERM
	}

	name = filepath.Join(n.FullPath(false), name)

	ds, err := NewShadowDirStream(n, name)
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

	cacheName := n.RebasePathUsingCache(name)

	// set new xattr to indicate file is deleted
	attr := xattr.XAttr{}
	_, errno := xattr.Get(cacheName, &attr)
	if errno != 0 {
		return errno
	}
	attr.PathStatus |= xattr.PathStatusDeleted
	return xattr.Set(cacheName, &attr)
}

