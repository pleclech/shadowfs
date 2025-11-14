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
	// SetDeleted already sets CacheIndependent = true
	return d.xattrMgr.SetDeleted(path, originalType)
}

