//go:build linux
// +build linux

package fs

import (
	"context"
	"path/filepath"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"golang.org/x/sys/unix"
)

func (n *ShadowNode) Getxattr(ctx context.Context, attr string, dest []byte) (uint32, syscall.Errno) {
	p := n.FullPath(true)
	sz, err := unix.Lgetxattr(p, attr, dest)
	return uint32(sz), fs.ToErrno(err)
}

func (n *ShadowNode) renameExchange(path string, newparent fs.InodeEmbedder, newPath string) syscall.Errno {
	fd1, err := syscall.Open(filepath.Dir(path), syscall.O_DIRECTORY, 0)
	if err != nil {
		return fs.ToErrno(err)
	}
	defer syscall.Close(fd1)

	fd2, err := syscall.Open(filepath.Dir(newPath), syscall.O_DIRECTORY, 0)
	defer syscall.Close(fd2)
	if err != nil {
		return fs.ToErrno(err)
	}

	var st syscall.Stat_t
	if err := syscall.Fstat(fd1, &st); err != nil {
		return fs.ToErrno(err)
	}

	// Double check that nodes didn't change from under us.
	inode := &n.Inode
	if inode.Root() != inode && inode.StableAttr().Ino != n.idFromStat(&st).Ino {
		return syscall.EBUSY
	}
	if err := syscall.Fstat(fd2, &st); err != nil {
		return fs.ToErrno(err)
	}

	newinode := newparent.EmbeddedInode()
	if newinode.Root() != newinode && newinode.StableAttr().Ino != n.idFromStat(&st).Ino {
		return syscall.EBUSY
	}

	return fs.ToErrno(unix.Renameat2(fd1, filepath.Base(path), fd2, filepath.Base(newPath), unix.RENAME_EXCHANGE))
}
