//go:build linux
// +build linux

package fs

// Copyright 2019 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

import (
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
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

	// Track consecutive empty loads to detect end-of-directory
	consecutiveEmptyLoads int
}

// NewShadowDirStream open a directory for reading as a DirStream
func NewShadowDirStream(root *ShadowNode, originPath string) (fs.DirStream, syscall.Errno) {
	if originPath == "" {
		originPath = root.FullPath(false)
	}

	cachePath := root.RebasePathUsingCache(originPath)

	// check if the directory is not deleted in cache
	xattr := ShadowXAttr{}
	GetShadowXAttr(cachePath, &xattr)
	if IsPathDeleted(xattr) {
		// directory is deleted
		return nil, syscall.ENOENT
	}

	ds := &ShadowDirStream{
		buf:        make([]byte, 4096),
		fd:         -1,
		root:       root,
		originPath: originPath,
		cachePath:  cachePath,
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
	ds.consecutiveEmptyLoads = 0

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

	var name string
	var xattr ShadowXAttr

	if ds.step == stepCache {
		if ds.todoErrno != 0 {
			ds.originStep()
		} else if ds.nextDirEntry != nil {
			return true
		}

		// Process cache entries
		for len(ds.todo) > 0 {
			next, todoErrno := ds.next()

			if todoErrno != 0 {
				ds.originStep()
				break
			}

			// Check if we got an empty entry (end of directory)
			if next.Name == "" {
				ds.originStep()
				break
			}

			if next.Name == "." || next.Name == ".." {
				continue
			}

			ds.seen[next.Name] = struct{}{}

			name = filepath.Join(ds.cachePath, next.Name)

			// check if file or directory is not deleted in cache
			xattr = ShadowXAttr{}
			GetShadowXAttr(name, &xattr)
			if !IsPathDeleted(xattr) {
				// Found a valid entry in cache
				ds.nextDirEntry = &next
				return true
			}
			// Entry is deleted, continue processing
		}

		// If we get here, either cache was empty or all entries were deleted
		// Switch to origin step to see entries from source directory
		ds.originStep()
	}

	if ds.todoErrno != 0 {
		return true
	}

	// Origin step: process entries from source directory
	maxIterations := 1000 // Maximum iterations to prevent infinite loops
	iterations := 0
	seenCount := 0 // Track how many entries we've skipped because already seen

	for {
		iterations++
		if iterations > maxIterations {
			// Too many iterations, likely infinite loop - break out
			break
		}

		// Check if we have a pending entry before calling next()
		if ds.nextDirEntry != nil {
			return true
		}

		// Check if we're out of data and can't load more
		if len(ds.todo) == 0 {
			if ds.todoErrno != 0 {
				// Error occurred, no more entries
				return false
			}
			// Try to load more data
			if errno := ds.load(); errno != 0 {
				return false
			}
			// If we got 0 bytes, we've reached end-of-directory
			if len(ds.todo) == 0 {
				// End of directory reached
				return false
			}
		}

		next, todoErrno := ds.next()

		if todoErrno != 0 {
			// Error occurred, check if we have a pending entry
			if ds.nextDirEntry != nil {
				return true
			}
			// No more entries available
			return false
		}

		// Check if we got an empty entry (end of directory)
		if next.Name == "" {
			// No more entries available
			return false
		}

		name := next.Name

		if name == "." || name == ".." {
			ds.nextDirEntry = &next
			return true
		}

		if _, ok := ds.seen[name]; ok {
			// Already seen this entry, skip it
			seenCount++
			// If we've skipped too many entries in a row, likely all remaining are duplicates
			if seenCount > 100 {
				// All remaining entries are duplicates, no more new entries
				return false
			}
			continue
		}

		// New entry found - reset seen count
		seenCount = 0
		ds.nextDirEntry = &next
		return true
	}

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

	// Minimum size for a dirent structure (header before name field)
	minDirentSize := int(unsafe.Offsetof(dirent{}.Name))
	maxSkipAttempts := 100 // Maximum attempts to skip invalid entries
	skipAttempts := 0
	var de *dirent

	for {
		// Check if we have enough data for a dirent structure
		if len(ds.todo) < minDirentSize+4 {
			// Not enough data, try to load more
			if errno := ds.load(); errno != 0 {
				return fuse.DirEntry{}, errno
			}
			if len(ds.todo) == 0 {
				// No more data available
				return fuse.DirEntry{}, fs.OK
			}
			continue
		}

		de = (*dirent)(unsafe.Pointer(&ds.todo[0]))

		// Validate Reclen to prevent out-of-bounds access
		reclenOffset := uint16(minDirentSize)
		if de.Reclen < reclenOffset || int(de.Reclen) > len(ds.todo) {
			// Invalid reclen, skip this entry
			// Advance by at least the minimum dirent size to ensure progress
			advance := minDirentSize
			if advance > len(ds.todo) {
				advance = len(ds.todo)
			}
			ds.todo = ds.todo[advance:]
			skipAttempts++
			if skipAttempts >= maxSkipAttempts {
				// Too many invalid entries, likely corrupted buffer
				return fuse.DirEntry{}, syscall.EIO
			}
			continue
		}

		// Extract name to check if it's empty
		nameBytes := ds.todo[unsafe.Offsetof(dirent{}.Name):de.Reclen]
		// Find the first null byte
		l := 0
		for l = range nameBytes {
			if nameBytes[l] == 0 {
				break
			}
		}
		nameStr := string(nameBytes[:l])

		// Validate name is not empty - if empty, skip and try next entry
		if nameStr == "" {
			// Empty name, skip this entry
			ds.todo = ds.todo[de.Reclen:]
			skipAttempts++
			if skipAttempts >= maxSkipAttempts {
				return fuse.DirEntry{}, syscall.EIO
			}
			// Continue the loop to get next entry
			if len(ds.todo) == 0 {
				if errno := ds.load(); errno != 0 {
					return fuse.DirEntry{}, errno
				}
				if len(ds.todo) == 0 {
					return fuse.DirEntry{}, fs.OK
				}
			}
			continue
		}

		// Valid entry with non-empty name found, process it
		ds.todo = ds.todo[de.Reclen:]

		var mode uint32

		// Always check the actual filesystem for reliable mode information
		// This prevents issues with incorrect de.Type values from kernel
		fullPath := filepath.Join(ds.cachePath, nameStr)
		var st syscall.Stat_t
		err := syscall.Lstat(fullPath, &st)
		if err == nil {
			// Use the actual mode from the filesystem
			mode = st.Mode
		} else {
			// Fallback to de.Type calculation
			if de.Type == 4 { // DT_DIR
				mode = 0040755 // Standard directory permissions: drwxr-xr-x (S_IFDIR | 0755)
			} else {
				mode = (uint32(de.Type) << 12)
			}
		}

		result := fuse.DirEntry{
			Ino:  de.Ino,
			Mode: mode,
			Name: nameStr,
		}
		return result, fs.OK
	}

	// Should never reach here, but return error if we do
	return fuse.DirEntry{}, syscall.EIO
}

func (ds *ShadowDirStream) Next() (fuse.DirEntry, syscall.Errno) {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	entry, errno := ds.next()

	return entry, errno
}

func (ds *ShadowDirStream) load() syscall.Errno {
	if len(ds.todo) > 0 {
		// Don't reset the counter here - let it be managed by HasNext()
		return fs.OK
	}

	n, err := syscall.Getdents(ds.fd, ds.buf)
	if n < 0 {
		n = 0
	}

	ds.todo = ds.buf[:n]
	ds.todoErrno = fs.ToErrno(err)

	// If we got 0 bytes and no error, we've reached end-of-directory
	if n == 0 && err == nil {
		ds.consecutiveEmptyLoads++
	} else if n > 0 {
		// Only reset when we actually get data
		ds.consecutiveEmptyLoads = 0
	}

	return fs.OK
}
