package fs

// // This file is kept for backward compatibility but functions have been moved:
// // - Mount discovery functions moved to fs/rootinit/discovery.go
// // - Git repository functions moved to fs/git_manager.go
// // This file may be removed in a future version.

// // Re-export functions from their new locations for backward compatibility
// import (
// 	"github.com/pleclech/shadowfs/fs/rootinit"
// )

// // FindCacheDirectory finds the cache directory for a given mount point
// // Deprecated: Use rootinit.FindCacheDirectory instead
// func FindCacheDirectory(mountPoint string) (string, error) {
// 	return rootinit.FindCacheDirectory(mountPoint)
// }
