package operations

import (
	"strings"
	"syscall"

	"github.com/pleclech/shadowfs/fs/xattr"
)

// RenameMapping tracks hierarchical rename operations per Phase 3.1
type RenameMapping struct {
	SourcePath string // Original source path before rename
	DestPath   string // Destination path after rename
	Depth      int    // Depth of rename operation
}

// RenameTracker manages hierarchical rename tracking
type RenameTracker struct {
	mappings map[string]*RenameMapping // key: destPath, value: mapping
}

// NewRenameTracker creates a new rename tracker
func NewRenameTracker() *RenameTracker {
	return &RenameTracker{
		mappings: make(map[string]*RenameMapping),
	}
}

// StoreRenameMapping stores a rename mapping with depth calculation
func (rt *RenameTracker) StoreRenameMapping(sourcePath, destPath string) {
	depth := rt.calculateDepth(sourcePath)
	mapping := &RenameMapping{
		SourcePath: sourcePath,
		DestPath:   destPath,
		Depth:      depth,
	}
	rt.mappings[destPath] = mapping
}

// ResolveRenamedPath resolves the original source path for a renamed directory
// Converts: /foo/baz/hello/world -> /foo/bar/hello/world
func (rt *RenameTracker) ResolveRenamedPath(requestedPath string) (string, bool) {
	// Normalize path to ensure consistent comparison
	requestedPath = strings.TrimRight(requestedPath, "/")
	if requestedPath == "" {
		requestedPath = "/"
	}
	
	// Check if requested path is under a renamed directory
	// Find the longest matching prefix (most specific rename)
	longestMatch := ""
	var bestMapping *RenameMapping
	
	for destPrefix, mapping := range rt.mappings {
		normalizedPrefix := strings.TrimRight(destPrefix, "/")
		if normalizedPrefix == "" {
			normalizedPrefix = "/"
		}
		
		// Check if requested path starts with this prefix
		if requestedPath == normalizedPrefix || strings.HasPrefix(requestedPath, normalizedPrefix+"/") {
			// Use the longest matching prefix (most specific)
			if len(normalizedPrefix) > len(longestMatch) {
				longestMatch = normalizedPrefix
				bestMapping = mapping
			}
		}
	}
	
	if bestMapping != nil {
		// Convert: /foo/baz/hello/world -> /foo/bar/hello/world
		relativePath := strings.TrimPrefix(requestedPath, longestMatch)
		if relativePath == "" {
			// Exact match - return source path
			return bestMapping.SourcePath, true
		}
		// Ensure relative path starts with /
		if !strings.HasPrefix(relativePath, "/") {
			relativePath = "/" + relativePath
		}
		originalPath := bestMapping.SourcePath + relativePath
		return originalPath, true
	}
	
	return requestedPath, false
}

// calculateDepth calculates the depth of a path
func (rt *RenameTracker) calculateDepth(path string) int {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	depth := 0
	for _, part := range parts {
		if part != "" && part != "." && part != ".." {
			depth++
		}
	}
	return depth
}

// StoreRenameInXAttr stores rename information in xattr for persistence
func StoreRenameInXAttr(path string, originalSourcePath, renamedFromPath string, depth int) syscall.Errno {
	xattrMgr := xattr.NewManager()
	return xattrMgr.SetRenameMapping(path, originalSourcePath, renamedFromPath, depth)
}

