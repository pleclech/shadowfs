package xattr

import (
	"unsafe"
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

// Get and Set are implemented in platform-specific files:
// - xattr_linux.go: Linux implementation using syscall.Getxattr/Setxattr
// - xattr_other.go: Non-Linux stub implementation

// Remove removes an extended attribute from the given path
// This is implemented in platform-specific files:
// - xattr_linux.go: Linux implementation using syscall.Removexattr
// - xattr_other.go: Non-Linux stub implementation

func IsPathDeleted(attr XAttr) bool {
	return attr.PathStatus&PathStatusDeleted != 0
}

