package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/pleclech/shadowfs/fs/cache"
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

	// Helper utilities
	helpers *ShadowNodeHelpers

	// Git functionality (optional)
	gitManager      *GitManager
	activityTracker *ActivityTracker
}

func (n *ShadowNode) GetMountPoint() string {
	return n.mountPoint
}

func (n *ShadowNode) GetMountID() string {
	return n.mountID
}

// InitGitManager initializes Git functionality for auto-versioning
func (n *ShadowNode) InitGitManager(config GitConfig) error {
	n.gitManager = NewGitManager(n.mountPoint, n.srcDir, config)

	// Initialize Git repository
	if err := n.gitManager.InitializeRepo(); err != nil {
		return err
	}

	// Create activity tracker
	n.activityTracker = NewActivityTracker(n, config)

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

	if n.activityTracker != nil {
		Debug("HandleWriteActivity: Tracking activity for %s", mountPointPath)
		n.activityTracker.MarkActivity(mountPointPath)
	} else {
		Debug("HandleWriteActivity: Activity tracker is nil, cannot track %s", mountPointPath)
	}
}

// CleanupGitManager cleans up Git resources and commits all pending changes
func (n *ShadowNode) CleanupGitManager() {
	if n.gitManager != nil {
		// Stop accepting new commits
		n.gitManager.Stop()

		// CRITICAL: Commit all pending changes before stopping timers to prevent data loss
		if err := n.activityTracker.CommitAllPending(); err != nil {
			Debug("Failed to commit pending changes on cleanup: %v", err)
		}
		n.activityTracker.StopIdleMonitoring()
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
	}()

	if path == "" {
		// Fallback if both methods failed
		return n.RootData.Path
	}

	return filepath.Join(n.RootData.Path, path)
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

// func (n *ShadowNode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
// 	fmt.Printf("Statfs:%s\n", n.FullPath(false))
// 	s := syscall.Statfs_t{}
// 	err := syscall.Statfs(n.FullPath(false), &s)
// 	if err != nil {
// 		return fs.ToErrno(err)
// 	}
// 	out.FromStatfsT(&s)
// 	return fs.OK
// }

func (n *ShadowNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate name parameter to prevent path traversal
	if !utils.ValidateName(name) {
		return nil, syscall.EPERM
	}

	p := filepath.Join(n.FullPath(false), name)

	// Cache independence: We check cache first, then fall back to source.
	// Note: Source directory changes during mount are not detected by design
	// to maintain cache independence and prevent data loss. Users should
	// unmount before modifying the source directory.
	mirrorPath := n.RebasePathUsingCache(p)

	var st syscall.Stat_t

	if mirrorPath != n.cachePath {
		// Optimize: check file existence first, then xattr only if file exists or might be deleted
		// This reduces syscalls when file doesn't exist
		var st2 *syscall.Stat_t
		var errno syscall.Errno

		if n.helpers != nil {
			st2, errno = n.helpers.StatFile(mirrorPath, false)
		} else {
			// Fallback: check file existence
			st2 = &syscall.Stat_t{}
			err := syscall.Lstat(mirrorPath, st2)
			if err != nil {
				errno = fs.ToErrno(err)
			}
		}

		if errno == 0 {
			// File exists - check if it's deleted
			var deleted bool
			if n.helpers != nil {
				deleted, errno = n.helpers.CheckFileDeleted(mirrorPath)
			} else {
				attr := xattr.XAttr{}
				exists, errno := xattr.Get(mirrorPath, &attr)
				if errno == 0 {
					deleted = exists && xattr.IsPathDeleted(attr)
				}
			}

			if errno != 0 {
				return nil, errno
			}

			if deleted {
				return nil, syscall.ENOENT
			}

			// File exists and is not deleted
			st = *st2
		} else {
			// File doesn't exist - check xattr to see if it's marked as deleted
			var deleted bool
			if n.helpers != nil {
				deleted, errno = n.helpers.CheckFileDeleted(mirrorPath)
			} else {
				attr := xattr.XAttr{}
				exists, errno := xattr.Get(mirrorPath, &attr)
				if errno == 0 {
					deleted = exists && xattr.IsPathDeleted(attr)
				}
			}

			if errno != 0 {
				return nil, errno
			}

			if deleted {
				return nil, syscall.ENOENT
			}

			// File doesn't exist and is not deleted - check source
			mirrorPath = ""
		}
	} else {
		mirrorPath = ""
	}

	if mirrorPath == "" {
		// check if file exist in srcDir
		var st2 *syscall.Stat_t
		var errno syscall.Errno

		if n.helpers != nil {
			st2, errno = n.helpers.StatFile(p, false)
		} else {
			// Fallback to original method
			st2 = &syscall.Stat_t{}
			err := syscall.Lstat(p, st2)
			if err != nil {
				errno = fs.ToErrno(err)
			}
		}

		if errno != 0 {
			return nil, errno
		}
		st = *st2
	}

	// Check if it's a directory and fix permissions if needed BEFORE FromStat
	// CRITICAL: Always check and fix permissions for directories, especially .git
	// This ensures git can always traverse directories it creates
	if st.Mode&syscall.S_IFDIR != 0 {
		// It's a directory - ALWAYS ensure execute permissions are set
		// Don't just check - always fix to prevent any possibility of wrong permissions
		// This is critical for FUSE filesystems where kernel might cache wrong attributes
		pathToFix := mirrorPath
		if pathToFix == "" {
			pathToFix = p
		}
		// Fix permissions immediately - don't wait
		if err := utils.EnsureDirPermissions(pathToFix); err != nil {

			// Continue - we'll fix st.Mode below
		} else {
			// Re-stat after fixing to get correct permissions
			if mirrorPath != "" {
				if err2 := syscall.Lstat(mirrorPath, &st); err2 == nil {
					// st now has correct permissions
				}
			} else {
				if err2 := syscall.Lstat(p, &st); err2 == nil {
					// st now has correct permissions
				}
			}
		}

		// Ensure st.Mode has correct execute bits BEFORE FromStat
		permBits := st.Mode & 0777
		if permBits&0111 == 0 {
			// Fix execute bits in stat structure
			st.Mode = (st.Mode &^ 0777) | (permBits | 0755)
		}
	}

	// create new inode
	out.Attr.FromStat(&st)

	rootData := n.RootData
	inode := newShadowNode(rootData, n.EmbeddedInode(), name, &st)

	if shadowNode, ok := inode.(*ShadowNode); ok {
		shadowNode.mirrorPath = mirrorPath
	}

	ch := n.NewInode(ctx, inode, n.idFromStat(&st))

	return ch, 0
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
	// get full path using mirror path if exists
	p := n.FullPath(true)

	st := syscall.Stat_t{}

	// CRITICAL: Always check if file should be read from cache first
	// This handles cases where file was renamed after mirrorPath was set
	cachedPath := n.RebasePathUsingCache(p)
	if err := syscall.Lstat(cachedPath, &st); err == nil {
		// File exists in cache - use cached file's attributes

		p = cachedPath
	} else {
		// File not in cache, use original path
		if &n.Inode == n.Root() {
			err = syscall.Stat(p, &st)
		} else {
			err = syscall.Lstat(p, &st)
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
		if err := utils.EnsureDirPermissions(p); err != nil {

			// Continue - we'll fix st.Mode below
		} else {
			// Re-stat after fixing to get FRESH attributes from filesystem
			// This ensures we're not using stale cached attributes
			if &n.Inode == n.Root() {
				if err2 := syscall.Stat(p, &st); err2 == nil {
				}
			} else {
				if err2 := syscall.Lstat(p, &st); err2 == nil {
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

		if n.mirrorPath != "" && strings.HasPrefix(p, n.cachePath) {
			// Force a fresh stat to get updated file size after writes
			if err2 := syscall.Lstat(p, &st); err2 == nil {
				// st now has fresh attributes including correct size

			}
		} else {
			// Check if file should be in cache even if p doesn't start with cachePath
			cachedPath := n.RebasePathUsingCache(p)

			if err2 := syscall.Lstat(cachedPath, &st); err2 == nil {

				// Use the cached file's attributes
				p = cachedPath
			}
		}
	}

	out.FromStat(&st)

	// CRITICAL: For files in cache, set very short cache timeout
	// This forces the kernel to requery attributes frequently, preventing stale size issues
	if n.mirrorPath != "" && strings.HasPrefix(p, n.cachePath) && st.Mode&syscall.S_IFREG != 0 {
		out.SetTimeout(100 * time.Millisecond) // 100ms timeout for cache files

	}

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

// preserveOwner sets uid and gid of `path` according to the caller information
// in `ctx`.
func (n *ShadowNode) preserveOwner(ctx context.Context, path string) error {
	return utils.PreserveOwner(ctx, path)
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

	// CRITICAL: Use source path first to ensure consistent path resolution
	// This prevents race conditions where parent directory state is inconsistent
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
		syscall.Removexattr(p, xattr.Name)
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

	// CRITICAL: Ensure parent directory has execute permissions for Git operations
	// This is especially important for .git directory when git creates files inside it
	// But only fix if we can access it (not for security tests)
	if err := utils.EnsureDirPermissions(cachedDir); err != nil {

		// Don't fail - try to continue, but this might cause issues
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

	// CRITICAL: Debug Git object creation issue
	if ch != nil {

	}

	return ch, lf, 0, 0
}

func (n *ShadowNode) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	p := n.FullPath(true)

	forceCache := flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_TRUNC|syscall.O_APPEND|syscall.O_CREAT) != 0
	hasAppend := flags&syscall.O_APPEND != 0
	// Preserve O_APPEND flag - kernel handles append semantics correctly
	// Only strip O_TRUNC to prevent truncation when we want to preserve content
	flags = flags &^ syscall.O_TRUNC
	Debug("Open: forceCache=%v, hasAppend=%v", forceCache, hasAppend)

	perms := uint32(0)

	// CRITICAL: Always verify that mirrorPath matches expected filename
	// This handles cases where mirrorPath is stale after rename operations
	if n.mirrorPath != "" {
		// Get the expected filename from the inode path
		expectedFilename := filepath.Base(n.EmbeddedInode().Path(nil))
		currentFilename := filepath.Base(n.mirrorPath)

		if expectedFilename != currentFilename {
			// Filenames don't match - mirrorPath is stale
			Debug("Open: mirrorPath filename mismatch, expected %s, got %s", expectedFilename, currentFilename)
			n.mirrorPath = ""
			p = n.FullPath(true) // Recompute path
		}
	}

	// check if path is not cached
	if !strings.HasPrefix(p, n.cachePath) {
		// check if file exists in cache
		cachedPath := n.RebasePathUsingCache(p)
		st := syscall.Stat_t{}
		err := syscall.Lstat(cachedPath, &st)
		if err == nil {
			// File exists in cache - use it
			// But if opening with O_APPEND and cache file is empty, copy source content first
			if hasAppend && st.Size == 0 {
				var sourceSt syscall.Stat_t
				if err := syscall.Lstat(p, &sourceSt); err == nil && sourceSt.Size > 0 {
					// Source file exists and has content - copy it to cache before opening for append
					if n.helpers != nil {
						if errno := n.helpers.CopyFile(p, cachedPath); errno != 0 {
							return nil, 0, errno
						}
					} else {
						if errno := n.copyFileSimple(p, cachedPath, uint32(sourceSt.Mode)); errno != 0 {
							return nil, 0, errno
						}
					}
				}
			}
			p = cachedPath
			n.mirrorPath = cachedPath
		} else if forceCache {
			// Need to create file in cache for write operations
			// create directory if not exists recursively using same permissions
			_, err = cache.CreateMirroredDir(filepath.Dir(p), n.cachePath, n.srcDir)
			if err != nil {
				return nil, 0, fs.ToErrno(err)
			}
			// get permissions from source file if it exists (only needed for write operations)
			err = syscall.Lstat(p, &st)
			if err == nil {
				perms = uint32(st.Mode)
			} else {
				perms = 0644 // default file permissions
			}

			// Copy-on-write for append operations: if opening with O_APPEND and source has content,
			// copy source content to cache before opening to preserve existing content
			if hasAppend {
				var sourceSt syscall.Stat_t
				if err := syscall.Lstat(p, &sourceSt); err == nil && sourceSt.Size > 0 {
					// Source file exists and has content - copy it to cache before opening for append
					if n.helpers != nil {
						if errno := n.helpers.CopyFile(p, cachedPath); errno != 0 {
							return nil, 0, errno
						}
					} else {
						if errno := n.copyFileSimple(p, cachedPath, uint32(sourceSt.Mode)); errno != 0 {
							return nil, 0, errno
						}
					}
					// File already exists in cache now, don't need O_CREAT
					p = cachedPath
					n.mirrorPath = cachedPath
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
		} else {
			// Read-only: use source file directly (no copy needed, no permission check needed)
			// p already points to source path
		}
	} else {
		// check if file exists in cache
		st := syscall.Stat_t{}
		err := syscall.Lstat(p, &st)
		if err == nil {
			// File exists in cache
			// But if opening with O_APPEND and cache file is empty, copy source content first
			if hasAppend && st.Size == 0 {
				sourcePath := n.RebasePathUsingSrc(p)
				var sourceSt syscall.Stat_t
				if err := syscall.Lstat(sourcePath, &sourceSt); err == nil && sourceSt.Size > 0 {
					// Source file exists and has content - copy it to cache before opening for append
					if n.helpers != nil {
						if errno := n.helpers.CopyFile(sourcePath, p); errno != 0 {
							return nil, 0, errno
						}
					} else {
						if errno := n.copyFileSimple(sourcePath, p, uint32(sourceSt.Mode)); errno != 0 {
							return nil, 0, errno
						}
					}
				}
			}
		} else {
			// CRITICAL: File doesn't exist at current mirrorPath
			// This can happen after rename operations where mirrorPath is stale
			// Try to find the file in cache using relative path
			relativePath := strings.TrimPrefix(n.EmbeddedInode().Path(nil), "/")
			expectedCachePath := filepath.Join(n.cachePath, relativePath)
			Debug("Open: file not found at %s, trying %s", p, expectedCachePath)

			if err2 := syscall.Lstat(expectedCachePath, &st); err2 == nil {
				// Found the file in cache at expected location
				p = expectedCachePath
				n.mirrorPath = expectedCachePath
				Debug("Open: found file in cache at %s", expectedCachePath)
			} else {
				// File not found anywhere
				n.mirrorPath = ""
				p = n.FullPath(false)
			}
		}
	}

	f, err := syscall.Open(p, int(flags), perms)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}

	lf := fs.NewLoopbackFile(f)

	// Note: Activity tracking happens in Write() method after actual writes occur
	// Not tracking here to avoid premature tracking before copy-on-write completes

	return lf, 0, 0
}

func (n *ShadowNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	// Validate name parameters to prevent path traversal
	if !utils.ValidateName(name) || !utils.ValidateName(newName) {
		return syscall.EPERM
	}

	p2 := filepath.Join(n.RootData.Path, newParent.EmbeddedInode().Path(nil), newName)

	// CRITICAL SECURITY FIX: Check if destination is an existing directory
	// If renaming a file to match an existing directory name, move file INTO directory
	if newParentNode, ok := newParent.(*ShadowNode); ok {
		if destChild := newParentNode.GetChild(newName); destChild != nil {
			if destOps := destChild.EmbeddedInode().Operations(); destOps != nil {
				if destNode, ok := destOps.(*ShadowNode); ok {
					// Check if destination is a directory by looking at its mirrorPath
					if destNode.mirrorPath != "" {
						if stat, err := os.Stat(destNode.mirrorPath); err == nil && stat.IsDir() {
							// Destination is a directory, move file inside it
							p2 = filepath.Join(p2, name)
						}
					}
				}
			}
		}
	}

	// if p2 is in srcDir then change it to cache
	if strings.HasPrefix(p2, n.srcDir) {
		p2 = n.RebasePathUsingCache(p2)
	}

	p1 := filepath.Join(n.FullPath(false), name)
	p1Cache := n.RebasePathUsingCache(p1)

	// check if file is in cache
	p1attrXattr := xattr.XAttr{}
	isp1InCache, errno := xattr.Get(p1Cache, &p1attrXattr)
	if errno != 0 {
		return errno
	}

	if xattr.IsPathDeleted(p1attrXattr) {
		return syscall.ENOENT
	}

	p1attr := syscall.Stat_t{}

	if isp1InCache {
		err := syscall.Lstat(p1Cache, &p1attr)
		if err != nil {
			return fs.ToErrno(err)
		}
		if flags&fs.RENAME_EXCHANGE != 0 {
			if errno := n.renameExchange(p1Cache, newParent, p2); errno != 0 {
				return errno
			}
		} else {
			// move file from p1 to p2 using syscall
			err := syscall.Rename(p1Cache, p2)
			if err != nil {
				return fs.ToErrno(err)
			}
			// CRITICAL SECURITY FIX: Update the actual file node's mirrorPath, not parent directory
			// Find the child node being renamed and update its mirrorPath to the new location
			if child := n.GetChild(name); child != nil {
				if childOps := child.EmbeddedInode().Operations(); childOps != nil {
					if childNode, ok := childOps.(*ShadowNode); ok {
						childNode.mirrorPath = p2
					}
				}
			}
		}
	} else {
		// make a copy of the file at p2 destination
		// ensure destination directory exists
		_, err := cache.CreateMirroredDir(filepath.Dir(p2), n.cachePath, n.srcDir)
		if err != nil {
			return fs.ToErrno(err)
		}
		// open file for writing in p2
		err = syscall.Lstat(p1, &p1attr)
		if err != nil {
			return fs.ToErrno(err)
		}
		f2, err := syscall.Open(p2, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC, uint32(p1attr.Mode))
		if err != nil {
			return fs.ToErrno(err)
		}
		defer syscall.Close(f2)
		// open file for reading in p1
		f1, err := syscall.Open(p1, syscall.O_RDONLY, 0)
		if err != nil {
			return fs.ToErrno(err)
		}
		defer syscall.Close(f1)

		// copy file from p1 to p2 using buffer pool with partial write handling
		bufPtr, ok := cache.GetBufferPool().Get().(*[]byte)
		if !ok {
			return syscall.ENOMEM
		}
		defer cache.GetBufferPool().Put(bufPtr)
		buf := *bufPtr

		for {
			// Read data from the source file
			n, err := syscall.Read(f1, buf)
			if err != nil && err != syscall.EINTR && err != syscall.EAGAIN {
				return fs.ToErrno(err)
			}
			if n == 0 {
				break
			}

			// Write data to the destination file (handle partial writes)
			offset := 0
			for offset < n {
				written, err := syscall.Write(f2, buf[offset:n])
				if err != nil {
					return fs.ToErrno(err)
				}
				offset += written
			}
		}

		// Copy ownership and timestamps to destination
		if err := syscall.Chown(p2, int(p1attr.Uid), int(p1attr.Gid)); err != nil {
			return fs.ToErrno(err)
		}

		var times [2]syscall.Timespec
		times[0] = syscall.Timespec{Sec: p1attr.Atim.Sec, Nsec: p1attr.Atim.Nsec}
		times[1] = syscall.Timespec{Sec: p1attr.Mtim.Sec, Nsec: p1attr.Mtim.Nsec}
		if err := syscall.UtimesNano(p2, times[:]); err != nil {
			return fs.ToErrno(err)
		}
	}

	// mark p1 as deleted
	p1attrXattr.PathStatus = xattr.PathStatusDeleted
	_, err := cache.CreateMirroredDir(filepath.Dir(p1Cache), n.cachePath, n.srcDir)
	if err != nil {
		return fs.ToErrno(err)
	}

	// create empty file using syscall.Open (handles existing files gracefully)
	fd, err := syscall.Open(p1Cache, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_TRUNC, uint32(p1attr.Mode))
	if err != nil {
		return fs.ToErrno(err)
	}
	syscall.Close(fd)

	// Copy ownership and timestamps to shadow deletion marker
	if err := syscall.Chown(p1Cache, int(p1attr.Uid), int(p1attr.Gid)); err != nil {
		return fs.ToErrno(err)
	}

	var times [2]syscall.Timespec
	times[0] = syscall.Timespec{Sec: p1attr.Atim.Sec, Nsec: p1attr.Atim.Nsec}
	times[1] = syscall.Timespec{Sec: p1attr.Mtim.Sec, Nsec: p1attr.Mtim.Nsec}
	if err := syscall.UtimesNano(p1Cache, times[:]); err != nil {
		return fs.ToErrno(err)
	}

	// mark it as deleted
	return xattr.Set(p1Cache, &p1attrXattr)
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

func (n *ShadowNode) createMirroredDir(path string) (string, error) {
	return cache.CreateMirroredDir(path, n.cachePath, n.srcDir)
}

func (n *ShadowNode) CreateMirroredFileOrDir(path string) (string, error) {
	return cache.CreateMirroredFileOrDir(path, n.cachePath, n.srcDir)
}

// copyFileSimple copies a file from source to destination (fallback when helpers not available)
func (n *ShadowNode) copyFileSimple(srcPath, destPath string, mode uint32) syscall.Errno {
	return cache.CopyFileSimple(srcPath, destPath, mode)
}

func (n *ShadowNode) Unlink(ctx context.Context, name string) syscall.Errno {
	// Validate name parameter to prevent path traversal
	if !utils.ValidateName(name) {
		return syscall.EPERM
	}

	p := filepath.Join(n.FullPath(false), name)
	Debug("Unlink: %s", p)

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
		// File exists in cache - unlink it directly
		return fs.ToErrno(syscall.Unlink(mirrorPath))
	}

	// check if file is in srcDir
	var srcSt *syscall.Stat_t
	if n.helpers != nil {
		srcSt, errno = n.helpers.StatFile(p, false)
	} else {
		// Fallback to original method
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

	// if file is in srcDir create a new one in cache
	deletedPath, err := n.CreateMirroredFileOrDir(p)
	if err != nil {
		// Handle race condition: if file already exists, verify it's valid
		if errno := fs.ToErrno(err); errno == syscall.EEXIST {
			// Another process may have created it - verify it exists
			if n.helpers != nil {
				_, checkErrno := n.helpers.StatFile(deletedPath, false)
				if checkErrno != 0 {
					return checkErrno
				}
			} else {
				st := syscall.Stat_t{}
				if syscall.Lstat(deletedPath, &st) != nil {
					return fs.ToErrno(err)
				}
			}
			// File exists, continue to set xattr
		} else {
			return errno
		}
	}

	// set new xattr to indicate file is deleted
	attr := xattr.XAttr{PathStatus: xattr.PathStatusDeleted}
	errno = xattr.Set(deletedPath, &attr)
	if errno != 0 {
		// Cleanup: if xattr setting failed, remove the shadow file we created
		// (best effort - ignore cleanup errors)
		syscall.Unlink(deletedPath)
		return errno
	}

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
	Debug("Symlink:%s -> %s", p, target)

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
	Debug("Link:%s -> %s", p, target.EmbeddedInode().Path(nil))

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
			// CRITICAL: Inherit git manager and activity tracker from parent
			// This ensures all child nodes can track write activity for auto-commits
			n.gitManager = parentNode.gitManager
			n.activityTracker = parentNode.activityTracker
			if n.gitManager != nil {
				Debug("newShadowNode: Inherited git manager from parent")
			}
			if n.activityTracker != nil {
				Debug("newShadowNode: Inherited activity tracker from parent")
			}
		}
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

	return root, nil
}
