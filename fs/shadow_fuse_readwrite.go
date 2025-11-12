package fs

import (
	"context"
	"strings"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/pleclech/shadowfs/fs/cache"
)

func (n *ShadowNode) CopyFileRange(ctx context.Context, fhIn fs.FileHandle, offIn uint64, out *fs.Inode, fhOut fs.FileHandle, offOut uint64, copyLen uint64, flags uint32) (uint32, syscall.Errno) {
	// For now, implement a basic copy using the file handles
	// This is a simplified implementation that should work for Git's needs

	// Read from input file handle
	reader, ok := fhIn.(fs.FileReader)
	if !ok {
		return 0, syscall.EBADF
	}
	writer, ok := fhOut.(fs.FileWriter)
	if !ok {
		return 0, syscall.EBADF
	}

	// Use buffer pool to reduce allocations
	bufPtr, ok := cache.GetBufferPool().Get().(*[]byte)
	if !ok {
		return 0, syscall.ENOMEM
	}
	defer cache.GetBufferPool().Put(bufPtr)
	buf := *bufPtr

	var totalCopied uint64

	for totalCopied < copyLen {
		// Read chunk from input
		result, status := reader.Read(ctx, buf, int64(offIn+totalCopied))
		if status != 0 {
			return uint32(totalCopied), status
		}
		bytesRead, _ := result.Bytes(buf)
		if len(bytesRead) == 0 {
			break
		}

		// Write chunk to output
		_, writeStatus := writer.Write(ctx, bytesRead, int64(offOut+totalCopied))
		if writeStatus != 0 {
			return uint32(totalCopied), writeStatus
		}

		totalCopied += uint64(len(bytesRead))
	}

	return uint32(totalCopied), 0
}

func (n *ShadowNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	p := n.FullPath(true)

	// Copy-on-write: if file is in cache but empty, and source exists, copy source content first
	// Use Lstat() on path for copy-on-write check (path-based, no reflection overhead)
	if strings.HasPrefix(p, n.cachePath) && off == 0 {
		var cacheSt syscall.Stat_t
		if err := syscall.Lstat(p, &cacheSt); err == nil && cacheSt.Size == 0 {
			// Cache file is empty - check if source has content to copy
			sourcePath := n.RebasePathUsingSrc(p)
			var sourceSt syscall.Stat_t
			if err := syscall.Lstat(sourcePath, &sourceSt); err == nil && sourceSt.Size > 0 {
				// Source file exists and has content - copy it to cache before writing

				if n.helpers != nil {
					if errno := n.helpers.CopyFile(sourcePath, p); errno != 0 {
						return 0, errno
					}
				} else {
					if errno := n.copyFileSimple(sourcePath, p, sourceSt.Mode); errno != 0 {
						return 0, errno
					}
				}
			}
		}
	}

	written, errno := func() (uint32, syscall.Errno) {
		if fw, ok := f.(fs.FileWriter); ok {
			return fw.Write(ctx, data, off)
		}
		return 0, syscall.EBADF
	}()
	if errno == 0 {
		// CRITICAL: After successful write, ensure file metadata is updated
		// This prevents git from seeing empty files immediately after write
		// Try to sync the file to ensure data is written and size is updated
		if fileWithFd, ok := f.(interface{ Fd() int }); ok {
			if fd := fileWithFd.Fd(); fd >= 0 {
				// Force sync to ensure data is written to disk
				syscall.Fsync(fd)
				// Update file size by doing fstat
				var st syscall.Stat_t
				if err := syscall.Fstat(fd, &st); err == nil {
					// File size is now properly updated

				}
			}
		}

		// Track write activity for Git auto-versioning
		n.HandleWriteActivity(p)
	}
	return written, errno
}

func (n *ShadowNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if fr, ok := f.(fs.FileReader); ok {
		return fr.Read(ctx, dest, off)
	}
	return nil, syscall.EBADF
}

