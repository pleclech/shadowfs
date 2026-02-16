package fs

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/pleclech/shadowfs/fs/cache"
	"github.com/pleclech/shadowfs/fs/ipc"
	"github.com/pleclech/shadowfs/fs/operations"
	"github.com/pleclech/shadowfs/fs/pathutil"
	"github.com/pleclech/shadowfs/fs/rootinit"
	"github.com/pleclech/shadowfs/fs/utils"
	"github.com/pleclech/shadowfs/fs/xattr"
)

type ShadowNode struct {
	fs.LoopbackNode

	sessionPath string
	cachePath   string
	mountPoint  string
	srcDir      string
	mountID     string
	mirrorPath  string

	// Stored in root node and accessible to all child nodes
	server *fuse.Server

	// Helper utilities
	helpers *ShadowNodeHelpers

	// Git functionality (optional)
	gitManager *GitManager

	// Clean operation handlers
	cacheMgr        *cache.Manager
	xattrMgr        *xattr.Manager
	renameOp        *operations.RenameOperation
	lookupOp        *operations.LookupOperation
	directoryOp     *operations.DirectoryOperation
	typeReplacement *operations.TypeReplacement
	deletion        *operations.Deletion
	renameTracker   *operations.RenameTracker
	pathResolver    *PathResolver

	// Track recently truncated files to prevent COW after truncation
	truncatedFiles map[string]bool

	// Track dirty files with reference count
	// ref count = number of open handles (write + read handles)
	// Uses mount point paths (not cache paths) because what's dirty is what's visible at the mount point
	dirtyFiles      map[string]int // mount point path -> number of open handles
	dirtyFilesMutex sync.RWMutex

	// Track whether write handle has closed for dirty files
	// Used to distinguish write handle closing from read handle closing when count becomes 0
	dirtyFilesWriteClosed map[string]bool // mount point path -> write handle closed

	// IPC server for CLI communication (optional)
	ipcServer *ipc.ControlServer
}

func (n *ShadowNode) GetMountPoint() string {
	return n.mountPoint
}

func (n *ShadowNode) GetMountID() string {
	return n.mountID
}

// SetServer stores the FUSE server reference for safe entry invalidation
func (n *ShadowNode) SetServer(server *fuse.Server) {
	n.server = server
	// Propagate to all existing child nodes (they inherit via newShadowNode)
}

// getServer gets the server reference, traversing up to root if needed
func (n *ShadowNode) getServer() *fuse.Server {
	if n.server != nil {
		return n.server
	}
	// Try to get from root node
	if root := n.Root(); root != nil {
		if ops := root.Operations(); ops != nil {
			if rootNode, ok := ops.(*ShadowNode); ok {
				return rootNode.server
			}
		}
	}
	return nil
}

// StartIPCServer starts the IPC server for CLI communication
// This should be called after mount in main.go
// Works in both daemon and non-daemon mode
func (n *ShadowNode) StartIPCServer() error {
	// Only start IPC server on root node
	if n.mountPoint == "" || n.mountID == "" {
		return fmt.Errorf("IPC server can only be started on root node")
	}

	socketPath, err := cache.GetSocketPath(n.mountID)
	if err != nil {
		return fmt.Errorf("failed to get socket path: %w", err)
	}

	// Ensure daemon directory exists (needed for non-daemon mode)
	daemonDir, err := cache.GetDaemonDirPath()
	if err != nil {
		return fmt.Errorf("failed to get daemon directory: %w", err)
	}
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		return fmt.Errorf("failed to create daemon directory: %w", err)
	}

	Debug("StartIPCServer: Creating IPC server for mountID=%s, mountPoint=%s, srcDir=%s, socketPath=%s", n.mountID, n.mountPoint, n.srcDir, socketPath)
	server, err := ipc.NewControlServer(socketPath, n)
	if err != nil {
		return fmt.Errorf("failed to create IPC server: %w", err)
	}

	n.ipcServer = server
	server.Start()
	log.Printf("IPC server started: %s (mountID: %s)", socketPath, n.mountID)
	return nil
}

// StopIPCServer stops the IPC server and removes the socket file
// This should be called during cleanup before unmount
func (n *ShadowNode) StopIPCServer() error {
	if n.ipcServer == nil {
		return nil
	}

	if err := n.ipcServer.Stop(); err != nil {
		return fmt.Errorf("failed to stop IPC server: %w", err)
	}

	n.ipcServer = nil
	return nil
}

// MarkDirty marks a file as dirty (public method for IPC)
// This is called by IPC server when CLI restore operations mark files as dirty
func (n *ShadowNode) MarkDirty(filePath string) {
	n.markDirty(filePath)
}

// Refresh invalidates the FUSE cache for a path so it gets re-read from cache
// This is called by IPC server after CLI restore operations modify the cache
func (n *ShadowNode) Refresh(mountPointPath string) {
	Debug("Refresh: Marking file as dirty for path: %s", mountPointPath)

	// Mark as dirty to force re-read from cache on next access
	n.markDirty(mountPointPath)
	Debug("Refresh: Marked %s as dirty", mountPointPath)
}

// RestoreFile handles file restoration - marks as dirty AND invalidates directory entry
func (n *ShadowNode) RestoreFile(mountPointPath string) {
	n.markDirty(mountPointPath)

	// Convert mount point path to path relative to mount point root
	mountRoot := n.mountPoint
	relPath := mountPointPath
	if strings.HasPrefix(mountPointPath, mountRoot) {
		relPath = strings.TrimPrefix(mountPointPath, mountRoot)
		relPath = strings.TrimPrefix(relPath, "/")
	}

	parentRelPath := filepath.Dir(relPath)
	filename := filepath.Base(mountPointPath)

	// Clear independent flag from rename tracker when restoring a file
	// When a file is deleted, it's marked as "independent" in the rename tracker.
	// We need to clear this flag so Lookup doesn't treat the file as deleted.
	if n.pathResolver != nil && n.pathResolver.renameTracker != nil {
		n.pathResolver.renameTracker.RemoveRenameMapping(relPath)
	}

	// Also clear the deletion marker xattr if present
	// When a file is deleted, it has a deletion marker xattr
	// We need to remove this so Lookup doesn't treat the file as deleted
	// Use pathutil directly to convert mount point path to cache path
	cachePath := pathutil.RebaseToCache(mountPointPath, n.cachePath, n.mountPoint)
	if n.xattrMgr != nil {
		var attr xattr.XAttr
		exists, errno := xattr.Get(cachePath, &attr)
		if errno != 0 && errno != syscall.ENODATA {
		} else if exists {
			n.xattrMgr.Clear(cachePath)
		}
	}

	// Helper function to invalidate both regular and negative entries
	invalidateEntries := func(node *ShadowNode, name string) {
		node.InvalidateEntry(context.Background(), name)
		node.InvalidateNegativeEntry(context.Background(), name)
	}

	// Handle case where parentRelPath is "." (file at root of mount point)
	// or parentRelPath is empty (also file at root)
	if parentRelPath == "." || parentRelPath == "" {
		// File is at root of mount point - invalidate on root node
		// Call invalidate multiple times to ensure kernel cache is refreshed
		// This is a workaround for kernel negative dentry caching issues
		for i := 0; i < 3; i++ {
			invalidateEntries(n, filename)
		}
	} else if filename != "" && filename != "." {
		if parentRelPath == "/" {
			for i := 0; i < 3; i++ {
				invalidateEntries(n, filename)
			}
		} else {
			parentNode := n.walkToPath(parentRelPath)
			if parentNode != nil {
				for i := 0; i < 3; i++ {
					invalidateEntries(parentNode, filename)
				}
			} else {
				for i := 0; i < 3; i++ {
					invalidateEntries(n, filename)
				}
			}
		}
	}
}

// walkToPath walks from root to the given path and returns the node
func (n *ShadowNode) walkToPath(targetPath string) *ShadowNode {
	if targetPath == "" || targetPath == "." {
		return n
	}

	relPath := targetPath
	rootPath := n.RootData.Path

	if strings.HasPrefix(targetPath, rootPath) {
		relPath = strings.TrimPrefix(targetPath, rootPath)
		relPath = strings.TrimPrefix(relPath, "/")
	}

	if relPath == "" {
		return n
	}

	current := n
	parts := strings.Split(relPath, "/")

	for _, part := range parts {
		if part == "" {
			continue
		}

		child := current.getChild(part)
		if child == nil {
			return nil
		}

		shadowNode, ok := child.(*ShadowNode)
		if !ok {
			return nil
		}

		current = shadowNode
	}

	return current
}

// getChild gets a child node by name from the inode
func (n *ShadowNode) getChild(name string) interface{} {
	inode := n.EmbeddedInode()
	if inode == nil {
		return nil
	}

	// Get the child inode
	childInode := inode.GetChild(name)
	if childInode == nil {
		return nil
	}

	// Get the operations
	ops := childInode.Operations()
	return ops
}

// markDirty marks a file as dirty and sets ref count to 1 (the write handle)
// filePath should be a mount point path (not cache path) because what's dirty is what's visible at the mount point
// If file is already dirty, increments ref count instead of resetting
// This handles the case where multiple writes occur before Release() is called
func (n *ShadowNode) markDirty(filePath string) {
	n.dirtyFilesMutex.Lock()
	defer n.dirtyFilesMutex.Unlock()
	if n.dirtyFiles == nil {
		n.dirtyFiles = make(map[string]int)
	}
	if n.dirtyFilesWriteClosed == nil {
		n.dirtyFilesWriteClosed = make(map[string]bool)
	}
	if count, exists := n.dirtyFiles[filePath]; exists {
		// File already dirty - increment ref count (another write handle)
		n.dirtyFiles[filePath] = count + 1
		Debug("markDirty: Incremented ref count for %s to %d (already dirty)", filePath, count+1)
	} else {
		// File not dirty - set ref count to 1 (first write handle)
		n.dirtyFiles[filePath] = 1
		n.dirtyFilesWriteClosed[filePath] = false // Write handle is open
		Debug("markDirty: Marked %s as dirty (ref count: 1)", filePath)
	}
}

// decrementDirtyRef decrements reference count when a handle closes
// filePath should be a mount point path (not cache path)
// Returns true if dirty flag was cleared
// When write handle closes (count was 1 → 0), keep entry with count 0 to allow reads that happen after
// When read handle closes and count becomes 0 (count was 1 → 0), delete entry (write handle already closed)
func (n *ShadowNode) decrementDirtyRef(filePath string) bool {
	n.dirtyFilesMutex.Lock()
	defer n.dirtyFilesMutex.Unlock()
	if n.dirtyFiles == nil {
		return false
	}
	if n.dirtyFilesWriteClosed == nil {
		n.dirtyFilesWriteClosed = make(map[string]bool)
	}
	if count, exists := n.dirtyFiles[filePath]; exists {
		oldCount := count
		count--
		if count < 0 {
			// Shouldn't happen, but handle gracefully
			delete(n.dirtyFiles, filePath)
			delete(n.dirtyFilesWriteClosed, filePath)
			Debug("decrementDirtyRef: Negative ref count for %s, clearing", filePath)
			return true
		} else if count == 0 {
			writeClosed := n.dirtyFilesWriteClosed[filePath]
			if !writeClosed && oldCount == 1 {
				// Write handle closing (count was 1 → 0, write handle not yet marked closed)
				// Keep entry with count 0 for pending reads
				n.dirtyFiles[filePath] = 0
				n.dirtyFilesWriteClosed[filePath] = true // Mark write handle closed
				Debug("decrementDirtyRef: Write handle closed for %s, keeping entry (count=0) for pending reads", filePath)
				return false
			} else if writeClosed {
				// Read handle closing and count becomes 0 (write handle already closed)
				// Safe to delete - all handles closed
				delete(n.dirtyFiles, filePath)
				delete(n.dirtyFilesWriteClosed, filePath)
				Debug("decrementDirtyRef: Last read handle closed for %s (write handle already closed), cleared dirty flag", filePath)
				return true
			} else {
				// Shouldn't happen: count is 0 but write handle not closed and oldCount != 1
				// Handle gracefully - keep entry
				n.dirtyFiles[filePath] = 0
				Debug("decrementDirtyRef: Unexpected state for %s (count=0, writeClosed=%v, oldCount=%d), keeping entry", filePath, writeClosed, oldCount)
				return false
			}
		} else {
			n.dirtyFiles[filePath] = count
			Debug("decrementDirtyRef: Decremented ref count for %s from %d to %d", filePath, oldCount, count)
			return false
		}
	}
	return false
}

// checkDirtyAndIncrementRef atomically checks if file is dirty and increments ref count
// filePath should be a mount point path (not cache path)
// Returns true if file was dirty and ref count was incremented
// This prevents race conditions where dirty flag is cleared between check and increment
// Handles count 0 case: if count is 0, it means write handle closed but file was recently written
func (n *ShadowNode) checkDirtyAndIncrementRef(filePath string) bool {
	n.dirtyFilesMutex.Lock()
	defer n.dirtyFilesMutex.Unlock()
	if n.dirtyFiles == nil {
		Debug("checkDirtyAndIncrementRef: dirtyFiles map is nil, checking for %s", filePath)
		return false
	}
	// Debug: Log all dirty files to see what's stored
	if len(n.dirtyFiles) > 0 {
		Debug("checkDirtyAndIncrementRef: Current dirty files: %v", n.dirtyFiles)
	}
	if count, exists := n.dirtyFiles[filePath]; exists {
		// File is dirty (even if count is 0, it means write handle closed but file was recently written)
		n.dirtyFiles[filePath] = count + 1
		Debug("checkDirtyAndIncrementRef: Found dirty file %s (count was %d), incremented to %d", filePath, count, count+1)
		return true
	}
	Debug("checkDirtyAndIncrementRef: File %s not found in dirtyFiles map", filePath)
	return false
}

func (n *ShadowNode) InvalidateEntry(ctx context.Context, name string) {
	server := n.getServer()
	if server == nil {
		return
	}
	var parentIno uint64
	parentInode := n.EmbeddedInode()
	if parentInode != nil {
		parentIno = parentInode.StableAttr().Ino
	}
	if parentIno == 0 {
		root := n.Root()
		if root != nil {
			rootInode := root.EmbeddedInode()
			if rootInode != nil {
				parentIno = rootInode.StableAttr().Ino
			}
		}
	}
	if parentIno == 0 {
		parentIno = 1
	}
	if parentIno != 0 {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					Debug("InvalidateEntry: EntryNotify panicked (ignored): %v", r)
				}
			}()
			server.EntryNotify(parentIno, name)
		}()
	}
}

// InvalidateNegativeEntry safely invalidates a negative dentry (useful after rm)
func (n *ShadowNode) InvalidateNegativeEntry(ctx context.Context, name string) {
	server := n.getServer()
	if server == nil {
		return
	}
	var parentIno uint64
	parentInode := n.EmbeddedInode()
	if parentInode != nil {
		parentIno = parentInode.StableAttr().Ino
	}
	if parentIno == 0 {
		root := n.Root()
		if root != nil {
			rootInode := root.EmbeddedInode()
			if rootInode != nil {
				parentIno = rootInode.StableAttr().Ino
			}
		}
	}
	if parentIno == 0 {
		parentIno = 1
	}
	if parentIno != 0 {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					Debug("InvalidateNegativeEntry: EntryNotify panicked (ignored): %v", r)
				}
			}()
			server.EntryNotify(parentIno, name)
		}()
	}
}

// InitGitManager initializes Git functionality for auto-versioning
func (n *ShadowNode) InitGitManager(config GitConfig) error {
	n.gitManager = NewGitManager(n.mountPoint, n.srcDir, n.cachePath, config, n.pathResolver, n.xattrMgr)

	// Note: IPC client is set automatically in GetGitRepository() when called from CLI
	// For FUSE context, files written through FUSE are automatically marked dirty in Write()
	// so no IPC client is needed here

	// Initialize Git repository
	if err := n.gitManager.InitializeRepo(); err != nil {
		return err
	}

	return nil
}

// HandleWriteActivity records file activity for Git tracking
func (n *ShadowNode) HandleWriteActivity(filePath string) {
	// ignore all .git/ files
	if strings.Contains(filePath, ".gitofs/") {
		Debug("HandleWriteActivity: Ignoring .gitofs file: %s", filePath)
		return
	}

	// Convert cache paths to mount point paths for git operations
	// Git operations expect paths relative to the mount point (workspace)
	mountPointPath := filePath
	if strings.HasPrefix(filePath, n.cachePath) {
		// Path is in cache - convert to mount point path
		mountPointPath = n.RebasePathUsingMountPoint(filePath)
		Debug("HandleWriteActivity: Converted cache path %s -> mount point path %s", filePath, mountPointPath)
	} else {
		Debug("HandleWriteActivity: Using path as-is (not cache path): %s", filePath)
	}

	// Skip auto-commit for shadowfs metadata files (they are handled by dedicated operations)
	if strings.Contains(mountPointPath, ".shadowfs-metadata/") {
		Debug("HandleWriteActivity: Skipping auto-commit for metadata file %s", mountPointPath)
		return
	}

	// Track file activity using GitManager directly
	if n.gitManager != nil && n.gitManager.IsEnabled() {
		Debug("HandleWriteActivity: Tracking activity for %s", mountPointPath)
		n.gitManager.AutoCommitFile(mountPointPath, "File modification")
	} else {
		Debug("HandleWriteActivity: Git manager is nil or disabled, cannot track %s", mountPointPath)
	}
}

// commitDeletion commits a file or directory deletion to git
func (n *ShadowNode) commitDeletion(mountPointPath string) error {
	if n.gitManager == nil || !n.gitManager.IsEnabled() {
		return nil // Git disabled, skip
	}

	// Convert mount point path to relative path
	relativePath := strings.TrimPrefix(mountPointPath, n.mountPoint)
	relativePath = strings.TrimPrefix(relativePath, "/")
	relativePath = strings.Trim(relativePath, "/")

	if relativePath == "" {
		return nil // Root path, skip
	}

	// Normalize path separators
	relativePath = filepath.ToSlash(relativePath)

	// Call GitManager's commitDeletion
	return n.gitManager.commitDeletion(relativePath)
}

// commitDirectoryDeletionRecursive commits a directory deletion recursively to git
func (n *ShadowNode) commitDirectoryDeletionRecursive(mountPointPath string) error {
	if n.gitManager == nil || !n.gitManager.IsEnabled() {
		return nil // Git disabled, skip
	}

	// Convert mount point path to relative path
	relativePath := strings.TrimPrefix(mountPointPath, n.mountPoint)
	relativePath = strings.TrimPrefix(relativePath, "/")
	relativePath = strings.Trim(relativePath, "/")

	if relativePath == "" {
		return nil // Root path, skip
	}

	// Normalize path separators
	relativePath = filepath.ToSlash(relativePath)

	// Call GitManager's commitDirectoryDeletionRecursive
	return n.gitManager.commitDirectoryDeletionRecursive(relativePath)
}

// CleanupGitManager cleans up Git resources and commits all pending changes
func (n *ShadowNode) CleanupGitManager() {
	if n.gitManager != nil {
		// Stop accepting new commits
		n.gitManager.Stop()
	}
}

func (n *ShadowNode) FullPath(useCache bool) string {
	// path returns the full path to the file in the underlying file
	if useCache && n.mirrorPath != "" {
		return n.mirrorPath
	}

	// Safely get root - handle case where inode doesn't have a root yet
	// This can happen during inode initialization (e.g., in Create operations)
	var path string
	func() {
		defer func() {
			if r := recover(); r != nil {
				// Root() panicked - inode doesn't have a root yet
				// Fallback: use RootData.Path directly
				path = n.RootData.Path
			}
		}()
		root := n.Root()
		if root == nil {
			path = n.RootData.Path
			return
		}
		path = n.Path(root)
		// Debug: Log path resolution for nested directories
		if strings.Contains(path, "level") {
			Debug("FullPath: n.Path(root)=%s, RootData.Path=%s", path, n.RootData.Path)
		}
	}()

	if path == "" {
		// Fallback if both methods failed
		return n.RootData.Path
	}

	result := filepath.Join(n.RootData.Path, path)
	// Debug: Log final path for nested directories
	if strings.Contains(result, "level") {
		Debug("FullPath: final path=%s", result)
	}
	return result
}

func (n *ShadowNode) IsCached(path string) bool {

	return n.mirrorPath != ""
}

func (r *ShadowNode) idFromStat(st *syscall.Stat_t) fs.StableAttr {
	// We compose an inode number by the underlying inode, and
	// mixing in the device number. In traditional filesystems,
	// the inode numbers are small. The device numbers are also
	// small (typically 16 bit). Finally, we mask out the root
	// device number of the root, so a loopback FS that does not
	// encompass multiple mounts will reflect the inode numbers of
	// the underlying filesystem
	swapped := (uint64(st.Dev) << 32) | (uint64(st.Dev) >> 32)
	rDev := r.RootData.Dev
	swappedRootDev := (rDev << 32) | (rDev >> 32)

	return fs.StableAttr{
		Mode: uint32(st.Mode),
		Gen:  1,
		// This should work well for traditional backing FSes,
		// not so much for other go-fuse FS-es
		Ino: (swapped ^ swappedRootDev) ^ st.Ino,
	}
}

func (n *ShadowNode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	// Get the mount point path - Statfs should report stats for the mount point itself
	mountPointPath := n.GetMountPoint()
	if mountPointPath == "" {
		// Fallback: try to get from root node
		if root := n.Root(); root != nil {
			if ops := root.Operations(); ops != nil {
				if rootNode, ok := ops.(*ShadowNode); ok {
					mountPointPath = rootNode.GetMountPoint()
				}
			}
		}
		if mountPointPath == "" {
			// Last resort: use the source directory
			mountPointPath = n.srcDir
		}
	}

	s := syscall.Statfs_t{}
	err := syscall.Statfs(mountPointPath, &s)
	if err != nil {
		return fs.ToErrno(err)
	}
	out.FromStatfsT(&s)
	return fs.OK
}

func (n *ShadowNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate name parameter to prevent path traversal
	if !utils.ValidateName(name) {
		return nil, syscall.EPERM
	}

	// Get parent path relative to mount point (needed for child lookup)
	var parentPathRelative string
	func() {
		defer func() {
			if r := recover(); r != nil {
				parentPathRelative = ""
			}
		}()
		root := n.Root()
		if root != nil {
			parentPathRelative = n.Path(root)
		}
	}()

	// Use clean lookup operation per Phase 1.2
	if n.lookupOp == nil {
		// Fallback if not initialized (shouldn't happen)
		n.lookupOp = operations.NewLookupOperation(n.cacheMgr, n.srcDir)
	}

	// parentPathRelative is already set above

	// Construct mount point path for current name (e.g., "foo/baz/hello")
	currentMountPointPath := filepath.Join(parentPathRelative, name)
	if parentPathRelative == "" {
		currentMountPointPath = name
	}
	fullMountPointPath := filepath.Join(n.mountPoint, currentMountPointPath)

	// Use PathResolver to resolve paths
	var st syscall.Stat_t
	var mirrorPath string
	var sourcePath string
	var isIndependent bool

	// Resolve parent path to check if it's independent
	if parentPathRelative != "" {
		parentMountPointPath := filepath.Join(n.mountPoint, parentPathRelative)
		_, _, parentIndependent, errno := n.pathResolver.ResolveForRead(parentMountPointPath)
		if errno != 0 && errno != syscall.ENOENT {
			return nil, errno
		}
		if parentIndependent {
			// Parent is independent - don't check source, return ENOENT
			return nil, syscall.ENOENT
		}
	}

	// Resolve current path using PathResolver
	cachePath, resolvedSourcePath, isIndependent, errno := n.pathResolver.ResolveForRead(fullMountPointPath)
	if errno == syscall.ENOENT {
		return nil, syscall.ENOENT
	}
	if errno != 0 {
		return nil, errno
	}

	// Check if found in cache
	if err := syscall.Lstat(cachePath, &st); err == nil {
		// Found in cache - use it
		mirrorPath = cachePath
		sourcePath = resolvedSourcePath
	} else {
		// Not in cache - use resolved source path
		mirrorPath = ""
		sourcePath = resolvedSourcePath
	}

	// Check if parent directory is type-replaced BEFORE trying to look up child
	// If parent is type-replaced (file -> directory), we MUST NOT fall back to source
	// because source still has the old file, and Lstat will fail with "not a directory"
	parentTypeReplaced := false
	if n.mirrorPath != "" && n.xattrMgr != nil {
		attr, exists, errno := n.xattrMgr.GetStatus(n.mirrorPath)
		if errno == 0 && exists && attr.TypeReplaced {
			parentTypeReplaced = true
		}
	}

	// If path is independent or parent is type-replaced, don't fall back to source
	if isIndependent || parentTypeReplaced {
		if mirrorPath == "" {
			return nil, syscall.ENOENT
		}
	}

	// CRITICAL: If path is independent (deleted), don't fall back to source even if it exists there
	// This handles the case where a directory was deleted but still exists in source
	if isIndependent && mirrorPath == "" {
		return nil, syscall.ENOENT
	}

	// If not found in cache, check source
	if mirrorPath == "" {
		if err := syscall.Lstat(sourcePath, &st); err != nil {
			return nil, fs.ToErrno(err)
		}
	}

	// Fix directory permissions if needed
	if st.Mode&syscall.S_IFDIR != 0 {
		pathToFix := mirrorPath
		if pathToFix == "" {
			pathToFix = sourcePath
		}
		if err := utils.EnsureDirPermissions(pathToFix); err == nil {
			// Re-stat after fixing
			if mirrorPath != "" {
				syscall.Lstat(mirrorPath, &st)
			} else {
				syscall.Lstat(sourcePath, &st)
			}
		}
		// Ensure execute bits
		permBits := st.Mode & 0777
		if permBits&0111 == 0 {
			st.Mode = (st.Mode &^ 0777) | (permBits | 0755)
		}
	}

	// Create new inode
	out.Attr.FromStat(&st)
	rootData := n.RootData
	inode := newShadowNode(rootData, n.EmbeddedInode(), name, &st)

	if shadowNode, ok := inode.(*ShadowNode); ok {
		shadowNode.mirrorPath = mirrorPath
	}

	stableAttr := n.idFromStat(&st)

	ch := n.NewInode(ctx, inode, stableAttr)

	return ch, 0
}

func (n *ShadowNode) Access(ctx context.Context, mask uint32) syscall.Errno {
	// Get mount point path from inode path (not from mirrorPath which might be cache path)
	var mountPointPath string
	func() {
		defer func() {
			if r := recover(); r != nil {
				mountPointPath = n.mountPoint
			}
		}()
		root := n.Root()
		if root != nil {
			relativePath := n.Path(root)
			if relativePath == "" {
				mountPointPath = n.mountPoint
			} else {
				mountPointPath = filepath.Join(n.mountPoint, relativePath)
			}
		} else {
			mountPointPath = n.mountPoint
		}
	}()

	// Use PathResolver to resolve paths
	cachePath, sourcePath, isIndependent, errno := n.pathResolver.ResolveForRead(mountPointPath)
	if errno == syscall.ENOENT {
		return syscall.ENOENT
	}
	if errno != 0 {
		return errno
	}

	// Check cache first
	if err := syscall.Access(cachePath, mask); err == nil {
		return 0
	}

	// If independent, don't check source
	if isIndependent {
		return syscall.ENOENT
	}

	// Fall back to source path (resolved if renamed)
	if err := syscall.Access(sourcePath, mask); err != nil {
		return fs.ToErrno(err)
	}

	return 0
}

func (n *ShadowNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {

	// CRITICAL: For cached files, NEVER use FileGetattrer because the file handle
	// might be stale after rename operations (e.g., config.lock -> config)
	// Always do a fresh stat to get current attributes
	if f != nil && n.mirrorPath != "" {

	} else if f != nil {
		if fga, ok := f.(fs.FileGetattrer); ok {

			return fga.Getattr(ctx, out)
		}
		// CRITICAL: If we have a file handle, try to get fresh attributes from it
		// This ensures we get the correct file size after writes
		if fileWithFd, ok := f.(interface{ Fd() int }); ok {
			if fd := fileWithFd.Fd(); fd >= 0 {
				var st syscall.Stat_t
				if err := syscall.Fstat(fd, &st); err == nil {
					// Use fstat result for files - this has the most up-to-date size

					out.FromStat(&st)
					return fs.OK
				}
			}
		}
		// If file handle doesn't implement FileGetattrer or Fd() failed, fall through to path-based stat
	}

	// Cache independence: Attributes are read from cache if available, otherwise from source.
	// Source directory changes during mount are not detected to maintain cache independence.
	// Get mount point path from inode path (not from mirrorPath which might be cache path)
	var mountPointPath string
	func() {
		defer func() {
			if r := recover(); r != nil {
				mountPointPath = n.mountPoint
			}
		}()
		root := n.Root()
		if root != nil {
			relativePath := n.Path(root)
			if relativePath == "" {
				mountPointPath = n.mountPoint
			} else {
				mountPointPath = filepath.Join(n.mountPoint, relativePath)
			}
		} else {
			mountPointPath = n.mountPoint
		}
	}()

	// Use PathResolver to resolve paths
	cachePath, resolvedSourcePath, _, errno := n.pathResolver.ResolveForRead(mountPointPath)
	if errno == syscall.ENOENT {
		return syscall.ENOENT
	}
	if errno != 0 {
		return errno
	}

	st := syscall.Stat_t{}
	var sourcePath string

	// CRITICAL: Always check if file should be read from cache first
	// This handles cases where file was renamed after mirrorPath was set
	if err := syscall.Lstat(cachePath, &st); err == nil {
		// File exists in cache - use cached file's attributes
		sourcePath = cachePath // Use cache path for permission fixing
	} else {
		// File not in cache, use resolved source path
		sourcePath = resolvedSourcePath
		if &n.Inode == n.Root() {
			err = syscall.Stat(sourcePath, &st)
		} else {
			err = syscall.Lstat(sourcePath, &st)
		}
		if err != nil {
			return fs.ToErrno(err)
		}
	}

	// CRITICAL: For directories, ALWAYS fix permissions and re-stat to get fresh attributes
	// This ensures FUSE always sees correct permissions, even if kernel cached wrong ones
	if st.Mode&syscall.S_IFDIR != 0 {
		// Always fix permissions for directories - don't just check
		// This is critical because FUSE might cache wrong attributes
		// Use sourcePath (resolved if renamed) for permission fixing
		if err := utils.EnsureDirPermissions(sourcePath); err != nil {

			// Continue - we'll fix st.Mode below
		} else {
			// Re-stat after fixing to get FRESH attributes from filesystem
			// This ensures we're not using stale cached attributes
			// Use sourcePath (resolved if renamed) for re-stat
			if &n.Inode == n.Root() {
				if err2 := syscall.Stat(sourcePath, &st); err2 == nil {
				}
			} else {
				if err2 := syscall.Lstat(sourcePath, &st); err2 == nil {
					// st now has fresh correct permissions
				}
			}
		}

		// Ensure st.Mode has correct execute bits BEFORE FromStat
		permBits := st.Mode & 0777
		if permBits&0111 == 0 {
			// Fix execute bits in stat structure
			st.Mode = (st.Mode &^ 0777) | (permBits | 0755)
		}
	} else {
		// CRITICAL: For files, if we have a mirrorPath (file is in cache),
		// ensure we get fresh attributes after writes

		if n.mirrorPath != "" && strings.HasPrefix(sourcePath, n.cachePath) {
			// Force a fresh stat to get updated file size after writes
			if err2 := syscall.Lstat(sourcePath, &st); err2 == nil {
				// st now has fresh attributes including correct size

			}
		} else {
			// Check if file should be in cache even if sourcePath doesn't start with cachePath
			cachedPath := n.RebasePathUsingCache(mountPointPath)

			if err2 := syscall.Lstat(cachedPath, &st); err2 == nil {

				// Use the cached file's attributes
				sourcePath = cachedPath
			}
		}
	}

	out.FromStat(&st)

	// Use mount-level timeouts (set in mount options) - no per-entry override needed

	return fs.OK
}

func (n *ShadowNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	p := n.FullPath(true)

	fsa, ok := f.(fs.FileSetattrer)
	if ok && fsa != nil {
		fsa.Setattr(ctx, in, out)
	} else {
		if m, ok := in.GetMode(); ok {
			if err := syscall.Chmod(p, m); err != nil {
				return fs.ToErrno(err)
			}
		}

		uid, uok := in.GetUID()
		gid, gok := in.GetGID()
		if uok || gok {
			suid := -1
			sgid := -1
			if uok {
				suid = int(uid)
			}
			if gok {
				sgid = int(gid)
			}
			if err := syscall.Chown(p, suid, sgid); err != nil {
				return fs.ToErrno(err)
			}
		}

		mtime, mok := in.GetMTime()
		atime, aok := in.GetATime()

		if mok || aok {

			ap := &atime
			mp := &mtime
			if !aok {
				ap = nil
			}
			if !mok {
				mp = nil
			}
			var ts [2]syscall.Timespec
			ts[0] = fuse.UtimeToTimespec(ap)
			ts[1] = fuse.UtimeToTimespec(mp)

			if err := syscall.UtimesNano(p, ts[:]); err != nil {
				return fs.ToErrno(err)
			}
		}

		if sz, ok := in.GetSize(); ok {
			if err := syscall.Truncate(p, int64(sz)); err != nil {
				return fs.ToErrno(err)
			}
		}
	}

	fga, ok := f.(fs.FileGetattrer)
	if ok && fga != nil {
		fga.Getattr(ctx, out)
	} else {
		st := syscall.Stat_t{}
		err := syscall.Lstat(p, &st)
		if err != nil {
			return fs.ToErrno(err)
		}
		out.FromStat(&st)
	}
	return fs.OK
}

// ensureDirPermissions ensures a directory has proper execute permissions.
// It checks if the path is a directory and ensures it has at least 0755 permissions.
// Always chmods to ensure kernel sees correct permissions immediately.
func ensureDirPermissions(path string) error {
	return utils.EnsureDirPermissions(path)
}

func (n *ShadowNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (inode *fs.Inode, fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	// Validate name parameter to prevent path traversal
	if !utils.ValidateName(name) {
		return nil, nil, 0, syscall.EPERM
	}

	sourcePath := filepath.Join(n.FullPath(false), name)
	p := n.RebasePathUsingCache(sourcePath)

	// Since we're always using cache path, check if file is deleted in cache
	attr := xattr.XAttr{}
	var xattrErrno syscall.Errno
	isPathInCache, xattrErrno := xattr.Get(p, &attr)
	if xattrErrno != 0 && xattrErrno != syscall.ENODATA {
		return nil, nil, 0, xattrErrno
	}

	if isPathInCache && xattr.IsPathDeleted(attr) {
		// File was deleted - remove the deletion marker and shadow file
		// Create new empty file (don't restore from source - cache is independent)
		err := syscall.Unlink(p)
		if err != nil && !os.IsNotExist(err) {
			return nil, nil, 0, fs.ToErrno(err)
		}
		// Remove the xattr deletion marker
		xattr.Remove(p)
		// Clear rename mapping if path was renamed before deletion
		// Recreation after deletion creates independent path (not linked to source)
		if n.renameTracker != nil {
			mountPointPath := pathutil.RebaseToMountPoint(p, n.mountPoint, n.cachePath)
			mountPointPath = strings.TrimPrefix(mountPointPath, n.mountPoint)
			mountPointPath = strings.TrimPrefix(mountPointPath, "/")
			if mountPointPath != "" {
				n.renameTracker.RemoveRenameMapping(mountPointPath)
			}
		}
		// Clear rename mapping from xattr
		if n.xattrMgr != nil {
			n.xattrMgr.ClearRenameMapping(p)
		}
		// Mark as independent (recreated path is not linked to source)
		n.setCacheIndependent(p)
	}

	// need to create file in cache
	// create directory if not exists recursively using same permissions
	parentDir := filepath.Dir(p)
	cachedDir, err := cache.CreateMirroredDir(parentDir, n.cachePath, n.srcDir)
	if err != nil && cachedDir != n.cachePath {
		return nil, nil, 0, fs.ToErrno(err)
	}
	// Check if parent directory is accessible before trying to create file
	// This respects directory permissions for security tests
	if err := syscall.Access(cachedDir, 0x2); err != nil {
		return nil, nil, 0, syscall.EACCES
	}

	// Ensure parent directory has execute permissions
	if err := utils.EnsureDirPermissions(cachedDir); err != nil {
		// Don't fail - try to continue
	}

	// Get source path to check permissions
	var sourceSt syscall.Stat_t
	fileMode := mode
	if err := syscall.Lstat(sourcePath, &sourceSt); err == nil {
		// Source file exists - use its permissions
		fileMode = uint32(sourceSt.Mode)
	}
	// Create empty file - content will be copied on first write
	mode = fileMode // Use source permissions if available

	// Preserve O_APPEND flag - kernel handles append semantics correctly
	// Don't strip it here as we need it for proper append behavior

	fd, err := syscall.Open(p, int(flags)|os.O_CREATE, mode)
	if err != nil {

		return nil, nil, 0, fs.ToErrno(err)
	}

	// CRITICAL: If O_TRUNC was requested in Create, mark file as independent immediately
	// This ensures Write() won't do COW - user wants to overwrite
	if flags&syscall.O_TRUNC != 0 {
		cachePathForXattr := p
		if !strings.HasPrefix(p, n.cachePath) {
			cachePathForXattr = n.RebasePathUsingCache(p)
		}
		n.setCacheIndependent(cachePathForXattr)
	}

	utils.PreserveOwner(ctx, p)

	st := syscall.Stat_t{}
	if err := syscall.Fstat(fd, &st); err != nil {
		syscall.Close(fd)
		return nil, nil, 0, fs.ToErrno(err)
	}

	node := newShadowNode(n.RootData, n.EmbeddedInode(), name, &st)
	if shadowNode, ok := node.(*ShadowNode); ok {
		shadowNode.mirrorPath = p
	}

	// Use recover to handle potential nil pointer in test contexts
	var ch *fs.Inode
	func() {
		defer func() {
			if r := recover(); r != nil {
				// In test contexts, return nil to avoid panic
				ch = nil
			}
		}()
		ch = n.NewInode(ctx, node, n.idFromStat(&st))
	}()
	lf := fs.NewLoopbackFile(fd)

	out.FromStat(&st)

	// Track write activity for Git auto-versioning
	// only file and for writing flags
	hasWriteFlags := (flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_TRUNC|syscall.O_APPEND|syscall.O_CREAT) != 0)
	if hasWriteFlags {
		n.HandleWriteActivity(p)
	}

	// Invalidate parent directory cache to ensure kernel sees new file
	n.NotifyContent(0, 0)

	return ch, lf, 0, 0
}

func (n *ShadowNode) Flush(ctx context.Context, f fs.FileHandle, t *time.Time) syscall.Errno {
	// If file was opened with DIRECT_IO, invalidate entry to clear FUSE cache
	// This ensures reads after close see the updated content
	p := n.FullPath(true)
	cachePath := p
	if !strings.HasPrefix(p, n.cachePath) {
		cachePath = n.RebasePathUsingCache(p)
	}

	rebasedMountPath := n.RebasePathUsingMountPoint(cachePath)

	// Get parent node and invalidate entry
	// Note: NotifyContent and EntryNotify can block if called from within a FUSE operation handler,
	// so we make them non-blocking by calling them in goroutines
	// The dirty flag mechanism handles cache invalidation for reads, so this is best-effort
	if inode := n.EmbeddedInode(); inode != nil {
		// NotifyContent can block, so call it in a goroutine
		go func() {
			defer func() {
				if r := recover(); r != nil {
					// Ignore panics from NotifyContent - it's best-effort
					Debug("Flush: NotifyContent panicked (ignored): %v", r)
				}
			}()
			inode.NotifyContent(0, 0)
		}()

		_, parentInode := inode.Parent()
		if parentInode != nil {
			parentIno := parentInode.StableAttr().Ino
			fileName := filepath.Base(rebasedMountPath)

			server := n.getServer()
			if server != nil {
				// Invalidate directory entry - call in goroutine to avoid blocking
				// The dirty flag mechanism ensures reads use DIRECT_IO when needed
				go func() {
					defer func() {
						if r := recover(); r != nil {
							// Ignore panics from EntryNotify - it's best-effort
							Debug("Flush: EntryNotify panicked (ignored): %v", r)
						}
					}()
					server.EntryNotify(parentIno, fileName)
				}()
			}
		}
	}

	// Also update timestamps to trigger cache refresh
	// CRITICAL: Use cache path, not mount point path, to avoid deadlock
	// Calling os.Chtimes on mount point path triggers FUSE Setattr, which can deadlock
	// when called from within a FUSE operation handler (Flush is called from Write)
	if t == nil {
		now := time.Now()
		t = &now
	}

	// Update timestamps on cache file directly (bypasses FUSE, avoids deadlock)
	if err := os.Chtimes(cachePath, *t, *t); err != nil {
		// Log but don't fail - timestamp update is best-effort
		Debug("Flush: Failed to update timestamps for %s: %v (ignored)", cachePath, err)
	}

	return 0
}

func (n *ShadowNode) Release(ctx context.Context, f fs.FileHandle) syscall.Errno {
	p := n.FullPath(true)

	// Resolve mount point path (not cache path) because what's dirty is what's visible at the mount point
	mountPointPath := p
	if strings.HasPrefix(p, n.cachePath) {
		// p is a cache path, convert to mount point path
		mountPointPath = n.RebasePathUsingMountPoint(p)
	} else if !strings.HasPrefix(p, n.mountPoint) {
		// p might be a source path, convert via cache path
		cachePath := n.RebasePathUsingCache(p)
		mountPointPath = n.RebasePathUsingMountPoint(cachePath)
	}

	// Decrement reference count
	// If count reaches 0, keep entry (don't delete) to allow reads that happen after write handle closes
	// Entry will be deleted when a read handle closes and count is 0 (meaning write handle already closed)
	cleared := n.decrementDirtyRef(mountPointPath)
	if cleared {
		Debug("Release: Cleared dirty flag for %s (all handles closed)", mountPointPath)
	}

	return 0
}

func (n *ShadowNode) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	// Get mount point path from inode path (not from mirrorPath which might be source or cache)
	// This ensures we always resolve from the mount point perspective
	var mountPointPath string
	func() {
		defer func() {
			if r := recover(); r != nil {
				// Fallback if Path() panics
				mountPointPath = n.mountPoint
			}
		}()
		root := n.Root()
		if root != nil {
			relativePath := n.Path(root)
			if relativePath == "" {
				mountPointPath = n.mountPoint
			} else {
				mountPointPath = filepath.Join(n.mountPoint, relativePath)
			}
		} else {
			mountPointPath = n.mountPoint
		}
	}()

	forceCache := flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_TRUNC|syscall.O_APPEND|syscall.O_CREAT) != 0
	hasAppend := flags&syscall.O_APPEND != 0
	hasTrunc := flags&syscall.O_TRUNC != 0
	// Preserve O_APPEND flag - kernel handles append semantics correctly
	// Strip O_TRUNC from flags passed to syscall.Open, but handle truncation manually if needed
	flags = flags &^ syscall.O_TRUNC

	perms := uint32(0)
	var cachePath, sourcePath string
	var isIndependent bool
	var resolveErrno syscall.Errno
	var p string // Will be set to either cachePath or sourcePath depending on where file exists

	if forceCache {
		// For write operations, use ResolveForWrite
		cachePath, sourcePath, resolveErrno = n.pathResolver.ResolveForWrite(mountPointPath)
		if resolveErrno != 0 {
			return nil, 0, resolveErrno
		}

		// CRITICAL: Commit original state from source BEFORE any writes happen
		// This ensures the first version is preserved even when COW is skipped
		Debug("Open(write): Checking for original state commit: mountPoint=%s, cachePath=%s, sourcePath=%s", mountPointPath, cachePath, sourcePath)
		if n.gitManager != nil && n.gitManager.IsEnabled() && sourcePath != "" {
			// Check if file is independent (if independent, no original state to commit)
			isIndependent := false
			// Check if file exists in cache first (xattr only exists if file exists)
			var cacheSt syscall.Stat_t
			fileExistsInCache := syscall.Lstat(cachePath, &cacheSt) == nil
			if fileExistsInCache && n.xattrMgr != nil {
				attr, exists, errno := n.xattrMgr.GetStatus(cachePath)
				if errno == 0 && exists && attr != nil {
					isIndependent = attr.CacheIndependent
					Debug("Open(write): File exists in cache, isIndependent=%v", isIndependent)
				} else {
					Debug("Open(write): File exists in cache but no xattr or error: exists=%v, errno=%v", exists, errno)
				}
			} else if !fileExistsInCache {
				Debug("Open(write): File does not exist in cache yet, assuming not independent")
			}

			// Only commit original state if file is NOT independent and resolves to source
			if !isIndependent {
				// Check if source file exists
				var sourceSt syscall.Stat_t
				if err := syscall.Lstat(sourcePath, &sourceSt); err == nil {
					// Flush commit queue to ensure any pending commits complete
					if err := n.gitManager.flushCommitQueue(); err != nil {
						Debug("Open(write): Failed to flush commit queue: %v (continuing)", err)
						// Continue even if flush fails - don't block Open()
					}

					// Get relative path for Git
					relativePath, err := n.gitManager.normalizePath(mountPointPath)
					if err == nil {
						// Check if file hasn't been committed yet
						if !n.gitManager.hasFileBeenCommitted(relativePath) {
							Debug("Open(write): Copying original state from source before writes: %s (source: %s)", relativePath, sourcePath)
							// Get source file mode for CopyFile
							var sourceSt syscall.Stat_t
							mode := uint32(0644) // Default fallback
							if err := syscall.Lstat(sourcePath, &sourceSt); err == nil {
								mode = uint32(sourceSt.Mode)
							} else {
								Debug("Open(write): Failed to stat source file %s, using default mode 0644: %v", sourcePath, err)
							}
							var cacheStBefore syscall.Stat_t
							if err := syscall.Lstat(cachePath, &cacheStBefore); err == nil {
							}
							if errno := n.helpers.CopyFile(sourcePath, cachePath, mode); errno != 0 {
								Debug("Open(write): Failed to copy file %s to %s: %v (continuing)", sourcePath, cachePath, errno)
								return nil, 0, errno
							}
							var cacheStAfter syscall.Stat_t
							if err := syscall.Lstat(cachePath, &cacheStAfter); err == nil {
							}
							n.setCacheIndependent(cachePath)

							Debug("Open(write): Committing original state from source before writes: %s (source: %s)", relativePath, sourcePath)
							// Commit original state synchronously before any writes
							if err := n.gitManager.commitOriginalStateSync(relativePath); err != nil {
								// Log but don't fail - Open() should proceed even if commit fails
								Debug("Open(write): Failed to commit original state for %s: %v (continuing)", relativePath, err)
							} else {
								Debug("Open(write): Successfully committed original state for %s", relativePath)
							}
						} else {
							Debug("Open(write): File %s already committed, skipping original state commit", relativePath)
						}
					} else {
						Debug("Open(write): Failed to normalize path %s: %v (skipping original state commit)", mountPointPath, err)
					}
				} else {
					Debug("Open(write): Source file %s does not exist, skipping original state commit", sourcePath)
				}
			} else {
				Debug("Open(write): File %s is independent, skipping original state commit", mountPointPath)
			}
		}

		// Start activity tracking for write operations
		// This ensures we track writes and start the idle timer
		n.HandleWriteActivity(mountPointPath)
	} else {
		// For read operations, use ResolveForRead
		cachePath, sourcePath, isIndependent, resolveErrno = n.pathResolver.ResolveForRead(mountPointPath)
		if resolveErrno == syscall.ENOENT {
			return nil, 0, syscall.ENOENT
		}
		if resolveErrno != 0 {
			return nil, 0, resolveErrno
		}
	}

	cachedPath := cachePath
	st := syscall.Stat_t{}
	err := syscall.Lstat(cachedPath, &st)
	if err == nil {
		// File exists in cache - use it
		// But if opening with O_APPEND and cache file is empty, copy source content first
		copyOnWriteHappened := false
		if hasAppend && st.Size == 0 {
			var sourceSt syscall.Stat_t
			if err := syscall.Lstat(sourcePath, &sourceSt); err == nil && sourceSt.Size > 0 {
				// Source file exists and has content - copy it to cache before opening for append
				if errno := n.helpers.CopyFile(sourcePath, cachedPath, uint32(sourceSt.Mode)); errno != 0 {
					return nil, 0, errno
				}
				copyOnWriteHappened = true
			}
		}
		p = cachedPath
		n.mirrorPath = cachedPath

		// If copy-on-write happened, sync the file before opening file handle
		// Note: We don't track activity here - activity tracking happens in Write() after actual writes
		if copyOnWriteHappened && forceCache {
			// Open file temporarily to sync it, ensuring it's written to disk
			// This ensures git can see the file when checking changes
			if syncFd, err := syscall.Open(cachedPath, syscall.O_RDONLY, 0); err == nil {
				syscall.Fsync(syncFd)
				syscall.Close(syncFd)
				// Also sync parent directory to ensure directory entry is written to disk
				// This is especially important in daemon mode where timing can be critical
				if dirFd, err := syscall.Open(filepath.Dir(cachedPath), syscall.O_RDONLY, 0); err == nil {
					syscall.Fsync(dirFd)
					syscall.Close(dirFd)
				}
			}
		}
	} else if forceCache {
		// Need to create file in cache for write operations
		// create directory if not exists recursively using same permissions
		_, err = cache.CreateMirroredDir(filepath.Dir(cachedPath), n.cachePath, n.srcDir)
		if err != nil {
			return nil, 0, fs.ToErrno(err)
		}
		// get permissions from source file if it exists (only needed for write operations)
		err = syscall.Lstat(sourcePath, &st)
		if err == nil {
			perms = uint32(st.Mode)
		} else {
			perms = 0644 // default file permissions
		}

		// Copy-on-write for append operations: if opening with O_APPEND and source has content,
		// copy source content to cache before opening to preserve existing content
		copyOnWriteHappened := false
		if hasAppend {
			var sourceSt syscall.Stat_t
			if err := syscall.Lstat(sourcePath, &sourceSt); err == nil && sourceSt.Size > 0 {
				// Source file exists and has content - copy it to cache before opening for append
				if errno := n.helpers.CopyFile(sourcePath, cachedPath, uint32(sourceSt.Mode)); errno != 0 {
					return nil, 0, errno
				}
				// File already exists in cache now, don't need O_CREAT
				p = cachedPath
				n.mirrorPath = cachedPath
				copyOnWriteHappened = true
			} else {
				// Source doesn't exist or is empty - create empty file in cache
				flags = flags | syscall.O_CREAT
				p = cachedPath
				n.mirrorPath = cachedPath
			}
		} else {
			// Not append mode - create empty file in cache, copy-on-write will happen on first write
			flags = flags | syscall.O_CREAT
			p = cachedPath
			n.mirrorPath = cachedPath
		}

		// If copy-on-write happened, sync the file before opening file handle
		// Note: We don't track activity here - activity tracking happens in Write() after actual writes
		if copyOnWriteHappened {
			// Open file temporarily to sync it, ensuring it's written to disk
			// This ensures git can see the file when checking changes
			if syncFd, err := syscall.Open(cachedPath, syscall.O_RDONLY, 0); err == nil {
				syscall.Fsync(syncFd)
				syscall.Close(syncFd)
				// Also sync parent directory to ensure directory entry is written to disk
				// This is especially important in daemon mode where timing can be critical
				if dirFd, err := syscall.Open(filepath.Dir(cachedPath), syscall.O_RDONLY, 0); err == nil {
					syscall.Fsync(dirFd)
					syscall.Close(dirFd)
				}
			}
		}
	} else {
		// Read-only: use source file directly (no copy needed, no permission check needed)
		// If independent, don't check source
		if isIndependent {
			return nil, 0, syscall.ENOENT
		}
		// Use resolved source path for lazy children of renamed directories
		p = sourcePath
	}

	f, err := syscall.Open(p, int(flags), perms)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}

	// CRITICAL: If file was created in cache for write operations (not append, not trunc),
	// mark it as independent immediately to prevent COW in Write()
	// This handles os.WriteFile() which opens with WRONLY (no O_TRUNC) but wants to overwrite
	if forceCache && !hasAppend && !hasTrunc && strings.HasPrefix(p, n.cachePath) {
		n.setCacheIndependent(p)
	}

	// If O_TRUNC was requested, truncate the file now
	// We do this after opening because we stripped O_TRUNC from flags
	// Note: Original state commit is now handled earlier in Open() for all write operations
	if hasTrunc && !hasAppend {
		if err := syscall.Ftruncate(f, 0); err != nil {
			syscall.Close(f)
			return nil, 0, fs.ToErrno(err)
		}
		// CRITICAL: Also truncate the on-disk file to ensure Lstat() in Write() sees size=0
		// Ftruncate() only truncates the file handle, but Lstat() reads from disk
		// We need to sync or truncate the on-disk file to prevent COW from copying source content
		if err := syscall.Fsync(f); err != nil {
			// If fsync fails, continue anyway - truncation tracking will prevent COW
		}
		// CRITICAL: Mark file as independent immediately when O_TRUNC is used
		// This ensures Write() won't do COW even if truncatedFiles path matching fails
		// User wants to overwrite, so file should be independent from source
		// CRITICAL: Use cache path for xattr operations (p might be mount point path)
		cachePathForXattr := p
		if !strings.HasPrefix(p, n.cachePath) {
			cachePathForXattr = n.RebasePathUsingCache(p)
		}
		n.setCacheIndependent(cachePathForXattr)
		// Track that this file was truncated to prevent COW in Write()
		// Store both the cache path and the original path to ensure we can find it in Write()
		if n.truncatedFiles == nil {
			n.truncatedFiles = make(map[string]bool)
		}
		n.truncatedFiles[p] = true
		// Also store cachePath if different from p (for Write() lookup)
		if cachePath := n.RebasePathUsingCache(p); cachePath != p {
			n.truncatedFiles[cachePath] = true
		}
		// Also store mount point path for Write() lookup
		if mountPath := n.RebasePathUsingMountPoint(p); mountPath != p {
			n.truncatedFiles[mountPath] = true
		}
	}

	lf := fs.NewLoopbackFile(f)

	// Initialize fuseFlags to 0 to prevent undefined behavior
	fuseFlags = 0

	// Set appropriate cache flags based on file access pattern
	if forceCache {
		// Write operation - always use DIRECT_IO
		fuseFlags |= fuse.FOPEN_DIRECT_IO
		// Note: markDirty will be called in Write(), not here
	} else {
		// Read operation
		// Use mount point path (not cache path) because what's dirty is what's visible at the mount point
		// mountPointPath is already resolved above (line 1038-1053)

		// Debug: Log the mount point path being checked
		Debug("Open(read): Checking dirty flag for mountPointPath=%s (cachePath=%s)", mountPointPath, cachePath)

		// Atomically check if dirty and increment ref count
		// This prevents race conditions where dirty flag is cleared between check and increment
		if n.checkDirtyAndIncrementRef(mountPointPath) {
			// File is dirty - use DIRECT_IO for fresh read
			fuseFlags |= fuse.FOPEN_DIRECT_IO
			Debug("Open(read): File is dirty, using DIRECT_IO for %s", mountPointPath)
		} else {
			// File is clean - use cache for performance
			fuseFlags |= fuse.FOPEN_KEEP_CACHE
			Debug("Open(read): File is clean, using KEEP_CACHE for %s", mountPointPath)
		}
	}

	// Note: Activity tracking happens in Write() method after actual writes occur
	// Not tracking here to avoid premature tracking before copy-on-write completes

	return lf, fuseFlags, 0
}

func (n *ShadowNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	// Validate name parameters to prevent path traversal
	if !utils.ValidateName(name) || !utils.ValidateName(newName) {
		return syscall.EPERM
	}

	// Resolve paths - use source paths (not cache paths) per Phase 2.1
	sourcePath := filepath.Join(n.FullPath(false), name)
	destPath := filepath.Join(n.RootData.Path, newParent.EmbeddedInode().Path(nil), newName)

	// Handle destination being an existing directory (move file INTO directory)
	if newParentNode, ok := newParent.(*ShadowNode); ok {
		if destChild := newParentNode.GetChild(newName); destChild != nil {
			if destOps := destChild.EmbeddedInode().Operations(); destOps != nil {
				if destNode, ok := destOps.(*ShadowNode); ok {
					if destNode.mirrorPath != "" {
						var st syscall.Stat_t
						if err := syscall.Lstat(destNode.mirrorPath, &st); err == nil {
							if st.Mode&syscall.S_IFDIR != 0 {
								// Destination is a directory, move file inside it
								destPath = filepath.Join(destPath, name)
							}
						}
					}
				}
			}
		}
	}

	// CRITICAL: Ensure parent directory exists BEFORE FUSE tries to look it up
	// This implements Phase 2.3: Partial Cache Path Creation
	// FUSE will look up the parent directory before calling Rename, so we must create it proactively
	destDir := filepath.Dir(destPath)
	if n.cacheMgr != nil {
		if errno := n.cacheMgr.EnsurePath(destDir); errno != 0 {
			return errno
		}
	}

	// Handle RENAME_EXCHANGE using platform-specific implementation
	if flags&fs.RENAME_EXCHANGE != 0 {
		sourceCachePath := n.RebasePathUsingCache(sourcePath)
		destCachePath := n.RebasePathUsingCache(destPath)
		// Use rename operation's exchange method
		if n.renameOp == nil {
			n.renameOp = operations.NewRenameOperation(n.cacheMgr, n.srcDir, n.cachePath, n.renameTracker)
		}
		return n.renameOp.RenameExchange(sourceCachePath, destCachePath)
	}

	// Use clean rename operation per Phase 2.1 constraints
	if n.renameOp == nil {
		n.renameOp = operations.NewRenameOperation(n.cacheMgr, n.srcDir, n.cachePath, n.renameTracker)
	}

	// Perform rename using operation module
	// Create wrapper function that uses helpers.CopyFile
	// The rename operation expects a function with signature:
	// func(srcPath, destPath string, mode uint32) syscall.Errno
	copyFileWrapper := func(srcPath, destPath string, mode uint32) syscall.Errno {
		// Get source file mode if not provided (mode might be 0)
		if mode == 0 {
			var sourceSt syscall.Stat_t
			if err := syscall.Lstat(srcPath, &sourceSt); err == nil {
				mode = uint32(sourceSt.Mode)
			} else {
				mode = 0644 // Default fallback
			}
		}
		return n.helpers.CopyFile(srcPath, destPath, mode)
	}
	errno := n.renameOp.Rename(sourcePath, destPath, flags, copyFileWrapper)
	if errno != 0 {
		return errno
	}

	// Commit rename metadata to git
	if n.gitManager != nil && n.gitManager.IsEnabled() {
		// Convert source paths to relative paths for git commit
		// sourcePath and destPath are absolute source paths
		// Use QueueRenameMetadata to avoid FUSE deadlocks
		n.gitManager.QueueRenameMetadata(sourcePath, destPath)
	}

	// Update child node's mirrorPath after successful rename
	if child := n.GetChild(name); child != nil {
		if childOps := child.EmbeddedInode().Operations(); childOps != nil {
			if childNode, ok := childOps.(*ShadowNode); ok {
				childNode.mirrorPath = n.RebasePathUsingCache(destPath)
			}
		}
	}

	return 0
}

func (n *ShadowNode) RebasePathUsingCache(path string) string {
	return pathutil.RebaseToCache(path, n.cachePath, n.srcDir)
}

func (n *ShadowNode) RebasePathUsingMountPoint(path string) string {
	return pathutil.RebaseToMountPoint(path, n.mountPoint, n.cachePath)
}

func (n *ShadowNode) RebasePathUsingSrc(path string) string {
	return pathutil.RebaseToSource(path, n.srcDir, n.cachePath)
}

// setCacheIndependent updates both xattr and trie to mark a path as independent
func (n *ShadowNode) setCacheIndependent(cachePath string) {
	if n.xattrMgr != nil {
		n.xattrMgr.SetCacheIndependent(cachePath)
	}
	// Also update trie if we have a rename tracker
	if n.renameTracker != nil {
		// Convert cache path to mount point relative path
		mountPointPath := pathutil.RebaseToMountPoint(cachePath, n.mountPoint, n.cachePath)
		mountPointPath = strings.TrimPrefix(mountPointPath, n.mountPoint)
		mountPointPath = strings.TrimPrefix(mountPointPath, "/")
		if mountPointPath != "" {
			n.renameTracker.SetIndependent(mountPointPath)
		}
	}
}

func (n *ShadowNode) createMirroredDir(path string) (string, error) {
	return cache.CreateMirroredDir(path, n.cachePath, n.srcDir)
}

func (n *ShadowNode) CreateMirroredFileOrDir(path string) (string, error) {
	return cache.CreateMirroredFileOrDir(path, n.cachePath, n.srcDir)
}

func (n *ShadowNode) Unlink(ctx context.Context, name string) syscall.Errno {
	// Validate name parameter to prevent path traversal
	if !utils.ValidateName(name) {
		return syscall.EPERM
	}

	p := filepath.Join(n.FullPath(false), name)
	mirrorPath := n.RebasePathUsingCache(p)

	// check if file is already deleted in cache by reading xattr
	var deleted bool
	var errno syscall.Errno

	if n.helpers != nil {
		deleted, errno = n.helpers.CheckFileDeleted(mirrorPath)
	} else {
		// Fallback to original method
		attr := xattr.XAttr{}
		exists, errno := xattr.Get(mirrorPath, &attr)
		if errno == 0 {
			deleted = exists && xattr.IsPathDeleted(attr)
		}
	}

	if errno != 0 {
		return errno
	}

	// abort if file is already deleted in cache
	if deleted {
		return syscall.ENOENT
	}

	// check if file exists in cache
	if n.helpers != nil {
		_, errno = n.helpers.StatFile(mirrorPath, false)
	} else {
		// Fallback to original method
		st := syscall.Stat_t{}
		err := syscall.Lstat(mirrorPath, &st)
		if err != nil {
			errno = fs.ToErrno(err)
		}
	}

	if errno == 0 {
		// File exists in cache - mark as deleted with xattr before unlinking
		// This ensures we can track deletion even after the file is removed
		// Get original type from cache file
		var st syscall.Stat_t
		if err := syscall.Lstat(mirrorPath, &st); err != nil {
			return fs.ToErrno(err)
		}
		originalType := uint32(st.Mode & syscall.S_IFMT)

		// Mark as deleted with permanent independence
		if n.deletion == nil {
			n.deletion = operations.NewDeletion()
		}
		if errno := n.deletion.HandlePermanentIndependence(mirrorPath); errno != 0 {
			return errno
		}
		// Update trie: remove rename mapping and mark path as independent
		if n.renameTracker != nil {
			// Convert cache path to mount point relative path
			mountPointPath := pathutil.RebaseToMountPoint(mirrorPath, n.mountPoint, n.cachePath)
			mountPointPath = strings.TrimPrefix(mountPointPath, n.mountPoint)
			mountPointPath = strings.TrimPrefix(mountPointPath, "/")
			if mountPointPath != "" {
				// Remove rename mapping (deletion breaks the link to source)
				n.renameTracker.RemoveRenameMapping(mountPointPath)
				// Also clear rename mapping from xattr
				if n.xattrMgr != nil {
					n.xattrMgr.ClearRenameMapping(mirrorPath)
				}
				// Mark as independent
				n.renameTracker.SetIndependent(mountPointPath)
			}
		}

		// Update original type in xattr (preserving PathStatusDeleted and CacheIndependent)
		attr := xattr.XAttr{}
		if _, getErrno := xattr.Get(mirrorPath, &attr); getErrno == 0 {
			attr.OriginalType = originalType
			if attr.PathStatus != xattr.PathStatusDeleted {
				attr.PathStatus = xattr.PathStatusDeleted
			}
			if !attr.CacheIndependent {
				attr.CacheIndependent = true
			}
			xattr.Set(mirrorPath, &attr)
		} else {
			attr = xattr.XAttr{
				PathStatus:       xattr.PathStatusDeleted,
				OriginalType:     originalType,
				CacheIndependent: true,
			}
			xattr.Set(mirrorPath, &attr)
		}

		// Now unlink the file (xattr persists even after file is removed on some filesystems,
		// but we keep the deletion marker file to be safe)
		// Actually, we should keep the deletion marker file, not unlink it!
		// The deletion marker file with xattr is what we use to track deletion
		// So we DON'T unlink it - we just mark it as deleted

		// Get child inode number before removing it from tree for DeleteNotify
		var childIno uint64
		if child := n.GetChild(name); child != nil {
			childIno = child.StableAttr().Ino
		}

		// Invalidate entry before removing child inode from tree
		n.InvalidateEntry(ctx, name)

		// Notify kernel that child inode is deleted
		if childIno != 0 {
			server := n.getServer()
			if server != nil {
				parentIno := n.EmbeddedInode().StableAttr().Ino
				server.DeleteNotify(parentIno, childIno, name)
			}
		}

		// Remove child inode from FUSE tree
		n.RmChild(name)

		// Commit deletion to git (best-effort, don't fail unlink if commit fails)
		mountPointPath := pathutil.RebaseToMountPoint(mirrorPath, n.mountPoint, n.cachePath)
		if err := n.commitDeletion(mountPointPath); err != nil {
			log.Printf("Unlink: Failed to commit deletion for %s: %v (unlink succeeded)", mountPointPath, err)
			// Don't fail unlink operation - commit is best-effort
		}

		return 0
	}

	// Check if file is in source
	var srcSt *syscall.Stat_t
	if n.helpers != nil {
		srcSt, errno = n.helpers.StatFile(p, false)
	} else {
		srcSt = &syscall.Stat_t{}
		err := syscall.Lstat(p, srcSt)
		if err != nil {
			errno = fs.ToErrno(err)
		}
	}

	if errno != 0 {
		return errno
	}

	// Verify it's a regular file (not a directory)
	if srcSt.Mode&syscall.S_IFDIR != 0 {
		return syscall.EISDIR
	}

	// Ensure parent directory exists in cache before creating deletion marker
	// This implements Phase 2.3: Partial Cache Path Creation
	if n.cacheMgr != nil {
		parentDir := filepath.Dir(p)
		if errno := n.cacheMgr.EnsurePath(parentDir); errno != 0 {
			return errno
		}
	}

	// Use mirrorPath (already computed) to ensure consistency with isDeleted checks
	// Create deletion marker file if it doesn't exist
	var st syscall.Stat_t
	if err := syscall.Lstat(mirrorPath, &st); err != nil {
		if os.IsNotExist(err) {
			// Create empty file as deletion marker
			fd, err := syscall.Open(mirrorPath, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_TRUNC, 0644)
			if err != nil {
				return fs.ToErrno(err)
			}
			syscall.Close(fd)
		} else {
			return fs.ToErrno(err)
		}
	}

	// Use permanent independence operation per Phase 3.3
	if n.deletion == nil {
		n.deletion = operations.NewDeletion()
	}
	// Get original type from source
	var originalType uint32 = syscall.S_IFREG
	if srcSt != nil {
		originalType = uint32(srcSt.Mode & syscall.S_IFMT)
	}
	// Mark as deleted with permanent independence
	// Use mirrorPath to ensure consistency with isDeleted checks
	if errno := n.deletion.HandlePermanentIndependence(mirrorPath); errno != 0 {
		// Cleanup: if xattr setting failed, remove the shadow file we created
		syscall.Unlink(mirrorPath)
		return errno
	}
	// Update trie: remove rename mapping and mark path as independent
	if n.renameTracker != nil {
		// Convert cache path to mount point relative path
		mountPointPath := pathutil.RebaseToMountPoint(mirrorPath, n.mountPoint, n.cachePath)
		mountPointPath = strings.TrimPrefix(mountPointPath, n.mountPoint)
		mountPointPath = strings.TrimPrefix(mountPointPath, "/")
		if mountPointPath != "" {
			// Remove rename mapping (deletion breaks the link to source)
			n.renameTracker.RemoveRenameMapping(mountPointPath)
			// Also clear rename mapping from xattr
			if n.xattrMgr != nil {
				n.xattrMgr.ClearRenameMapping(mirrorPath)
			}
			// Mark as independent
			n.renameTracker.SetIndependent(mountPointPath)
		}
	}
	// Update original type in xattr (preserving PathStatusDeleted and CacheIndependent)
	attr := xattr.XAttr{}
	if _, getErrno := xattr.Get(mirrorPath, &attr); getErrno == 0 {
		// Preserve PathStatus and CacheIndependent when updating OriginalType
		attr.OriginalType = originalType
		// Ensure PathStatusDeleted is still set
		if attr.PathStatus != xattr.PathStatusDeleted {
			attr.PathStatus = xattr.PathStatusDeleted
		}
		// Ensure CacheIndependent is still set (all deleted items are permanently independent)
		if !attr.CacheIndependent {
			attr.CacheIndependent = true
		}
		xattr.Set(mirrorPath, &attr)
	} else {
		// If xattr doesn't exist, set it with PathStatusDeleted, OriginalType, and CacheIndependent
		attr = xattr.XAttr{
			PathStatus:       xattr.PathStatusDeleted,
			OriginalType:     originalType,
			CacheIndependent: true, // All deleted items are permanently independent
		}
		xattr.Set(mirrorPath, &attr)
	}

	// Get child inode number before removing it from tree for DeleteNotify
	var childIno uint64
	if child := n.GetChild(name); child != nil {
		childIno = child.StableAttr().Ino
	}

	// Invalidate entry before removing child inode from tree
	n.InvalidateEntry(ctx, name)

	// Notify kernel that child inode is deleted
	if childIno != 0 {
		server := n.getServer()
		if server != nil {
			parentIno := n.EmbeddedInode().StableAttr().Ino
			server.DeleteNotify(parentIno, childIno, name)
		}
	}

	// Remove child inode from FUSE tree
	n.RmChild(name)

	return 0
}

func (n *ShadowNode) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate name parameter to prevent path traversal
	if !utils.ValidateName(name) {
		return nil, syscall.EPERM
	}

	// Validate symlink target - prevent absolute paths and path traversal
	if filepath.IsAbs(target) || strings.Contains(target, "..") {
		return nil, syscall.EPERM
	}

	p := filepath.Join(n.FullPath(false), name)

	// change path to cache
	p = n.RebasePathUsingCache(p)

	// create directory if not exists recursively using same permissions
	cachedDir, err := n.createMirroredDir(filepath.Dir(p))
	if err != nil && cachedDir != n.cachePath {
		return nil, fs.ToErrno(err)
	}
	p = filepath.Join(cachedDir, filepath.Base(p))

	err = syscall.Symlink(target, p)
	if err != nil {
		return nil, fs.ToErrno(err)
	}

	utils.PreserveOwner(ctx, p)

	st := syscall.Stat_t{}
	if err := syscall.Lstat(p, &st); err != nil {
		syscall.Unlink(p)
		return nil, fs.ToErrno(err)
	}

	out.Attr.FromStat(&st)

	node := newShadowNode(n.RootData, n.EmbeddedInode(), name, &st)
	node.(*ShadowNode).mirrorPath = p

	ch := n.NewInode(ctx, node, n.idFromStat(&st))

	return ch, 0
}

func (n *ShadowNode) Readlink(ctx context.Context) (string, syscall.Errno) {
	p := n.FullPath(true)

	// read symlink target
	buf := make([]byte, 4096)
	bytesRead, err := syscall.Readlink(p, buf)
	if err != nil {
		return "", fs.ToErrno(err)
	}

	return string(buf[:bytesRead]), 0
}

func (n *ShadowNode) Link(ctx context.Context, target fs.InodeEmbedder, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate name parameter to prevent path traversal
	if !utils.ValidateName(name) {
		return nil, syscall.EPERM
	}

	p := filepath.Join(n.FullPath(false), name)

	// get target path
	targetOps := target.EmbeddedInode().Operations()
	if targetOps == nil {
		return nil, syscall.EINVAL
	}
	targetNode, ok := targetOps.(*ShadowNode)
	if !ok {
		return nil, syscall.EINVAL
	}
	targetPath := targetNode.FullPath(true)

	// change path to cache
	p = n.RebasePathUsingCache(p)

	// create directory if not exists recursively using same permissions
	cachedDir, err := n.createMirroredDir(filepath.Dir(p))
	if err != nil && cachedDir != n.cachePath {
		return nil, fs.ToErrno(err)
	}
	p = filepath.Join(cachedDir, filepath.Base(p))

	err = syscall.Link(targetPath, p)
	if err != nil {
		return nil, fs.ToErrno(err)
	}

	utils.PreserveOwner(ctx, p)

	st := syscall.Stat_t{}
	if err := syscall.Lstat(p, &st); err != nil {
		syscall.Unlink(p)
		return nil, fs.ToErrno(err)
	}

	out.Attr.FromStat(&st)

	// Update the target node's mirrorPath to point to the new linked location
	// This is crucial for Git's object creation pattern where it creates a temp file
	// then links it to the final object name
	targetNode.mirrorPath = p

	// Return the existing target node, not a new one
	return target.EmbeddedInode(), 0
}

func newShadowNode(rootData *fs.LoopbackRoot, parent *fs.Inode, _ string, _ *syscall.Stat_t) fs.InodeEmbedder {
	n := &ShadowNode{
		LoopbackNode: fs.LoopbackNode{
			RootData: rootData,
		},
	}

	if parent != nil {
		if ops := parent.EmbeddedInode().Operations(); ops != nil {
			parentNode := ops.(*ShadowNode)
			n.sessionPath = parentNode.sessionPath
			n.cachePath = parentNode.cachePath
			n.mountPoint = parentNode.mountPoint
			n.srcDir = parentNode.srcDir
			n.mountID = parentNode.mountID
			// Inherit git manager from parent
			n.gitManager = parentNode.gitManager
			// Inherit operation handlers from parent
			n.cacheMgr = parentNode.cacheMgr
			n.xattrMgr = parentNode.xattrMgr
			n.renameOp = parentNode.renameOp
			n.lookupOp = parentNode.lookupOp
			n.directoryOp = parentNode.directoryOp
			n.typeReplacement = parentNode.typeReplacement
			n.deletion = parentNode.deletion
			n.renameTracker = parentNode.renameTracker
			n.pathResolver = parentNode.pathResolver
			// Inherit server reference from parent (for safe entry invalidation)
			n.server = parentNode.server
			// Inherit truncated files map from parent (for COW prevention)
			n.truncatedFiles = parentNode.truncatedFiles
			// Inherit dirty files map from parent (shared across all nodes)
			n.dirtyFiles = parentNode.dirtyFiles
			// Inherit write closed tracking map from parent (shared across all nodes)
			n.dirtyFilesWriteClosed = parentNode.dirtyFilesWriteClosed
			// Inherit helpers from parent
			if parentNode.helpers != nil {
				n.helpers = parentNode.helpers
			} else {
				n.helpers = NewShadowNodeHelpers(n)
			}
		}
	}

	// CRITICAL: Ensure helpers is always initialized
	// This guarantees helpers is never nil, allowing us to remove defensive checks
	// Even if parent is nil or inheritance fails, helpers will be available
	if n.helpers == nil {
		n.helpers = NewShadowNodeHelpers(n)
	}

	return n
}

func NewShadowRoot(inMountPoint, inSrcDir, cacheBaseDir string) (fs.InodeEmbedder, error) {
	mountPoint, err := rootinit.GetMountPoint(inMountPoint)
	if err != nil {
		return nil, fmt.Errorf("get mount point error:\n%w", err)
	}

	srcDir, err := rootinit.GetMountPoint(inSrcDir)
	if err != nil {
		return nil, fmt.Errorf("get source directory error:\n%w", err)
	}

	// Compute mount ID using centralized function
	mountID := cache.ComputeMountID(mountPoint, srcDir)

	var baseCacheDir string
	if cacheBaseDir != "" {
		// Validate and normalize
		baseCacheDir, err = filepath.Abs(cacheBaseDir)
		if err != nil {
			return nil, fmt.Errorf("invalid cache directory: %w", err)
		}
		if err := rootinit.ValidateCacheDirectory(baseCacheDir); err != nil {
			return nil, fmt.Errorf("cache directory validation failed: %w", err)
		}
	} else {
		// Use centralized function to get cache base directory
		baseCacheDir, err = cache.GetCacheBaseDir()
		if err != nil {
			return nil, fmt.Errorf("get cache base directory error:\n%w", err)
		}
	}

	// Create session directory using centralized function
	sessionPath := cache.GetSessionPath(baseCacheDir, mountID)
	if err := rootinit.CreateDir(sessionPath, 0755); err != nil {
		return nil, fmt.Errorf("creating session directory error:\n%w", err)
	}

	cachePath := cache.GetCachePath(sessionPath)
	if err := rootinit.CreateDir(cachePath, 0755); err != nil {
		return nil, fmt.Errorf("creating cache directory error:\n%w", err)
	}

	// create a file into the session directory indicating the target dir
	targetFile := cache.GetTargetFilePath(sessionPath)
	if err := rootinit.WriteFileOnce(targetFile, []byte(srcDir), 0444); err != nil {
		return nil, fmt.Errorf("creating target file error:\n%w", err)
	}

	rootData := &fs.LoopbackRoot{
		NewNode: newShadowNode,
		Path:    srcDir,
	}

	root := &ShadowNode{
		LoopbackNode: fs.LoopbackNode{
			RootData: rootData,
		},
		sessionPath: sessionPath,
		cachePath:   cachePath,
		mountPoint:  mountPoint,
		srcDir:      srcDir,
		mountID:     mountID,
		mirrorPath:  cachePath,
	}

	// Initialize helpers after all fields are set
	root.helpers = NewShadowNodeHelpers(root)

	// Initialize clean operation handlers
	root.cacheMgr = cache.NewManager(cachePath, srcDir)
	root.xattrMgr = xattr.NewManager()
	root.renameTracker = operations.NewRenameTracker()
	root.renameOp = operations.NewRenameOperation(root.cacheMgr, srcDir, cachePath, root.renameTracker)
	root.lookupOp = operations.NewLookupOperation(root.cacheMgr, srcDir)
	root.directoryOp = operations.NewDirectoryOperation(root.cacheMgr, srcDir, root.renameTracker)
	root.typeReplacement = operations.NewTypeReplacement()
	root.deletion = operations.NewDeletion()
	root.pathResolver = NewPathResolver(srcDir, cachePath, mountPoint, root.renameTracker, root.cacheMgr, root.xattrMgr)

	// Initialize truncatedFiles map at root (shared across all nodes)
	root.truncatedFiles = make(map[string]bool)
	// Initialize dirtyFiles map at root (shared across all nodes)
	root.dirtyFiles = make(map[string]int)
	// Initialize dirtyFilesWriteClosed map at root (shared across all nodes)
	root.dirtyFilesWriteClosed = make(map[string]bool)

	return root, nil
}
