//go:build darwin
// +build darwin

package fs

// UNTESTED: macOS implementation - requires testing on macOS hardware

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"

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
	todo      []os.DirEntry
	todoIndex int
	todoErrno syscall.Errno

	root       *ShadowNode
	originPath string
	cachePath  string

	nextDirEntry *fuse.DirEntry

	// Protects fd so we can guard against double close
	mu   sync.Mutex
	dir  *os.File
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
		dir:        nil,
		root:       root,
		originPath: sourcePath, // Use resolved source path (may be original path for renamed dirs)
		cachePath:  cachePath,  // Use provided cache path
		seen:       make(map[string]struct{}),
		todoIndex:  0,
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

	if ds.dir != nil {
		ds.dir.Close()
		ds.dir = nil
	}

	ds.step = step
	ds.todoErrno = 0
	ds.todo = nil
	ds.todoIndex = 0
	ds.nextDirEntry = nil

	var path string
	if ds.step == stepCache {
		path = ds.cachePath
	} else {
		path = ds.originPath
	}

	dir, err := os.Open(path)
	ds.dir = dir

	if err != nil {
		if ds.dir != nil {
			ds.dir.Close()
			ds.dir = nil
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
	if ds.dir != nil {
		ds.dir.Close()
		ds.dir = nil
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
	for ds.todoIndex < len(ds.todo) {
		next, errno := ds.next()
		if errno != 0 || next.Name == "" {
			return false
		}

		if next.Name == "." || next.Name == ".." {
			ds.nextDirEntry = &next
			return true
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
		if ds.todoIndex >= len(ds.todo) {
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

		if next.Name == "." || next.Name == ".." {
			ds.nextDirEntry = &next
			return true
		}

		// Check if this source entry is marked as deleted in cache
		// Cache priority: if deleted in cache, don't show from source
		if ds.isDeleted(next.Name) {
			// Skip deleted entries from source
			continue
		}

		if _, seen := ds.seen[next.Name]; !seen {
			ds.nextDirEntry = &next
			return true
		}
	}
}

// isDeleted checks if a file/directory is marked as deleted in cache
func (ds *ShadowDirStream) isDeleted(name string) bool {
	fullPath := filepath.Join(ds.cachePath, name)
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

func (ds *ShadowDirStream) next() (fuse.DirEntry, syscall.Errno) {
	if ds.todoErrno != 0 {
		return fuse.DirEntry{}, ds.todoErrno
	}

	if ds.nextDirEntry != nil {
		e := *ds.nextDirEntry
		ds.nextDirEntry = nil
		return e, fs.OK
	}

	if ds.todoIndex >= len(ds.todo) {
		return fuse.DirEntry{}, fs.OK
	}

	entry := ds.todo[ds.todoIndex]
	ds.todoIndex++

	nameStr := entry.Name()
	if nameStr == "" {
		return ds.next()
	}

	// Get mode from filesystem for accuracy
	var mode uint32
	fullPath := filepath.Join(ds.getCurrentPath(), nameStr)
	info, err := entry.Info()
	if err == nil {
		mode = uint32(info.Mode())
	} else {
		// Fallback: stat the file
		var st syscall.Stat_t
		if err := syscall.Lstat(fullPath, &st); err == nil {
			mode = uint32(st.Mode)
		} else {
			// Default mode based on type
			if entry.IsDir() {
				mode = 0040755
			} else {
				mode = 0100644
			}
		}
	}

	// Get inode number
	var ino uint64
	if info, err := entry.Info(); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			ino = stat.Ino
		}
	}

	return fuse.DirEntry{
		Ino:  ino,
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

	entry, errno := ds.next()

	return entry, errno
}

func (ds *ShadowDirStream) load() syscall.Errno {
	if ds.dir == nil {
		return syscall.EBADF
	}

	// Read directory entries using os.ReadDir (macOS compatible)
	entries, err := ds.dir.ReadDir(-1) // -1 means read all entries
	if err != nil {
		ds.todoErrno = fs.ToErrno(err)
		return ds.todoErrno
	}

	ds.todo = entries
	ds.todoIndex = 0
	ds.todoErrno = 0

	return fs.OK
}
