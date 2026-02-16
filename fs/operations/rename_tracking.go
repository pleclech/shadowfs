package operations

import (
	"fmt"
	"path/filepath"
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

// RenameTrie is a path-component trie for efficient prefix matching of renamed paths
type RenameTrie struct {
	// If this node represents a renamed path:
	renamedFrom      string // Original source path (relative to srcDir)
	cacheIndependent bool   // Whether this path is independent

	// Children map: component name -> child trie node
	children map[string]*RenameTrie
}

// NewRenameTrie creates a new empty trie
func NewRenameTrie() *RenameTrie {
	return &RenameTrie{
		children: make(map[string]*RenameTrie),
	}
}

// Insert adds a rename mapping to the trie
// destPath: current mount point path (e.g., "foo/baz")
// renamedFrom: original source path (e.g., "foo/bar")
// independent: whether this path is cache independent
func (rt *RenameTrie) Insert(destPath, renamedFrom string, independent bool) {
	// Normalize paths
	destPath = strings.Trim(destPath, "/")
	renamedFrom = strings.Trim(renamedFrom, "/")

	if destPath == "" {
		// Root path - store directly in this node
		rt.renamedFrom = renamedFrom
		rt.cacheIndependent = independent
		return
	}

	// Split into components
	parts := strings.Split(destPath, "/")
	current := rt

	// Navigate/create path in trie
	for i, part := range parts {
		if part == "" {
			continue
		}

		// Get or create child node
		if current.children == nil {
			current.children = make(map[string]*RenameTrie)
		}
		child, exists := current.children[part]
		if !exists {
			child = NewRenameTrie()
			current.children[part] = child
		}

		// If this is the last component, store rename info
		if i == len(parts)-1 {
			child.renamedFrom = renamedFrom
			child.cacheIndependent = independent
		}

		current = child
	}
}

// Resolve finds the longest matching prefix and resolves the path
// mountPointPath: path to resolve (e.g., "foo/baz/hello")
// Returns: (resolvedPath, isIndependent, found)
func (rt *RenameTrie) Resolve(mountPointPath string) (resolvedPath string, isIndependent bool, found bool) {
	// Normalize path
	mountPointPath = strings.Trim(mountPointPath, "/")
	if mountPointPath == "" {
		// Root path - check if root is renamed
		if rt.renamedFrom != "" {
			return rt.renamedFrom, rt.cacheIndependent, true
		}
		return "", false, false
	}

	// Split into components
	parts := strings.Split(mountPointPath, "/")
	current := rt
	matchedPath := ""
	bestMatch := ""
	bestRenamedFrom := ""
	bestIndependent := false

	// Walk down the trie, tracking the best match
	for i, part := range parts {
		if part == "" {
			continue
		}

		// Check if current node has a rename (potential match)
		if current.renamedFrom != "" {
			bestMatch = matchedPath
			bestRenamedFrom = current.renamedFrom
			bestIndependent = current.cacheIndependent
		}

		// Check if this path is independent - stop resolution
		if current.cacheIndependent && current.renamedFrom != "" {
			// Found independent path - return immediately
			return "", true, true
		}

		// Try to continue down the trie
		if current.children == nil {
			break
		}
		child, exists := current.children[part]
		if !exists {
			break
		}

		// Build matched path
		if matchedPath == "" {
			matchedPath = part
		} else {
			matchedPath = filepath.Join(matchedPath, part)
		}

		current = child

		// Check if we've reached the end of the path
		if i == len(parts)-1 {
			// Final component - check if it has rename info or is independent
			if current.renamedFrom != "" {
				bestMatch = matchedPath
				bestRenamedFrom = current.renamedFrom
				bestIndependent = current.cacheIndependent
			} else if current.cacheIndependent {
				// Path is marked as independent (e.g., deleted) but not renamed
				// Return it as found with independent=true
				bestMatch = matchedPath
				bestRenamedFrom = matchedPath // Use same path for independent paths
				bestIndependent = true
			}
		}
	}

	// If we found a match (either renamed or independent), resolve the path
	if bestMatch != "" {
		if bestRenamedFrom != "" {
			// Calculate suffix (remaining path after matched prefix)
			suffix := strings.TrimPrefix(mountPointPath, bestMatch)
			suffix = strings.TrimPrefix(suffix, "/")

			if suffix == "" {
				// Exact match
				resolvedPath = bestRenamedFrom
			} else {
				// Partial match - append suffix
				resolvedPath = filepath.Join(bestRenamedFrom, suffix)
			}

			return resolvedPath, bestIndependent, true
		} else if bestIndependent {
			// Independent path without rename - return as-is
			return mountPointPath, true, true
		}
	}

	return "", false, false
}

// Helper function to get map keys for debugging
func getMapKeys(m map[string]*RenameTrie) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// Remove removes a rename mapping from the trie
func (rt *RenameTrie) Remove(destPath string) {
	destPath = strings.Trim(destPath, "/")
	if destPath == "" {
		// Remove root rename
		rt.renamedFrom = ""
		rt.cacheIndependent = false
		return
	}

	parts := strings.Split(destPath, "/")
	current := rt

	// Navigate to the node
	for i, part := range parts {
		if part == "" {
			continue
		}

		if current.children == nil {
			return // Path doesn't exist
		}

		child, exists := current.children[part]
		if !exists {
			return // Path doesn't exist
		}

		// If this is the last component, clear rename info
		if i == len(parts)-1 {
			child.renamedFrom = ""
			child.cacheIndependent = false

			// If node has no children and no rename info, remove it
			if len(child.children) == 0 {
				delete(current.children, part)
			}
			return
		}

		current = child
	}
}

// SetIndependent marks a path as independent in the trie
func (rt *RenameTrie) SetIndependent(destPath string) {
	destPath = strings.Trim(destPath, "/")
	if destPath == "" {
		// Root path
		rt.cacheIndependent = true
		return
	}

	parts := strings.Split(destPath, "/")
	current := rt

	// Navigate to the node
	for i, part := range parts {
		if part == "" {
			continue
		}

		if current.children == nil {
			// Path doesn't exist in trie - create it
			if current.children == nil {
				current.children = make(map[string]*RenameTrie)
			}
			current.children[part] = NewRenameTrie()
		}

		child, exists := current.children[part]
		if !exists {
			child = NewRenameTrie()
			current.children[part] = child
		}

		// If this is the last component, mark as independent
		if i == len(parts)-1 {
			child.cacheIndependent = true
			return
		}

		current = child
	}
}

// RenameTracker manages hierarchical rename tracking
type RenameTracker struct {
	mappings map[string]*RenameMapping // key: destPath, value: mapping (kept for backward compatibility)
	trie     *RenameTrie               // Trie for efficient prefix matching
}

// NewRenameTracker creates a new rename tracker
func NewRenameTracker() *RenameTracker {
	return &RenameTracker{
		mappings: make(map[string]*RenameMapping),
		trie:     NewRenameTrie(),
	}
}

// StoreRenameMapping stores a rename mapping with depth calculation
// sourcePath: original source path (relative to srcDir, e.g., "foo/bar")
// destPath: destination path (relative to mount point, e.g., "foo/baz")
// independent: whether this path is cache independent
func (rt *RenameTracker) StoreRenameMapping(sourcePath, destPath string, independent ...bool) {
	depth := rt.calculateDepth(sourcePath)
	mapping := &RenameMapping{
		SourcePath: sourcePath,
		DestPath:   destPath,
		Depth:      depth,
	}
	rt.mappings[destPath] = mapping

	// Also store in trie for efficient lookup
	isIndependent := false
	if len(independent) > 0 {
		isIndependent = independent[0]
	}
	rt.trie.Insert(destPath, sourcePath, isIndependent)
}

// ResolveRenamedPath resolves the original source path for a renamed directory
// Converts: foo/baz/hello/world -> foo/bar/hello/world
// Returns: (resolvedPath, isIndependent, found)
func (rt *RenameTracker) ResolveRenamedPath(requestedPath string) (resolvedPath string, isIndependent bool, found bool) {
	// Normalize path
	requestedPath = strings.Trim(requestedPath, "/")

	// Use trie for efficient O(m) resolution
	resolved, independent, found := rt.trie.Resolve(requestedPath)

	return resolved, independent, found
}

// RemoveRenameMapping removes a rename mapping from both map and trie
func (rt *RenameTracker) RemoveRenameMapping(destPath string) {
	delete(rt.mappings, destPath)
	rt.trie.Remove(destPath)
}

// FindRenamedToPath finds the current destination path for a given source path
// Given a sourcePath (e.g., "file1.txt"), returns the destPath it was renamed TO (e.g., "renamed.txt")
// Returns: (destPath, found)
func (rt *RenameTracker) FindRenamedToPath(sourcePath string) (destPath string, found bool) {
	// Search through all mappings to find one where SourcePath matches
	for dest, mapping := range rt.mappings {
		if mapping.SourcePath == sourcePath {
			return dest, true
		}
	}
	return "", false
}

// GetDestinationPaths returns a list of all destination paths currently tracked
// Used for targeted conflict detection to avoid full directory walks
func (rt *RenameTracker) GetDestinationPaths() []string {
	var paths []string
	for destPath := range rt.mappings {
		paths = append(paths, destPath)
	}
	return paths
}

// SetIndependent marks a path as independent in the trie
// This is used when a path becomes independent (e.g., after deletion)
func (rt *RenameTracker) SetIndependent(destPath string) {
	destPath = strings.Trim(destPath, "/")
	if rt.trie != nil {
		rt.trie.SetIndependent(destPath)
	}
}

// DumpTrie returns a string representation of all mappings in the trie
// Useful for debugging
func (rt *RenameTracker) DumpTrie() string {
	if rt == nil {
		return "<nil RenameTracker>\n"
	}
	if rt.trie == nil {
		return "<nil trie>\n"
	}
	result := rt.trie.Dump("")
	if result == "" {
		return "<empty trie>\n"
	}
	return result
}

// Dump returns a string representation of the trie starting at the given prefix
func (rt *RenameTrie) Dump(prefix string) string {
	var result strings.Builder

	if rt.renamedFrom != "" {
		result.WriteString(fmt.Sprintf("%s -> %s (independent=%v)\n", prefix, rt.renamedFrom, rt.cacheIndependent))
	}

	if rt.children != nil {
		for name, child := range rt.children {
			childPrefix := name
			if prefix != "" {
				childPrefix = prefix + "/" + name
			}
			result.WriteString(child.Dump(childPrefix))
		}
	}

	return result.String()
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
