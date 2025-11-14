//go:build linux
// +build linux

package fs

// Copyright 2019 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

import (
	"context"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/pleclech/shadowfs/fs/xattr"
)

type step int

const (
	stepNone step = iota
	stepCache
	stepOrigin
)

type ShadowDirStream struct {
	buf       []byte
	todo      []byte
	todoErrno syscall.Errno

	root       *ShadowNode
	originPath string
	cachePath  string

	nextDirEntry *fuse.DirEntry

	// Protects fd so we can guard against double close
	mu   sync.Mutex
	fd   int
	step step
	seen map[string]struct{}
}

// NewShadowDirStream open a directory for reading as a DirStream
func NewShadowDirStream(root *ShadowNode, originPath string) (fs.DirStream, syscall.Errno) {
	if originPath == "" {
		originPath = root.FullPath(false)
	}

	cachePath := root.RebasePathUsingCache(originPath)
	return NewShadowDirStreamWithPaths(root, cachePath, originPath)
}

// NewShadowDirStreamWithPaths creates a DirStream with explicit cache and source paths
// This is used by directoryOp.Readdir to pass resolved paths (for renamed directories)
func NewShadowDirStreamWithPaths(root *ShadowNode, cachePath, sourcePath string) (fs.DirStream, syscall.Errno) {
	// check if the directory is not deleted in cache
	attr := xattr.XAttr{}
	xattr.Get(cachePath, &attr)
	if xattr.IsPathDeleted(attr) {
		// directory is deleted
		return nil, syscall.ENOENT
	}

	ds := &ShadowDirStream{
		buf:        make([]byte, 4096),
		fd:         -1,
		root:       root,
		originPath: sourcePath, // Use resolved source path (may be original path for renamed dirs)
		cachePath:  cachePath,  // Use provided cache path
		seen:       make(map[string]struct{}),
	}

	if err := ds.cacheStep(); err != 0 {
		ds.originStep()
	}

	return ds, fs.OK
}

func (ds *ShadowDirStream) setStep(step step) syscall.Errno {
	if ds.step == step {
		return fs.OK
	}

	if ds.fd != -1 {
		syscall.Close(ds.fd)
		ds.fd = -1
	}

	ds.step = step
	ds.todoErrno = 0
	ds.todo = nil
	ds.nextDirEntry = nil

	var path string
	if ds.step == stepCache {
		path = ds.cachePath
	} else {
		path = ds.originPath
	}

	fd, err := syscall.Open(path, syscall.O_DIRECTORY, 0755)
	ds.fd = fd

	if err != nil {
		if ds.fd != -1 {
			syscall.Close(ds.fd)
			ds.fd = -1
		}
		ds.todoErrno = fs.ToErrno(err)
		return ds.todoErrno
	}

	ds.load()

	return fs.OK
}

func (ds *ShadowDirStream) cacheStep() syscall.Errno {
	return ds.setStep(stepCache)
}

func (ds *ShadowDirStream) originStep() syscall.Errno {
	return ds.setStep(stepOrigin)
}

func (ds *ShadowDirStream) Close() {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if ds.fd != -1 {
		syscall.Close(ds.fd)
		ds.fd = -1
	}
	// clear seen map
	ds.seen = nil
}

func (ds *ShadowDirStream) HasNext() bool {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	// Check if we already have a pending entry
	if ds.nextDirEntry != nil {
		return true
	}

	// Process cache entries first
	if ds.step == stepCache {
		if ds.processCacheEntries() {
			return true
		}
		// Switch to origin step when cache is exhausted
		ds.originStep()
	}

	// Process origin entries
	return ds.processOriginEntries()
}

// processCacheEntries processes entries from cache directory
func (ds *ShadowDirStream) processCacheEntries() bool {
	for len(ds.todo) > 0 {
		next, errno := ds.next()
		if errno != 0 || next.Name == "" {
			return false
		}

		// Handle . and .. entries with deduplication
		if next.Name == "." || next.Name == ".." {
			if _, seen := ds.seen[next.Name]; !seen {
				ds.seen[next.Name] = struct{}{}
				ds.nextDirEntry = &next
				return true
			}
			// Skip duplicate . or .. entries
			continue
		}

		// Check if deleted BEFORE adding to seen
		// Cache priority: deleted entries in cache override source entries
		if ds.isDeleted(next.Name) {
			// Mark as seen so source entry is also skipped
			ds.seen[next.Name] = struct{}{}
			// Skip deleted entries from cache
			continue
		}

		// Not deleted - add to seen and return
		ds.seen[next.Name] = struct{}{}
		ds.nextDirEntry = &next
		return true
	}
	return false
}

// processOriginEntries processes entries from source directory
func (ds *ShadowDirStream) processOriginEntries() bool {
	for {
		// Load more data if needed
		if len(ds.todo) == 0 {
			if errno := ds.load(); errno != 0 {
				return false
			}
			if len(ds.todo) == 0 {
				return false
			}
		}

		next, errno := ds.next()
		if errno != 0 || next.Name == "" {
			return false
		}

		// Handle . and .. entries with deduplication
		if next.Name == "." || next.Name == ".." {
			if _, seen := ds.seen[next.Name]; !seen {
				ds.seen[next.Name] = struct{}{}
				ds.nextDirEntry = &next
				return true
			}
			// Skip duplicate . or .. entries
			continue
		}

		// Check if this source entry is marked as deleted in cache
		// Cache priority: if deleted in cache, don't show from source
		if ds.isDeleted(next.Name) {
			// Skip deleted entries from source
			continue
		}

		if _, seen := ds.seen[next.Name]; !seen {
			ds.seen[next.Name] = struct{}{}
			ds.nextDirEntry = &next
			return true
		}
	}
}

// isDeleted checks if a file/directory is marked as deleted in cache
func (ds *ShadowDirStream) isDeleted(name string) bool {
	fullPath := filepath.Join(ds.cachePath, name)
	
	// List all xattrs to check if our xattr exists
	allAttrs, listErrno := xattr.List(fullPath)
	
	// Check if our xattr name is in the list
	hasShadowXattr := false
	if listErrno == 0 {
		for _, attrName := range allAttrs {
			if attrName == xattr.Name {
				hasShadowXattr = true
				break
			}
		}
	}
	
	// If we have the xattr in the list, try to read it
	if hasShadowXattr {
		attr := xattr.XAttr{}
		exists, errno := xattr.Get(fullPath, &attr)
		if errno == 0 && exists {
			return xattr.IsPathDeleted(attr)
		}
	}
	
	// Fallback to normal check
	attr := xattr.XAttr{}
	exists, errno := xattr.Get(fullPath, &attr)
	// If xattr exists and is marked as deleted, return true
	if errno == 0 && exists {
		return xattr.IsPathDeleted(attr)
	}
	
	// If there's an error reading xattr (file doesn't exist or xattr doesn't exist), it's not deleted
	// Note: We check xattr even if file doesn't exist physically, because deletion markers
	// are files with xattr. If the file doesn't exist and has no xattr, it's not deleted.
	return false
}

// Like syscall.Dirent, but without the [256]byte name.
type dirent struct {
	Ino    uint64
	Off    int64
	Reclen uint16
	Type   uint8
	Name   [1]uint8 // align to 4 bytes for 32 bits.
}

func (ds *ShadowDirStream) next() (fuse.DirEntry, syscall.Errno) {
	if ds.todoErrno != 0 {
		return fuse.DirEntry{}, ds.todoErrno
	}

	if ds.nextDirEntry != nil {
		e := *ds.nextDirEntry
		ds.nextDirEntry = nil
		return e, fs.OK
	}

	// Minimum size for a dirent structure
	minDirentSize := int(unsafe.Offsetof(dirent{}.Name))

	// Ensure we have enough data for a dirent structure
	if len(ds.todo) < minDirentSize {
		if errno := ds.load(); errno != 0 {
			return fuse.DirEntry{}, errno
		}
		if len(ds.todo) < minDirentSize {
			return fuse.DirEntry{}, fs.OK
		}
	}

	de := (*dirent)(unsafe.Pointer(&ds.todo[0]))

	// Validate Reclen
	if de.Reclen < uint16(minDirentSize) || int(de.Reclen) > len(ds.todo) {
		return fuse.DirEntry{}, syscall.EIO
	}

	// Extract name
	nameBytes := ds.todo[unsafe.Offsetof(dirent{}.Name):de.Reclen]
	// Find null terminator
	end := 0
	for i, b := range nameBytes {
		if b == 0 {
			end = i
			break
		}
	}
	nameStr := string(nameBytes[:end])

	if nameStr == "" {
		// Skip empty entry
		ds.todo = ds.todo[de.Reclen:]
		return ds.next()
	}

	// Advance buffer past this entry
	ds.todo = ds.todo[de.Reclen:]

	// Get mode from filesystem for accuracy
	var mode uint32
	fullPath := filepath.Join(ds.getCurrentPath(), nameStr)
	var st syscall.Stat_t
	if err := syscall.Lstat(fullPath, &st); err == nil {
		mode = st.Mode
	} else {
		// Fallback to dirent type
		if de.Type == 4 { // DT_DIR
			mode = 0040755
		} else {
			mode = (uint32(de.Type) << 12)
		}
	}

	return fuse.DirEntry{
		Ino:  de.Ino,
		Mode: mode,
		Name: nameStr,
	}, fs.OK
}

// getCurrentPath returns the appropriate path based on current step
func (ds *ShadowDirStream) getCurrentPath() string {
	if ds.step == stepCache {
		return ds.cachePath
	}
	return ds.originPath
}

func (ds *ShadowDirStream) Next() (fuse.DirEntry, syscall.Errno) {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	// Check if we already have a pending entry
	if ds.nextDirEntry != nil {
		e := *ds.nextDirEntry
		ds.nextDirEntry = nil
		return e, fs.OK
	}

	// Process cache entries first
	if ds.step == stepCache {
		if ds.processCacheEntries() {
			if ds.nextDirEntry != nil {
				e := *ds.nextDirEntry
				ds.nextDirEntry = nil
				return e, fs.OK
			}
		}
		// Switch to origin step when cache is exhausted
		ds.originStep()
	}

	// Process origin entries
	if ds.processOriginEntries() {
		if ds.nextDirEntry != nil {
			e := *ds.nextDirEntry
			ds.nextDirEntry = nil
			return e, fs.OK
		}
	}

	return fuse.DirEntry{}, fs.OK
}

// Readdirent is called by go-fuse for READDIRPLUS operations
// It calls HasNext() and Next() internally
func (ds *ShadowDirStream) Readdirent(ctx context.Context) (*fuse.DirEntry, syscall.Errno) {
	if !ds.HasNext() {
		return nil, 0
	}
	de, errno := ds.Next()
	if errno != 0 {
		return nil, errno
	}
	return &de, errno
}

func (ds *ShadowDirStream) load() syscall.Errno {
	if len(ds.todo) > 0 {
		return fs.OK
	}

	n, err := syscall.Getdents(ds.fd, ds.buf)
	if n < 0 {
		n = 0
	}

	ds.todo = ds.buf[:n]
	ds.todoErrno = fs.ToErrno(err)

	return fs.OK
}
