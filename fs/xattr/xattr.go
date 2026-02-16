package xattr

import (
	"strings"
	"time"
	"unsafe"
)

const Name = "user.shadow-fs"

// MaxPathLength is the maximum length for OriginalSourcePath
const MaxPathLength = 512

type PathStatus int

const (
	PathStatusNone    PathStatus = iota
	PathStatusDeleted            // file is deleted in cache
)

// RenameMapping tracks hierarchical rename operations
type RenameMapping struct {
	OriginalPath string // Path before rename
	CurrentPath  string // Path after rename
	Depth        int    // Depth of rename operation
	Timestamp    int64  // When the rename occurred
}

type XAttr struct {
	PathStatus         PathStatus          // Current path status
	OriginalType       uint32              // Original type from source
	CurrentType        uint32              // Current type in cache
	OriginalSourcePath [MaxPathLength]byte // Original source path for renamed directories
	RenamedFromPath    [MaxPathLength]byte // Track directory renames (path it was renamed from)
	RenameDepth        int                 // Depth of rename operation
	DeletedFromSource  bool                // Was this originally from source?
	CacheIndependent   bool                // Is this permanently independent?
	TypeReplaced       bool                // Has type been replaced? (same as IsReplaced, kept for clarity)
	// Note: IsReplaced field removed, use TypeReplaced instead
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

// List lists all extended attribute names for a given path
// This is implemented in platform-specific files:
// - xattr_linux.go: Linux implementation using syscall.Listxattr
// - xattr_other.go: Non-Linux stub implementation

func IsPathDeleted(attr XAttr) bool {
	return attr.PathStatus == PathStatusDeleted
}

// SetOriginalSourcePath sets the original source path for renamed directories
func (attr *XAttr) SetOriginalSourcePath(path string) {
	// Clear the array first
	for i := range attr.OriginalSourcePath {
		attr.OriginalSourcePath[i] = 0
	}
	// Copy path bytes, truncating if too long
	pathBytes := []byte(path)
	copyLen := len(pathBytes)
	if copyLen > MaxPathLength {
		copyLen = MaxPathLength
	}
	copy(attr.OriginalSourcePath[:], pathBytes[:copyLen])
}

// GetOriginalSourcePath returns the original source path as a string
func (attr *XAttr) GetOriginalSourcePath() string {
	// Find null terminator or end of array
	for i := 0; i < MaxPathLength; i++ {
		if attr.OriginalSourcePath[i] == 0 {
			return string(attr.OriginalSourcePath[:i])
		}
	}
	return string(attr.OriginalSourcePath[:])
}

// SetRenamedFromPath sets the path this entry was renamed from
func (attr *XAttr) SetRenamedFromPath(path string) {
	// Clear the array first
	for i := range attr.RenamedFromPath {
		attr.RenamedFromPath[i] = 0
	}
	// Copy path bytes, truncating if too long
	pathBytes := []byte(path)
	copyLen := len(pathBytes)
	if copyLen > MaxPathLength {
		copyLen = MaxPathLength
	}
	copy(attr.RenamedFromPath[:], pathBytes[:copyLen])
}

// GetRenamedFromPath returns the path this entry was renamed from
func (attr *XAttr) GetRenamedFromPath() string {
	// Find null terminator or end of array
	for i := 0; i < MaxPathLength; i++ {
		if attr.RenamedFromPath[i] == 0 {
			return string(attr.RenamedFromPath[:i])
		}
	}
	return string(attr.RenamedFromPath[:])
}

// StoreRenameMapping stores a rename mapping for hierarchical tracking
func StoreRenameMapping(mappings []RenameMapping, originalPath, currentPath string, depth int) []RenameMapping {
	mapping := RenameMapping{
		OriginalPath: originalPath,
		CurrentPath:  currentPath,
		Depth:        depth,
		Timestamp:    time.Now().Unix(),
	}
	return append(mappings, mapping)
}

// ResolveRenamedPath resolves the current path of a renamed entry considering hierarchical renames
func ResolveRenamedPath(mappings []RenameMapping, originalPath string) string {
	currentPath := originalPath

	// Apply rename mappings in order
	for _, mapping := range mappings {
		if strings.HasPrefix(currentPath, mapping.OriginalPath) {
			// Replace the prefix with the new path
			relativePath := strings.TrimPrefix(currentPath, mapping.OriginalPath)
			if !strings.HasPrefix(relativePath, "/") {
				relativePath = "/" + relativePath
			}
			currentPath = mapping.CurrentPath + relativePath
		}
	}

	return currentPath
}

// FindRenameMapping finds the most recent rename mapping for a given path
func FindRenameMapping(mappings []RenameMapping, path string) *RenameMapping {
	var latest *RenameMapping
	for i := range mappings {
		if strings.HasPrefix(path, mappings[i].OriginalPath) {
			if latest == nil || mappings[i].Timestamp > latest.Timestamp {
				latest = &mappings[i]
			}
		}
	}
	return latest
}
