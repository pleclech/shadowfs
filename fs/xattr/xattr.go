package xattr

import (
	"os"
	"syscall"
	"unsafe"

	"github.com/hanwen/go-fuse/v2/fs"
)

const Name = "user.shadow-fs"

type PathStatus int

const (
	PathStatusNone    PathStatus = iota
	PathStatusDeleted            // file is deleted in cache
)

type XAttr struct {
	PathStatus PathStatus
}

const Size = int(unsafe.Sizeof(XAttr{}))

func ToBytes(attr *XAttr) []byte {
	return (*(*[Size]byte)(unsafe.Pointer(attr)))[:]
}

func FromBytes(data []byte) *XAttr {
	if len(data) < Size {
		// Return zero-initialized struct if data is too short
		return &XAttr{}
	}
	return (*XAttr)(unsafe.Pointer(&data[0]))
}

func Get(path string, attr *XAttr) (exists bool, errno syscall.Errno) {
	_, err := syscall.Getxattr(path, Name, ToBytes(attr))

	// skip if xattr not found
	if os.IsNotExist(err) {
		return false, 0
	}

	if err != nil && err != syscall.ENODATA {
		return false, fs.ToErrno(err)
	}

	return true, 0
}

func Set(path string, attr *XAttr) syscall.Errno {
	return fs.ToErrno(syscall.Setxattr(path, Name, ToBytes(attr), 0))
}

func IsPathDeleted(attr XAttr) bool {
	return attr.PathStatus&PathStatusDeleted != 0
}

