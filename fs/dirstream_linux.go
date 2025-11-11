//go:build linux
// +build linux

package fs

// Copyright 2019 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

import (
	"fmt"
	"path/filepath"
	"strings"
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

		for len(ds.todo) > 0 {
			next, todoErrno := ds.next()

			if todoErrno != 0 {
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
				// Debug for root directory
				if strings.Contains(ds.cachePath, ".root") {
					fmt.Printf("Processing directory entry: %s\n", next.Name)
					fmt.Printf("Including entry: %s\n", next.Name)
				}
				ds.nextDirEntry = &next
				return true
			} else {
				// Debug for root directory
				if strings.Contains(ds.cachePath, ".root") {
					fmt.Printf("Skipping deleted entry: %s\n", next.Name)
				}
			}
		}

		ds.originStep()
	}

	if ds.todoErrno != 0 || ds.nextDirEntry != nil {
		return true
	}

	for len(ds.todo) > 0 {
		next, todoErrno := ds.next()

		if todoErrno != 0 || ds.nextDirEntry != nil {
			return true
		}

		name := next.Name

		if name == "." || name == ".." {
			ds.nextDirEntry = &next
			return true
		}

		if _, ok := ds.seen[name]; ok {
			continue
		}

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

	// We can't use syscall.Dirent here, because it declares a
	// [256]byte name, which may run beyond the end of ds.todo.
	// when that happens in the race detector, it causes a panic
	// "converted pointer straddles multiple allocations"
	if len(ds.todo) < int(unsafe.Offsetof(dirent{}.Reclen))+4 {
		// Not enough data for a dirent structure
		return fuse.DirEntry{}, ds.load()
	}
	de := (*dirent)(unsafe.Pointer(&ds.todo[0]))

	// Validate Reclen to prevent out-of-bounds access
	reclenOffset := uint16(unsafe.Offsetof(dirent{}.Name))
	if de.Reclen < reclenOffset || int(de.Reclen) > len(ds.todo) {
		// Invalid reclen, skip this entry
		ds.todo = ds.todo[1:]
		return ds.next()
	}

	nameBytes := ds.todo[unsafe.Offsetof(dirent{}.Name):de.Reclen]
	ds.todo = ds.todo[de.Reclen:]

	// After the loop, l contains the index of the first '\0'.
	l := 0
	for l = range nameBytes {
		if nameBytes[l] == 0 {
			break
		}
	}
	nameBytes = nameBytes[:l]
	nameStr := string(nameBytes)
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
		Name: string(nameBytes),
	}
	return result, ds.load()
}

func (ds *ShadowDirStream) Next() (fuse.DirEntry, syscall.Errno) {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	entry, errno := ds.next()

	return entry, errno
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
