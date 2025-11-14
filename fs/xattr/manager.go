package xattr

import "syscall"

// Manager provides a clean interface for xattr operations
type Manager struct {
	// No state needed - all operations are stateless
}

// NewManager creates a new XAttr manager
func NewManager() *Manager {
	return &Manager{}
}

// GetStatus retrieves the xattr status for a path
func (m *Manager) GetStatus(path string) (*XAttr, bool, syscall.Errno) {
	attr := XAttr{}
	exists, errno := Get(path, &attr)
	return &attr, exists, errno
}

// SetDeleted marks a path as deleted in cache and permanently independent
// All deleted files/directories are permanently detached from source
func (m *Manager) SetDeleted(path string, originalType uint32) syscall.Errno {
	attr := XAttr{
		PathStatus:      PathStatusDeleted,
		OriginalType:    originalType,
		CacheIndependent: true, // All deleted items are permanently independent
	}
	return Set(path, &attr)
}

// SetTypeReplaced marks a path as type-replaced with permanent independence
func (m *Manager) SetTypeReplaced(path string, originalType, currentType uint32) syscall.Errno {
	attr := XAttr{
		PathStatus:        PathStatusNone, // Active, not deleted
		OriginalType:      originalType,
		CurrentType:       currentType,
		DeletedFromSource: true,
		CacheIndependent:  true,
		TypeReplaced:      true,
	}
	return Set(path, &attr)
}

// SetCacheIndependent marks a path as permanently independent
func (m *Manager) SetCacheIndependent(path string) syscall.Errno {
	attr := XAttr{}
	exists, errno := Get(path, &attr)
	if errno != 0 && errno != syscall.ENODATA {
		return errno
	}
	if !exists {
		// Create new xattr if it doesn't exist
		attr = XAttr{}
	}
	attr.CacheIndependent = true
	return Set(path, &attr)
}

// Clear removes xattr from a path
func (m *Manager) Clear(path string) syscall.Errno {
	return Remove(path)
}

// IsDeleted checks if a path is marked as deleted
func (m *Manager) IsDeleted(path string) (bool, syscall.Errno) {
	attr := XAttr{}
	exists, errno := Get(path, &attr)
	if errno != 0 {
		return false, errno
	}
	return exists && IsPathDeleted(attr), 0
}

// SetRenameMapping sets rename tracking information
func (m *Manager) SetRenameMapping(path string, originalSourcePath, renamedFromPath string, depth int) syscall.Errno {
	attr := XAttr{}
	exists, errno := Get(path, &attr)
	if errno != 0 && errno != syscall.ENODATA {
		return errno
	}
	if !exists {
		attr = XAttr{}
	}
	attr.SetOriginalSourcePath(originalSourcePath)
	attr.SetRenamedFromPath(renamedFromPath)
	attr.RenameDepth = depth
	attr.DeletedFromSource = true // Renamed items are from source
	return Set(path, &attr)
}

