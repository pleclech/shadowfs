package fs

import (
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
)

// FileOperation defines the interface for file operations
type FileOperation interface {
	// Path operations
	GetPath() string
	GetSourcePath() string
	GetCachePath() string
	IsCached() bool

	// File operations
	Stat(followSymlinks bool) (*syscall.Stat_t, syscall.Errno)
	Open(flags int) (*os.File, syscall.Errno)
	Create(mode uint32) (*os.File, syscall.Errno)
	ReadAll() ([]byte, syscall.Errno)
	WriteAll(data []byte) syscall.Errno
	CopyTo(destPath string) syscall.Errno

	// Metadata operations
	GetXattr(name string, dest []byte) (int, syscall.Errno)
	SetXattr(name string, value []byte, flags int) syscall.Errno
	RemoveXattr(name string) syscall.Errno

	// Status operations
	IsDeleted() (bool, syscall.Errno)
	MarkDeleted() syscall.Errno
	MarkNormal() syscall.Errno
}

// FileOperationImpl implements the FileOperation interface
type FileOperationImpl struct {
	pathManager *PathManager
	path        string
}

// NewFileOperation creates a new FileOperation instance
func NewFileOperation(pathManager *PathManager, statCache interface{}, path string) FileOperation {
	return &FileOperationImpl{
		pathManager: pathManager,
		path:        path,
	}
}

// GetPath returns the relative path
func (fo *FileOperationImpl) GetPath() string {
	return fo.path
}

// GetSourcePath returns the full source path
func (fo *FileOperationImpl) GetSourcePath() string {
	return fo.pathManager.FullPath(fo.path, false)
}

// GetCachePath returns the full cache path
func (fo *FileOperationImpl) GetCachePath() string {
	return fo.pathManager.FullPath(fo.path, true)
}

// IsCached checks if the file exists in cache
func (fo *FileOperationImpl) IsCached() bool {
	cachePath := fo.GetCachePath()
	_, err := os.Stat(cachePath)
	return err == nil
}

// Stat performs a stat operation with caching
func (fo *FileOperationImpl) Stat(followSymlinks bool) (*syscall.Stat_t, syscall.Errno) {
	start := time.Now()
	defer func() {
		LogOperation("stat", fo.path, start, nil)
	}()

	// Perform actual stat
	var st syscall.Stat_t
	var err error
	path := fo.GetCachePath()
	if fo.pathManager.IsCachePath(path) {
		// Use cache path if it exists
		if _, statErr := os.Stat(path); statErr == nil {
			if followSymlinks {
				err = syscall.Stat(path, &st)
			} else {
				err = syscall.Lstat(path, &st)
			}
		} else {
			// Fall back to source path
			path = fo.GetSourcePath()
			if followSymlinks {
				err = syscall.Stat(path, &st)
			} else {
				err = syscall.Lstat(path, &st)
			}
		}
	} else {
		path = fo.GetSourcePath()
		if followSymlinks {
			err = syscall.Stat(path, &st)
		} else {
			err = syscall.Lstat(path, &st)
		}
	}

	var errno syscall.Errno
	if err != nil {
		errno = fs.ToErrno(err)
	}

	return &st, errno
}

// Open opens a file with the specified flags
func (fo *FileOperationImpl) Open(flags int) (*os.File, syscall.Errno) {
	start := time.Now()
	defer func() {
		LogOperation("open", fo.path, start, nil)
	}()

	path := fo.GetCachePath()
	if !fo.IsCached() {
		path = fo.GetSourcePath()
	}

	file, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return nil, fs.ToErrno(err)
	}

	return file, 0
}

// Create creates a new file
func (fo *FileOperationImpl) Create(mode uint32) (*os.File, syscall.Errno) {
	start := time.Now()
	defer func() {
		LogOperation("create", fo.path, start, nil)
	}()

	path := fo.GetCachePath()

	// Ensure parent directory exists
	lastSlash := strings.LastIndex(fo.path, "/")
	if lastSlash > 0 {
		parentDir := fo.pathManager.FullPath(fo.path[:lastSlash], true)
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			return nil, fs.ToErrno(err)
		}
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(mode))
	if err != nil {
		return nil, fs.ToErrno(err)
	}

	return file, 0
}

// ReadAll reads the entire file content
func (fo *FileOperationImpl) ReadAll() ([]byte, syscall.Errno) {
	start := time.Now()
	defer func() {
		LogOperation("readall", fo.path, start, nil)
	}()

	file, errno := fo.Open(os.O_RDONLY)
	if errno != 0 {
		return nil, errno
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fs.ToErrno(err)
	}

	return data, 0
}

// WriteAll writes data to the file
func (fo *FileOperationImpl) WriteAll(data []byte) syscall.Errno {
	start := time.Now()
	defer func() {
		LogOperation("writeall", fo.path, start, nil)
	}()

	file, errno := fo.Create(0644)
	if errno != 0 {
		return errno
	}
	defer file.Close()

	_, err := file.Write(data)
	if err != nil {
		return fs.ToErrno(err)
	}

	return 0
}

// CopyTo copies the file to a destination path
func (fo *FileOperationImpl) CopyTo(destPath string) syscall.Errno {
	start := time.Now()
	defer func() {
		LogOperation("copy", fo.path+" -> "+destPath, start, nil)
	}()

	srcFile, errno := fo.Open(os.O_RDONLY)
	if errno != 0 {
		return errno
	}
	defer srcFile.Close()

	destFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fs.ToErrno(err)
	}
	defer destFile.Close()

	// Use efficient copy with larger buffer
	_, err = io.CopyBuffer(destFile, srcFile, make([]byte, 64*1024))
	if err != nil {
		return fs.ToErrno(err)
	}

	return 0
}

// GetXattr gets an extended attribute
func (fo *FileOperationImpl) GetXattr(name string, dest []byte) (int, syscall.Errno) {
	path := fo.GetCachePath()
	if !fo.IsCached() {
		path = fo.GetSourcePath()
	}

	size, err := syscall.Getxattr(path, name, dest)
	if err != nil {
		return 0, fs.ToErrno(err)
	}

	return size, 0
}

// SetXattr sets an extended attribute
func (fo *FileOperationImpl) SetXattr(name string, value []byte, flags int) syscall.Errno {
	path := fo.GetCachePath()
	if !fo.IsCached() {
		path = fo.GetSourcePath()
	}

	err := syscall.Setxattr(path, name, value, flags)
	if err != nil {
		return fs.ToErrno(err)
	}

	return 0
}

// RemoveXattr removes an extended attribute
func (fo *FileOperationImpl) RemoveXattr(name string) syscall.Errno {
	path := fo.GetCachePath()
	if !fo.IsCached() {
		path = fo.GetSourcePath()
	}

	err := syscall.Removexattr(path, name)
	if err != nil {
		return fs.ToErrno(err)
	}

	return 0
}

// IsDeleted checks if the file is marked as deleted
func (fo *FileOperationImpl) IsDeleted() (bool, syscall.Errno) {
	xattr := ShadowXAttr{}
	exists, errno := GetShadowXAttr(fo.GetCachePath(), &xattr)
	if errno != 0 {
		return false, errno
	}
	return exists && IsPathDeleted(xattr), 0
}

// MarkDeleted marks the file as deleted
func (fo *FileOperationImpl) MarkDeleted() syscall.Errno {
	xattr := ShadowXAttr{ShadowPathStatus: ShadowPathStatusDeleted}
	return SetShadowXAttr(fo.GetCachePath(), &xattr)
}

// MarkNormal marks the file as normal (not deleted)
func (fo *FileOperationImpl) MarkNormal() syscall.Errno {
	xattr := ShadowXAttr{ShadowPathStatus: ShadowPathStatusNone}
	return SetShadowXAttr(fo.GetCachePath(), &xattr)
}
