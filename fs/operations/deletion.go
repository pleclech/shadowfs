package operations

import (
	"syscall"

	"github.com/pleclech/shadowfs/fs/xattr"
)

// Deletion handles permanent independence after deletion per Phase 3.3 constraints
type Deletion struct {
	xattrMgr *xattr.Manager
}

// NewDeletion creates a new deletion handler
func NewDeletion() *Deletion {
	return &Deletion{
		xattrMgr: xattr.NewManager(),
	}
}

// HandlePermanentIndependence marks a path as permanently independent after deletion
// MUST:
// - Set CacheIndependent = true (all deleted items are permanently independent)
// - Set PathStatus = PathStatusDeleted
// - Prevent source reconnection
// - Clear rename mapping since path is now independent
func (d *Deletion) HandlePermanentIndependence(path string) syscall.Errno {
	attr, exists, errno := d.xattrMgr.GetStatus(path)
	if errno != 0 && errno != syscall.ENODATA {
		return errno
	}
	
	var originalType uint32
	if exists && attr != nil {
		originalType = attr.OriginalType
	} else {
		// Default to regular file if no xattr exists
		originalType = 0 // Will be set by caller
	}
	
	// All deleted items are permanently independent (detached from source)
	// SetDeleted already sets CacheIndependent = true and clears rename fields
	if errno := d.xattrMgr.SetDeleted(path, originalType); errno != 0 {
		return errno
	}
	
	// Explicitly clear rename mapping to ensure it's removed
	// (SetCacheIndependent in SetDeleted should have done this, but be explicit)
	return d.xattrMgr.ClearRenameMapping(path)
}

