package fs

import (
	"context"
	"path/filepath"
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
	
	// Resolve to cache path if needed (same logic as Open())
	cachePath := p
	if !strings.HasPrefix(p, n.cachePath) {
		cachePath = n.RebasePathUsingCache(p)
	} else {
		cachePath = p
	}

	// Copy-on-write: if file is in cache but not independent, and source exists, copy source content first
	// Principle: If file exists in cache and is NOT independent (CacheIndependent = false) and also present in source,
	// we must copy the whole content from source and make it independent BEFORE any write (regardless of offset)
	// This is critical for patching files at non-zero offsets - we need the full source content first
	// BUT: Skip COW if file was just truncated (O_TRUNC was used) - user wants to overwrite
	// Lazy COW: we only copy when we need to write
	if strings.HasPrefix(cachePath, n.cachePath) {
		// CRITICAL: Check if this file was recently truncated FIRST
		// If file was truncated, it's intentionally empty - NEVER do COW
		// This prevents overwriting two different contents (e.g., "cache1" overwriting "source1" partially)
		wasTruncated := false
		if n.truncatedFiles != nil {
			// Check multiple path variations to ensure we find the truncated file
			// Try all possible path combinations since Open() and Write() might use different paths
			wasTruncated = n.truncatedFiles[cachePath] || n.truncatedFiles[p]
			if !wasTruncated {
				// Try rebasing p to cache to see if it matches cachePath
				rebasedCachePath := n.RebasePathUsingCache(p)
				wasTruncated = n.truncatedFiles[rebasedCachePath]
			}
			if !wasTruncated {
				// Try rebasing cachePath to mount point to see if it matches p
				rebasedMountPath := n.RebasePathUsingMountPoint(cachePath)
				wasTruncated = n.truncatedFiles[rebasedMountPath]
			}
			// Remove from map after first write (COW won't happen again)
			if wasTruncated {
				delete(n.truncatedFiles, cachePath)
				delete(n.truncatedFiles, p)
				rebasedCachePath := n.RebasePathUsingCache(p)
				delete(n.truncatedFiles, rebasedCachePath)
				rebasedMountPath := n.RebasePathUsingMountPoint(cachePath)
				delete(n.truncatedFiles, rebasedMountPath)
			}
		}
		
		// CRITICAL: If file was truncated with O_TRUNC, NEVER do COW - user wants to overwrite
		// A truncated file should remain empty until we write to it
		if !wasTruncated {
			// Check if file exists in cache (on disk)
			var cacheSt syscall.Stat_t
			fileExistsInCache := syscall.Lstat(cachePath, &cacheSt) == nil
			
			if fileExistsInCache {
				// File exists in cache - check if it's independent
				// If not independent and source exists, copy source content and make it independent
				// CRITICAL: This must happen BEFORE any write, regardless of offset
				// If user wants to patch at offset 5, we need the full source content (bytes 0-4) first
				isIndependent := false
				if n.xattrMgr != nil {
					attr, exists, errno := n.xattrMgr.GetStatus(cachePath)
					if errno == 0 {
						// Check if file is independent (CacheIndependent = true)
						isIndependent = exists && attr != nil && attr.CacheIndependent
					}
				}
				
				if !isIndependent {
					// File is not independent - check if source exists
					sourcePath := n.RebasePathUsingSrc(cachePath)
					var sourceSt syscall.Stat_t
					if err := syscall.Lstat(sourcePath, &sourceSt); err == nil {
						// Source file exists - copy it to cache before writing/appending/patching
						// This is lazy COW: we only copy when we need to write
						// Principle: If file is in cache and is not independent and is also present in source,
						// we must copy the whole content from source and make it independent
						// This ensures patching at non-zero offsets works correctly
						if n.helpers != nil {
							if errno := n.helpers.CopyFile(sourcePath, cachePath); errno != 0 {
								return 0, errno
							}
						} else {
							if errno := n.copyFileSimple(sourcePath, cachePath, uint32(sourceSt.Mode)); errno != 0 {
								return 0, errno
							}
						}
						// Once copied, file becomes independent of source
						n.setCacheIndependent(cachePath)
					}
				}
				// If file is already independent, no COW needed
			} else {
				// File doesn't exist in cache yet - this is first write/append/patch
				// Check if source has content to copy (lazy COW on first write)
				// Same principle: copy source content first, then write/append/patch
				// CRITICAL: This ensures patching at non-zero offsets works correctly
				sourcePath := n.RebasePathUsingSrc(cachePath)
				var sourceSt syscall.Stat_t
				if err := syscall.Lstat(sourcePath, &sourceSt); err == nil && sourceSt.Size > 0 {
					// Source file exists and has content - copy it to cache before writing/appending/patching
					// This is lazy COW: we only copy when we need to write
					// Once copied, file becomes independent of source
					if n.helpers != nil {
						if errno := n.helpers.CopyFile(sourcePath, cachePath); errno != 0 {
							return 0, errno
						}
					} else {
						if errno := n.copyFileSimple(sourcePath, cachePath, sourceSt.Mode); errno != 0 {
							return 0, errno
						}
					}
					// Mark as independent after copying
					n.setCacheIndependent(cachePath)
				}
			}
		}
		// If wasTruncated is true, file was intentionally truncated with O_TRUNC - don't do COW
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
				// Also sync parent directory to ensure directory entry is written to disk
				// This is especially important in daemon mode where timing can be critical
				if dirFd, err := syscall.Open(filepath.Dir(p), syscall.O_RDONLY, 0); err == nil {
					syscall.Fsync(dirFd)
					syscall.Close(dirFd)
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

