package fs

import (
	"path/filepath"
	"strings"
	"syscall"

	"github.com/pleclech/shadowfs/fs/cache"
	"github.com/pleclech/shadowfs/fs/operations"
	"github.com/pleclech/shadowfs/fs/xattr"
)

// PathResolver centralizes path resolution logic for renamed paths
// It handles rename tracking, deletion markers, cache priority, and independent paths
type PathResolver struct {
	srcDir        string
	cachePath     string
	mountPoint    string
	renameTracker *operations.RenameTracker
	cacheMgr      *cache.Manager
	xattrMgr      *xattr.Manager
}

// NewPathResolver creates a new PathResolver instance
func NewPathResolver(srcDir, cachePath, mountPoint string, renameTracker *operations.RenameTracker, cacheMgr *cache.Manager, xattrMgr *xattr.Manager) *PathResolver {
	return &PathResolver{
		srcDir:        srcDir,
		cachePath:     cachePath,
		mountPoint:    mountPoint,
		renameTracker: renameTracker,
		cacheMgr:      cacheMgr,
		xattrMgr:      xattrMgr,
	}
}

// ResolvePaths resolves a mount point path to both cache and source paths
// Returns: (cachePath, sourcePath, isIndependent, errno)
// - cachePath: path in cache directory (may not exist)
// - sourcePath: resolved source path (original path if renamed)
// - isIndependent: true if path is cache-independent
// - errno: error code (0 on success)
func (pr *PathResolver) ResolvePaths(mountPointPath string) (cachePath, sourcePath string, isIndependent bool, errno syscall.Errno) {
	// Normalize mount point path to relative path
	mountPointRelative := strings.TrimPrefix(mountPointPath, pr.mountPoint)
	mountPointRelative = strings.TrimPrefix(mountPointRelative, "/")
	mountPointRelative = strings.Trim(mountPointRelative, "/")

	if mountPointRelative == "" {
		return pr.cachePath, pr.srcDir, false, 0
	}

	if pr.xattrMgr == nil {
		// No xattr manager - return path as-is
		sourcePath = filepath.Join(pr.srcDir, mountPointRelative)
		cachePath = pr.cacheMgr.ResolveCachePath(sourcePath)
		return cachePath, sourcePath, false, 0
	}

	if pr.renameTracker != nil {
		resolved, independent, found := pr.renameTracker.ResolveRenamedPath(mountPointRelative)
		if found {
			if independent {
				sourcePath = filepath.Join(pr.srcDir, mountPointRelative)
				cachePath = filepath.Join(pr.cachePath, mountPointRelative)
				return cachePath, sourcePath, true, 0
			}
			sourcePath = filepath.Join(pr.srcDir, resolved)
			cachePath = filepath.Join(pr.cachePath, mountPointRelative)
			return cachePath, sourcePath, false, 0
		}
	}

	parts := strings.Split(mountPointRelative, "/")
	if len(parts) == 0 {
		sourcePath = pr.srcDir
		cachePath = pr.cachePath
		return cachePath, sourcePath, false, 0
	}

	resolvedPath := mountPointRelative
	currentPath := ""

	for i := 0; i < len(parts); i++ {
		if currentPath == "" {
			currentPath = parts[i]
		} else {
			currentPath = filepath.Join(currentPath, parts[i])
		}

		// Convert mount point relative path directly to cache path to check xattr
		cachePathForXattr := filepath.Join(pr.cachePath, currentPath)

		// Check if this path exists in cache and has xattr
		var st syscall.Stat_t
		if err := syscall.Lstat(cachePathForXattr, &st); err == nil {
			// Path exists in cache - check xattr
			attr, exists, errno := pr.xattrMgr.GetStatus(cachePathForXattr)
			if errno != 0 {
				return "", "", false, errno
			}
			if exists && attr != nil {
				if attr.CacheIndependent {
					// Populate memory tracker with independent flag
					if pr.renameTracker != nil {
						currentPathNormalized := strings.Trim(currentPath, "/")
						// Store as independent in memory
						pr.renameTracker.StoreRenameMapping(currentPathNormalized, currentPathNormalized, true)
					}
					// Return current mount point path (no resolution needed)
					// Use mount point relative path directly for cache path
					sourcePath = filepath.Join(pr.srcDir, mountPointRelative)
					cachePath = filepath.Join(pr.cachePath, mountPointRelative)
					return cachePath, sourcePath, true, 0
				}

				// Check if this path was renamed
				renamedFrom := attr.GetRenamedFromPath()
				if renamedFrom != "" {
					// This path was renamed - replace the prefix
					renamedFromNormalized := strings.Trim(renamedFrom, "/")
					currentPathNormalized := strings.Trim(currentPath, "/")

					// Check if mountPointRelative starts with currentPath
					if mountPointRelative == currentPathNormalized || strings.HasPrefix(mountPointRelative, currentPathNormalized+"/") {
						// Replace currentPath prefix with renamedFrom
						suffix := strings.TrimPrefix(mountPointRelative, currentPathNormalized)
						suffix = strings.TrimPrefix(suffix, "/")
						if suffix == "" {
							resolvedPath = renamedFromNormalized
						} else {
							resolvedPath = filepath.Join(renamedFromNormalized, suffix)
						}
						// Populate in-memory tracker for future fast lookups
						if pr.renameTracker != nil {
							// Store mapping: original source path -> current mount point path
							pr.renameTracker.StoreRenameMapping(renamedFromNormalized, currentPathNormalized, false)
						}
						// Update currentPath to use resolved path for next iteration (for nested renames)
						currentPath = resolvedPath
					}
				}
			}
		}
	}

	// Reconstruct resolved source path
	sourcePath = filepath.Join(pr.srcDir, resolvedPath)
	// Convert mount point relative path to cache path
	cachePath = filepath.Join(pr.cachePath, mountPointRelative)
	return cachePath, sourcePath, isIndependent, 0
}

// ResolveForRead resolves paths for read operations (files/directories)
// Handles renamed paths, deletion markers, and cache priority
// Returns: (cachePath, sourcePath, isIndependent, errno)
func (pr *PathResolver) ResolveForRead(mountPointPath string) (cachePath, sourcePath string, isIndependent bool, errno syscall.Errno) {
	// First resolve paths (handles rename tracking)
	cachePath, sourcePath, isIndependent, errno = pr.ResolvePaths(mountPointPath)
	if errno != 0 {
		return "", "", false, errno
	}

	// Check if path is marked as deleted in cache
	deleted, errno := pr.cacheMgr.IsDeleted(cachePath)
	if errno != 0 && errno != syscall.ENODATA {
		return "", "", false, errno
	}
	if deleted {
		return "", "", false, syscall.ENOENT
	}

	return cachePath, sourcePath, isIndependent, 0
}

// ResolveForWrite resolves paths for write operations
// Ensures cache path exists, handles renamed paths
// Returns: (cachePath, sourcePath, errno)
func (pr *PathResolver) ResolveForWrite(mountPointPath string) (cachePath, sourcePath string, errno syscall.Errno) {
	// Resolve paths (handles rename tracking)
	cachePath, sourcePath, isIndependent, errno := pr.ResolvePaths(mountPointPath)
	if errno != 0 {
		return "", "", errno
	}

	// For write operations, we always use cache path
	// If path is independent, sourcePath is still returned for reference but won't be used
	_ = isIndependent

	return cachePath, sourcePath, 0
}

// DumpState returns a string representation of the resolver state
// Useful for debugging
func (pr *PathResolver) DumpState() string {
	var result strings.Builder
	result.WriteString("PathResolver State:\n")
	result.WriteString("  srcDir: " + pr.srcDir + "\n")
	result.WriteString("  cachePath: " + pr.cachePath + "\n")
	result.WriteString("  mountPoint: " + pr.mountPoint + "\n")
	if pr.renameTracker != nil {
		result.WriteString("  RenameTracker:\n")
		result.WriteString(pr.renameTracker.DumpTrie())
	} else {
		result.WriteString("  RenameTracker: <nil>\n")
	}
	return result.String()
}

// VerifyPath verifies resolution for a path and returns debug information
func (pr *PathResolver) VerifyPath(mountPointPath string) (cachePath, sourcePath string, isIndependent bool, debugInfo string, errno syscall.Errno) {
	var debug strings.Builder
	debug.WriteString("Verifying path: " + mountPointPath + "\n")

	// Normalize mount point path to relative path
	mountPointRelative := strings.TrimPrefix(mountPointPath, pr.mountPoint)
	mountPointRelative = strings.TrimPrefix(mountPointRelative, "/")
	mountPointRelative = strings.Trim(mountPointRelative, "/")
	debug.WriteString("  Relative path: " + mountPointRelative + "\n")

	// Check memory tracker
	if pr.renameTracker != nil {
		resolved, independent, found := pr.renameTracker.ResolveRenamedPath(mountPointRelative)
		debug.WriteString("  Memory tracker: found=" + boolToString(found) + ", resolved=" + resolved + ", independent=" + boolToString(independent) + "\n")
		if found {
			if independent {
				sourcePath = filepath.Join(pr.srcDir, mountPointRelative)
				cachePath = pr.cacheMgr.ResolveCachePath(sourcePath)
				return cachePath, sourcePath, true, debug.String(), 0
			}
			sourcePath = filepath.Join(pr.srcDir, resolved)
			cachePath = pr.cacheMgr.ResolveCachePath(mountPointPath)
			return cachePath, sourcePath, false, debug.String(), 0
		}
	}

	// Resolve using full logic
	cachePath, sourcePath, isIndependent, errno = pr.ResolveForRead(mountPointPath)
	debug.WriteString("  Final resolution: cachePath=" + cachePath + ", sourcePath=" + sourcePath + ", independent=" + boolToString(isIndependent) + "\n")
	return cachePath, sourcePath, isIndependent, debug.String(), errno
}

func boolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
