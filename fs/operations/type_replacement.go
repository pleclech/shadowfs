package operations

import (
	"syscall"

	"github.com/pleclech/shadowfs/fs/xattr"
)

// TypeReplacement handles type replacement per Phase 3.2 constraints
type TypeReplacement struct {
	xattrMgr *xattr.Manager
}

// NewTypeReplacement creates a new type replacement handler
func NewTypeReplacement() *TypeReplacement {
	return &TypeReplacement{
		xattrMgr: xattr.NewManager(),
	}
}

// HandleTypeReplacement sets all required xattr fields for type replacement
// MUST set:
// - PathStatus = PathStatusActive (not deleted)
// - OriginalType to original type from source
// - CurrentType to new type
// - DeletedFromSource = true
// - CacheIndependent = true (permanent independence)
// - TypeReplaced = true
func (tr *TypeReplacement) HandleTypeReplacement(path string, originalType, newType uint32) syscall.Errno {
	return tr.xattrMgr.SetTypeReplaced(path, originalType, newType)
}

// IsTypeReplaced checks if a path has been type-replaced
func (tr *TypeReplacement) IsTypeReplaced(path string) (bool, syscall.Errno) {
	attr, exists, errno := tr.xattrMgr.GetStatus(path)
	if errno != 0 {
		return false, errno
	}
	return exists && attr.TypeReplaced, 0
}

