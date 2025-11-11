package fs

import (
	"context"
	"crypto/sha256"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

const ShadowXattrName = "user.shadow-fs"

const HomeName = ".shadowfs"

const RootName = ".root"

type ShadowPathStatus int

const (
	ShadowPathStatusNone    ShadowPathStatus = iota
	ShadowPathStatusDeleted                  // file is deleted in cache
)

type ShadowXAttr struct {
	ShadowPathStatus ShadowPathStatus
}

const ShadowXAttrSZ = int(unsafe.Sizeof(ShadowXAttr{}))

func ShadowXAttrToBytes(attr *ShadowXAttr) []byte {
	return (*(*[ShadowXAttrSZ]byte)(unsafe.Pointer(attr)))[:]
}

func ShadowXAttrFromBytes(data []byte) *ShadowXAttr {
	if len(data) < ShadowXAttrSZ {
		// Return zero-initialized struct if data is too short
		return &ShadowXAttr{}
	}
	return (*ShadowXAttr)(unsafe.Pointer(&data[0]))
}

func GetShadowXAttr(path string, attr *ShadowXAttr) (exists bool, errno syscall.Errno) {
	_, err := syscall.Getxattr(path, ShadowXattrName, ShadowXAttrToBytes(attr))

	// skip if xattr not found
	if os.IsNotExist(err) {
		return false, 0
	}

	if err != nil && err != syscall.ENODATA {
		return false, fs.ToErrno(err)
	}

	return true, 0
}

func SetShadowXAttr(path string, attr *ShadowXAttr) syscall.Errno {
	return fs.ToErrno(syscall.Setxattr(path, ShadowXattrName, ShadowXAttrToBytes(attr), 0))
}

func IsPathDeleted(attr ShadowXAttr) bool {
	return attr.ShadowPathStatus&ShadowPathStatusDeleted != 0
}

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
		return
	}

	if n.activityTracker != nil {
		n.activityTracker.MarkActivity(filePath)
	}
}

// CleanupGitManager cleans up Git resources and commits all pending changes
func (n *ShadowNode) CleanupGitManager() {
	if n.gitManager != nil {
		// Stop accepting new commits
		n.gitManager.Stop()
	}
	if n.activityTracker != nil {
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
	if !validateNameParameter(name) {
		return nil, syscall.EPERM
	}

	p := filepath.Join(n.FullPath(false), name)
	Debug("Lookup: %s", p)

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
				xattr := ShadowXAttr{}
				exists, errno := GetShadowXAttr(mirrorPath, &xattr)
				if errno == 0 {
					deleted = exists && IsPathDeleted(xattr)
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
				xattr := ShadowXAttr{}
				exists, errno := GetShadowXAttr(mirrorPath, &xattr)
				if errno == 0 {
					deleted = exists && IsPathDeleted(xattr)
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
	if f != nil {
		if fga, ok := f.(fs.FileGetattrer); ok {
			return fga.Getattr(ctx, out)
		}
		// If file handle doesn't implement FileGetattrer, fall through to path-based stat
	}

	// Cache independence: Attributes are read from cache if available, otherwise from source.
	// Source directory changes during mount are not detected to maintain cache independence.
	// get full path using mirror path if exists
	p := n.FullPath(true)

	var err error
	st := syscall.Stat_t{}

	if &n.Inode == n.Root() {
		err = syscall.Stat(p, &st)
	} else {
		err = syscall.Lstat(p, &st)
	}

	if err != nil {
		return fs.ToErrno(err)
	}

	out.FromStat(&st)

	// Ensure directories have execute permissions
	if out.Mode&0170000 == 0040000 { // S_IFDIR
		// Special fix for .git directories to ensure they always have correct permissions
		if strings.Contains(p, ".git") && (out.Mode&0111 == 0) {
			out.Mode = 0040755 // Force correct directory permissions for .git
		} else if out.Mode&0111 == 0 { // No execute bits
			out.Mode |= 0755 // Add standard directory permissions
		}
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

// validateNameParameter validates FUSE name parameter to prevent path traversal
// Fast check - only validates the name component, not the full path
func validateNameParameter(name string) bool {
	// Reject names containing path traversal sequences
	if strings.Contains(name, "..") {
		return false
	}
	// Reject absolute paths (shouldn't happen from FUSE, but be safe)
	if filepath.IsAbs(name) {
		return false
	}
	// Reject empty names
	if name == "" {
		return false
	}
	return true
}

// validatePathWithinRoot validates that a path stays within the specified root directory
// Returns the cleaned path and an error if the path escapes the root boundary
// Only use this for paths that may have been constructed from user input
func validatePathWithinRoot(path, root string) (string, error) {
	cleaned := filepath.Clean(path)

	// Convert to absolute paths for comparison
	var absPath string
	var err error
	if filepath.IsAbs(cleaned) {
		absPath = cleaned
	} else {
		absPath, err = filepath.Abs(cleaned)
		if err != nil {
			return "", err
		}
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}

	// Ensure the path is within the root directory
	// Check if absPath is exactly absRoot or is a subdirectory of absRoot
	if absPath != absRoot && !strings.HasPrefix(absPath+string(os.PathSeparator), absRoot+string(os.PathSeparator)) {
		return "", syscall.EPERM
	}

	return cleaned, nil
}

// preserveOwner sets uid and gid of `path` according to the caller information
// in `ctx`.
func (n *ShadowNode) preserveOwner(ctx context.Context, path string) error {
	if os.Getuid() != 0 {
		return nil
	}
	caller, ok := fuse.FromContext(ctx)
	if !ok {
		return nil
	}
	return syscall.Lchown(path, int(caller.Uid), int(caller.Gid))
}

func (n *ShadowNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (inode *fs.Inode, fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	// Validate name parameter to prevent path traversal
	if !validateNameParameter(name) {
		return nil, nil, 0, syscall.EPERM
	}

	p := filepath.Join(n.FullPath(true), name)

	// check if file is from cache
	isPathInCache := false
	if strings.HasPrefix(p, n.cachePath) {
		// check if file is deleted in cache
		xattr := ShadowXAttr{}
		var errno syscall.Errno
		isPathInCache, errno = GetShadowXAttr(p, &xattr)
		if errno != 0 {
			return nil, nil, 0, errno
		}

		if isPathInCache && IsPathDeleted(xattr) {
			// File was deleted - remove the deletion marker and shadow file
			// Create new empty file (don't restore from source - cache is independent)
			err := syscall.Unlink(p)
			if err != nil && !os.IsNotExist(err) {
				return nil, nil, 0, fs.ToErrno(err)
			}
			// Remove the xattr deletion marker
			syscall.Removexattr(p, ShadowXattrName)
			// Create new empty file - don't restore from source
			isPathInCache = false
		}
	}

	if !isPathInCache {
		// need to create file in cache
		// create directory if not exists recursively using same permissions
		cachedDir, err := n.createMirroredDir(filepath.Dir(p))
		if err != nil && cachedDir != n.cachePath {
			return nil, nil, 0, fs.ToErrno(err)
		}
		cacheFilePath := filepath.Join(cachedDir, filepath.Base(p))

		// Create empty file in cache - copy-on-write will happen on first write
		// Get source path to check permissions
		sourcePath := filepath.Join(n.FullPath(false), name)
		var sourceSt syscall.Stat_t
		fileMode := mode
		if err := syscall.Lstat(sourcePath, &sourceSt); err == nil {
			// Source file exists - use its permissions
			fileMode = uint32(sourceSt.Mode)
		}
		// Create empty file - content will be copied on first write
		p = cacheFilePath
		mode = fileMode // Use source permissions if available
	}

	flags = flags &^ syscall.O_APPEND
	fd, err := syscall.Open(p, int(flags)|os.O_CREATE, mode)
	if err != nil {
		return nil, nil, 0, fs.ToErrno(err)
	}

	n.preserveOwner(ctx, p)

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

	return ch, lf, 0, 0
}

func (n *ShadowNode) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	p := n.FullPath(true)

	forceCache := flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_TRUNC|syscall.O_APPEND|syscall.O_CREAT) != 0
	flags = flags &^ (syscall.O_APPEND | syscall.O_TRUNC)
	Debug("Open: forceCache=%v", forceCache)

	perms := uint32(0)

	// check if path is not cached
	if !strings.HasPrefix(p, n.cachePath) {
		// check if file exists in cache
		cachedPath := n.RebasePathUsingCache(p)
		st := syscall.Stat_t{}
		err := syscall.Lstat(cachedPath, &st)
		if err == nil {
			// File exists in cache - use it
			p = cachedPath
			n.mirrorPath = cachedPath
		} else if forceCache {
			// Need to create file in cache for write operations
			// create directory if not exists recursively using same permissions
			_, err = n.createMirroredDir(filepath.Dir(p))
			if err != nil {
				return nil, 0, fs.ToErrno(err)
			}
			// Create empty file in cache - copy-on-write will happen on first write
			// get permissions from source file if it exists (only needed for write operations)
			err = syscall.Lstat(p, &st)
			if err == nil {
				perms = uint32(st.Mode)
			} else {
				perms = 0644 // default file permissions
			}
			// set flags to create file
			flags = flags | syscall.O_CREAT
			p = cachedPath
			n.mirrorPath = cachedPath
		} else {
			// Read-only: use source file directly (no copy needed, no permission check needed)
			// p already points to source path
		}
	} else {
		// check if file exists in cache
		st := syscall.Stat_t{}
		err := syscall.Lstat(p, &st)
		if err != nil {
			n.mirrorPath = ""
			p = n.FullPath(false)
		}
	}

	f, err := syscall.Open(p, int(flags), perms)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}

	lf := fs.NewLoopbackFile(f)

	// Track write activity for Git auto-versioning
	if forceCache {
		n.HandleWriteActivity(p)
	}

	return lf, 0, 0
}

func (n *ShadowNode) Opendir(ctx context.Context) syscall.Errno {
	fd, err := syscall.Open(n.FullPath(true), syscall.O_DIRECTORY, 0755)
	if err != nil {
		return fs.ToErrno(err)
	}
	n.preserveOwner(ctx, n.FullPath(true))
	syscall.Close(fd)
	return fs.OK
}

func (n *ShadowNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	return NewShadowDirStream(n, "")
}

func (n *ShadowNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate name parameter to prevent path traversal
	if !validateNameParameter(name) {
		return nil, syscall.EPERM
	}

	Debug("Mkdir: %s, mode=%o", name, mode)
	p := filepath.Join(n.FullPath(false), name)
	Debug("Mkdir:%s", p)

	// check if directory exists in src
	st := syscall.Stat_t{}
	err := syscall.Lstat(p, &st)
	if err == nil {
		// directory exists in src use the same mode
		mode = uint32(st.Mode)
	}

	// change path to cache
	p = n.RebasePathUsingCache(p)

	// Ensure directories have execute permissions
	dirMode := os.FileMode(mode)
	if dirMode&0111 == 0 {
		dirMode |= 0755 // Add standard directory permissions if no execute bits
	}
	Debug("Mkdir (cache path):%s, mode:%o", p, dirMode)
	err = os.Mkdir(p, dirMode)
	if err != nil {
		// Handle case where directory already exists (idempotent operation)
		if os.IsExist(err) {
			// Directory exists, verify it's actually a directory
			var st2 syscall.Stat_t
			if err2 := syscall.Lstat(p, &st2); err2 == nil {
				if st2.Mode&syscall.S_IFDIR != 0 {
					// It's a directory, treat as success
					err = nil
				} else {
					// Path exists but is not a directory
					return nil, fs.ToErrno(err)
				}
			} else {
				return nil, fs.ToErrno(err)
			}
		} else {
			return nil, fs.ToErrno(err)
		}
	}

	n.preserveOwner(ctx, p)

	st = syscall.Stat_t{}
	if err := syscall.Lstat(p, &st); err != nil {
		syscall.Rmdir(p)
		return nil, fs.ToErrno(err)
	}

	out.Attr.FromStat(&st)

	node := newShadowNode(n.RootData, n.EmbeddedInode(), name, &st)
	node.(*ShadowNode).mirrorPath = p

	ch := n.NewInode(ctx, node, n.idFromStat(&st))

	// Don't invalidate cache immediately after creation - this can cause issues
	// n.NotifyEntry(name)

	return ch, 0
}

func (n *ShadowNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	// Validate name parameter to prevent path traversal
	if !validateNameParameter(name) {
		return syscall.EPERM
	}

	name = filepath.Join(n.FullPath(false), name)

	ds, err := NewShadowDirStream(n, name)
	if err != 0 {
		return err
	}
	defer ds.Close()

	cnt := 0
	for ds.HasNext() && cnt == 0 {
		de, err := ds.Next()
		if err != 0 {
			return err
		}
		if de.Name == "." || de.Name == ".." {
			continue
		}
		cnt++
	}

	if cnt != 0 {
		return syscall.ENOTEMPTY
	}

	cacheName := n.RebasePathUsingCache(name)

	// set new xattr to indicate file is deleted
	xattr := ShadowXAttr{}
	_, errno := GetShadowXAttr(cacheName, &xattr)
	if errno != 0 {
		return errno
	}
	xattr.ShadowPathStatus |= ShadowPathStatusDeleted
	return SetShadowXAttr(cacheName, &xattr)
}

func (n *ShadowNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	// Validate name parameters to prevent path traversal
	if !validateNameParameter(name) || !validateNameParameter(newName) {
		return syscall.EPERM
	}

	p2 := filepath.Join(n.RootData.Path, newParent.EmbeddedInode().Path(nil), newName)

	// if p2 is in srcDir then change it to cache
	if strings.HasPrefix(p2, n.srcDir) {
		p2 = n.RebasePathUsingCache(p2)
	}

	p1 := filepath.Join(n.FullPath(false), name)
	p1Cache := n.RebasePathUsingCache(p1)

	// check if file is in cache
	p1xattr := ShadowXAttr{}
	isp1InCache, errno := GetShadowXAttr(p1Cache, &p1xattr)
	if errno != 0 {
		return errno
	}

	if IsPathDeleted(p1xattr) {
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
			// Update the mirrorPath to the new location
			n.mirrorPath = p2
		}
	} else {
		// make a copy of the file at p2 destination
		// ensure destination directory exists
		_, err := n.createMirroredDir(filepath.Dir(p2))
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
		bufPtr, ok := bufferPool.Get().(*[]byte)
		if !ok {
			return syscall.ENOMEM
		}
		defer bufferPool.Put(bufPtr)
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
	p1xattr.ShadowPathStatus = ShadowPathStatusDeleted
	_, err := n.createMirroredDir(filepath.Dir(p1Cache))
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
	return SetShadowXAttr(p1Cache, &p1xattr)
}

func (n *ShadowNode) RebasePathUsingCache(path string) string {
	if strings.HasPrefix(path, n.cachePath) {
		return path
	}
	return filepath.Join(n.cachePath, strings.TrimPrefix(path, n.srcDir))
}

func (n *ShadowNode) RebasePathUsingMountPoint(path string) string {
	if strings.HasPrefix(path, n.mountPoint) {
		return path
	}
	return filepath.Join(n.mountPoint, strings.TrimPrefix(path, n.cachePath))
}

func (n *ShadowNode) RebasePathUsingSrc(path string) string {
	if strings.HasPrefix(path, n.srcDir) {
		return path
	}
	return filepath.Join(n.srcDir, strings.TrimPrefix(path, n.cachePath))
}

func (n *ShadowNode) createMirroredDir(path string) (string, error) {
	// Always use original implementation for Create method to avoid initialization issues
	path = n.RebasePathUsingSrc(path)

	srcDir := n.srcDir
	dstDir := n.cachePath

	path = strings.TrimPrefix(path, srcDir)
	paths := strings.Split(path, string(os.PathSeparator))

	last := len(paths) - 1

	if last >= 0 && paths[0] == "" {
		paths = paths[1:]
		last--
	}

	// Optimization: use os.MkdirAll for bulk creation, then fix permissions
	fullCachePath := dstDir
	for _, dir := range paths {
		// Validate each directory component doesn't contain path traversal
		if dir == ".." || dir == "." {
			return fullCachePath, syscall.EPERM
		}
		fullCachePath = filepath.Join(fullCachePath, dir)
	}

	// Create all directories at once with default permissions
	err := os.MkdirAll(fullCachePath, 0755)
	if err != nil {
		return fullCachePath, err
	}

	// Now fix permissions for each directory level
	// for _, dir := range paths {
	// 	dstDir = filepath.Join(dstDir, dir)

	// 	// Always ensure directories have correct permissions (0755)
	// 	// This prevents kernel attribute caching issues
	// 	err = syscall.Chmod(dstDir, 0755)
	// 	if err != nil {
	// 		return dstDir, err
	// 	}

	// 	// Force kernel to refresh attributes after fixing permissions
	// 	// Use sync.FileInfo to ensure the change is visible to the kernel
	// 	if _, err := os.Stat(dstDir); err != nil {
	// 		return dstDir, err
	// 	}

	// 	// Invalidate kernel attribute cache for this directory
	// 	// Use recover to handle potential nil pointer in test contexts
	// 	func() {
	// 		defer func() {
	// 			if r := recover(); r != nil {
	// 				// Ignore panic in test contexts where inode might not be initialized
	// 			}
	// 		}()
	// 		n.NotifyEntry(dir)
	// 	}()
	// }

	return fullCachePath, nil
}

func (n *ShadowNode) CreateMirroredFileOrDir(path string) (string, error) {
	// Always use original implementation for Create method to avoid initialization issues
	path = n.RebasePathUsingSrc(path)
	cachePath := n.RebasePathUsingCache(path)

	// get src dir mode using syscall
	var st syscall.Stat_t
	err := syscall.Lstat(path, &st)
	if err != nil {
		return cachePath, err
	}

	// check if file is a directory
	if st.Mode&syscall.S_IFDIR != 0 {
		// create directory in cache using same permissions
		return n.createMirroredDir(path)
	}

	// if file is a regular file create it in cache using same permissions
	if st.Mode&syscall.S_IFREG != 0 {
		// create directory if not exists recursively using same permissions
		newDir, err := n.createMirroredDir(filepath.Dir(path))
		if err != nil {
			return newDir, err
		}

		// create empty file using syscall.Open (handles existing files gracefully)
		fd, err := syscall.Open(cachePath, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_TRUNC, st.Mode)
		if err != nil {
			return cachePath, err
		}
		syscall.Close(fd)

		// Copy ownership
		if err := syscall.Chown(cachePath, int(st.Uid), int(st.Gid)); err != nil {
			return cachePath, err
		}

		// Copy timestamps
		var times [2]syscall.Timespec
		times[0] = syscall.Timespec{Sec: st.Atim.Sec, Nsec: st.Atim.Nsec}
		times[1] = syscall.Timespec{Sec: st.Mtim.Sec, Nsec: st.Mtim.Nsec}
		if err := syscall.UtimesNano(cachePath, times[:]); err != nil {
			return cachePath, err
		}

		return cachePath, nil
	}

	return cachePath, syscall.ENOTSUP
}

// copyFileSimple copies a file from source to destination (fallback when helpers not available)
func (n *ShadowNode) copyFileSimple(srcPath, destPath string, mode uint32) syscall.Errno {
	srcFd, err := syscall.Open(srcPath, syscall.O_RDONLY, 0)
	if err != nil {
		return fs.ToErrno(err)
	}
	defer syscall.Close(srcFd)

	destFd, err := syscall.Open(destPath, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_TRUNC, mode)
	if err != nil {
		return fs.ToErrno(err)
	}
	defer syscall.Close(destFd)

	// Get file size
	var stat syscall.Stat_t
	err = syscall.Fstat(srcFd, &stat)
	if err != nil {
		return fs.ToErrno(err)
	}

	// Copy file content using buffer pool
	bufPtr, ok := bufferPool.Get().(*[]byte)
	if !ok {
		return syscall.ENOMEM
	}
	defer bufferPool.Put(bufPtr)
	buf := *bufPtr

	for {
		n, err := syscall.Read(srcFd, buf)
		if err != nil && err != syscall.EINTR && err != syscall.EAGAIN {
			return fs.ToErrno(err)
		}
		if n == 0 {
			break
		}

		offset := 0
		for offset < n {
			written, err := syscall.Write(destFd, buf[offset:n])
			if err != nil {
				return fs.ToErrno(err)
			}
			offset += written
		}
	}

	// Copy metadata
	if err := syscall.Chown(destPath, int(stat.Uid), int(stat.Gid)); err != nil {
		return fs.ToErrno(err)
	}

	var times [2]syscall.Timespec
	times[0] = syscall.Timespec{Sec: stat.Atim.Sec, Nsec: stat.Atim.Nsec}
	times[1] = syscall.Timespec{Sec: stat.Mtim.Sec, Nsec: stat.Mtim.Nsec}
	if err := syscall.UtimesNano(destPath, times[:]); err != nil {
		return fs.ToErrno(err)
	}

	return 0
}

func (n *ShadowNode) Unlink(ctx context.Context, name string) syscall.Errno {
	// Validate name parameter to prevent path traversal
	if !validateNameParameter(name) {
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
		xattr := ShadowXAttr{}
		exists, errno := GetShadowXAttr(mirrorPath, &xattr)
		if errno == 0 {
			deleted = exists && IsPathDeleted(xattr)
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
	xattr := ShadowXAttr{ShadowPathStatus: ShadowPathStatusDeleted}
	errno = SetShadowXAttr(deletedPath, &xattr)
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
	if !validateNameParameter(name) {
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

	n.preserveOwner(ctx, p)

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
	if !validateNameParameter(name) {
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

	n.preserveOwner(ctx, p)

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

func (n *ShadowNode) CopyFileRange(ctx context.Context, fhIn fs.FileHandle, offIn uint64, out *fs.Inode, fhOut fs.FileHandle, offOut uint64, copyLen uint64, flags uint32) (uint32, syscall.Errno) {
	// For now, implement a basic copy using the file handles
	// This is a simplified implementation that should work for Git's needs

	// Read from input file handle
	reader, ok := fhIn.(fs.FileReader)
	if !ok {
		return 0, syscall.EBADF
	}
	writer, ok := fhOut.(fs.FileWriter)
	if !ok {
		return 0, syscall.EBADF
	}

	// Use buffer pool to reduce allocations
	bufPtr, ok := bufferPool.Get().(*[]byte)
	if !ok {
		return 0, syscall.ENOMEM
	}
	defer bufferPool.Put(bufPtr)
	buf := *bufPtr

	var totalCopied uint64

	for totalCopied < copyLen {
		// Read chunk from input
		result, status := reader.Read(ctx, buf, int64(offIn+totalCopied))
		if status != 0 {
			return uint32(totalCopied), status
		}
		bytesRead, _ := result.Bytes(buf)
		if len(bytesRead) == 0 {
			break
		}

		// Write chunk to output
		_, writeStatus := writer.Write(ctx, bytesRead, int64(offOut+totalCopied))
		if writeStatus != 0 {
			return uint32(totalCopied), writeStatus
		}

		totalCopied += uint64(len(bytesRead))
	}

	return uint32(totalCopied), 0
}

func (n *ShadowNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	p := n.FullPath(true)
	Debug("Write: %s, len=%d, off=%d, data=%q", p, len(data), off, string(data))

	// Copy-on-write: if file is in cache but empty, and source exists, copy source content first
	// Use Lstat() on path for copy-on-write check (path-based, no reflection overhead)
	if strings.HasPrefix(p, n.cachePath) && off == 0 {
		var cacheSt syscall.Stat_t
		if err := syscall.Lstat(p, &cacheSt); err == nil && cacheSt.Size == 0 {
			// Cache file is empty - check if source has content to copy
			sourcePath := n.RebasePathUsingSrc(p)
			var sourceSt syscall.Stat_t
			if err := syscall.Lstat(sourcePath, &sourceSt); err == nil && sourceSt.Size > 0 {
				// Source file exists and has content - copy it to cache before writing

				if n.helpers != nil {
					if errno := n.helpers.CopyFile(sourcePath, p); errno != 0 {
						return 0, errno
					}
				} else {
					if errno := n.copyFileSimple(sourcePath, p, sourceSt.Mode); errno != 0 {
						return 0, errno
					}
				}
			}
		}
	}

	written, errno := func() (uint32, syscall.Errno) {
		if fw, ok := f.(fs.FileWriter); ok {
			return fw.Write(ctx, data, off)
		}
		return 0, syscall.EBADF
	}()
	if errno == 0 {
		// Track write activity for Git auto-versioning
		n.HandleWriteActivity(p)
	}
	return written, errno
}

func (n *ShadowNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if fr, ok := f.(fs.FileReader); ok {
		return fr.Read(ctx, dest, off)
	}
	return nil, syscall.EBADF
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
		}
	}

	return n
}

func createDir(path string, perm iofs.FileMode) error {
	stat, err := os.Stat(path)

	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(path, perm); err != nil {
				return fmt.Errorf("creating directory error:\n%w", err)
			}
			return nil
		}
		return fmt.Errorf("stat error:\n%w", err)
	}

	if !stat.IsDir() {
		return fmt.Errorf("path must be a directory(%s)", path)
	}

	return nil
}

func writeFileOnce(path string, content []byte, perm iofs.FileMode) error {
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.WriteFile(path, content, perm); err != nil {
				return fmt.Errorf("writing file error:\n%w", err)
			}
			return nil
		}
		return fmt.Errorf("stat error:\n%w", err)
	}
	// path must be a file
	if stat.IsDir() {
		return fmt.Errorf("path must be a file(%s)", path)
	}
	return nil
}

// validateCacheDirectory validates and prepares a cache directory
func validateCacheDirectory(path string) error {
	// Normalize to absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("cannot resolve cache directory path: %w", err)
	}

	// Check if exists
	info, err := os.Stat(absPath)
	if os.IsNotExist(err) {
		// Create directory
		if err := os.MkdirAll(absPath, 0755); err != nil {
			return fmt.Errorf("failed to create cache directory: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot access cache directory: %w", err)
	}

	// Check is directory
	if !info.IsDir() {
		return fmt.Errorf("cache path is not a directory: %s", absPath)
	}

	// Check writable (try to create a test file)
	testFile := filepath.Join(absPath, ".test-write")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		return fmt.Errorf("cache directory is not writable: %w", err)
	}
	os.Remove(testFile)

	return nil
}

func NewShadowRoot(inMountPoint, inSrcDir, cacheBaseDir string) (fs.InodeEmbedder, error) {
	mountPoint, err := getMountPoint(inMountPoint)
	if err != nil {
		return nil, fmt.Errorf("get mount point error:\n%w", err)
	}

	srcDir, err := getMountPoint(inSrcDir)
	if err != nil {
		return nil, fmt.Errorf("get source directory error:\n%w", err)
	}

	// get mountID as sha256 of mountPoint
	mountID := fmt.Sprintf("%x", sha256.Sum256([]byte(mountPoint+srcDir)))

	var baseCacheDir string
	if cacheBaseDir != "" {
		// Validate and normalize
		baseCacheDir, err = filepath.Abs(cacheBaseDir)
		if err != nil {
			return nil, fmt.Errorf("invalid cache directory: %w", err)
		}
		if err := validateCacheDirectory(baseCacheDir); err != nil {
			return nil, fmt.Errorf("cache directory validation failed: %w", err)
		}
	} else {
		// Default: use ~/.shadowfs
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home directory error:\n%w", err)
		}
		baseCacheDir = filepath.Join(homeDir, HomeName)
	}

	// create session directory if !exists
	sessionPath := filepath.Join(baseCacheDir, mountID)
	if err := createDir(sessionPath, 0755); err != nil {
		return nil, fmt.Errorf("creating session directory error:\n%w", err)
	}

	cachePath := filepath.Join(sessionPath, RootName)
	if err := createDir(cachePath, 0755); err != nil {
		return nil, fmt.Errorf("creating cache directory error:\n%w", err)
	}

	// create a file into the session directory indicating the target dir
	targetFile := filepath.Join(sessionPath, ".target")
	if err := writeFileOnce(targetFile, []byte(srcDir), 0444); err != nil {
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

func getMountPoint(mountPoint string) (string, error) {
	mountPoint = filepath.Clean(mountPoint)
	if !filepath.IsAbs(mountPoint) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		mountPoint = filepath.Clean(filepath.Join(cwd, mountPoint))
	}

	// check if mountPoint exists and is a directory
	stat, err := os.Stat(mountPoint)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("mount point does not exist:\n%w", err)
		}
		return "", fmt.Errorf("stat error:\n%w", err)
	}

	if !stat.IsDir() {
		return "", fmt.Errorf("mount point(%s) must be a directory", mountPoint)
	}

	return mountPoint, nil
}
