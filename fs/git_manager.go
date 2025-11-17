package fs

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pleclech/shadowfs/fs/cache"
	"github.com/pleclech/shadowfs/fs/ipc"
	"github.com/pleclech/shadowfs/fs/rootinit"
	"github.com/pleclech/shadowfs/fs/xattr"
)

// CommitRequest represents a request to commit files
type CommitRequest struct {
	filePaths  []string
	reason     string
	commitType string // "auto-save", "checkpoint", "unmount"
	result     chan error
}

// GitManager handles automatic Git operations for overlay workspace
type GitManager struct {
	workspacePath   string
	sourcePath      string
	cachePath       string // Cache directory path (.root) - set when available to avoid FUSE deadlocks
	gitDir          string
	enabled         bool
	config          GitConfig
	commitQueue     chan CommitRequest
	wg              sync.WaitGroup
	stopChan        chan struct{}
	once            sync.Once
	pathResolver    *PathResolver  // Optional: for resolving renamed paths
	xattrMgr        *xattr.Manager // Optional: for checking file status (deleted, independent)
	userConfigured  bool           // Cache flag to avoid repeated Git config checks
	userConfigMutex sync.Mutex     // Mutex for thread-safe user configuration

	// IPC client for marking files as dirty (for FUSE cache invalidation)
	// Used when files are restored/modified outside of normal FUSE write path
	// Set by GetGitRepository() if IPC socket exists (FUSE process running)
	ipcClient interface {
		MarkDirty(path string) error
		Close() error
	}
}

// GitConfig contains configuration for Git operations
type GitConfig struct {
	IdleTimeout  time.Duration
	SafetyWindow time.Duration // Delay after last write before committing (default: 5s)
	AutoCommit   bool
}

// NewGitManager creates a new GitManager instance
// cachePath is the cache directory path (.root) - if provided, avoids repeated lookups and FUSE deadlocks
// If empty, cachePath will be resolved lazily via getCachePath() when needed
func NewGitManager(workspacePath, sourcePath, cachePath string, config GitConfig, pathResolver *PathResolver, xattrMgr *xattr.Manager) *GitManager {
	// gitDir points to the actual .git directory (created by git init)
	// git init .gitofs creates .gitofs/.git/ in the mount point
	// From mount point view: {workspacePath}/.gitofs/.git
	gitDir := filepath.Join(cachePath, GitofsName, ".git")
	gm := &GitManager{
		workspacePath: workspacePath,
		sourcePath:    sourcePath,
		cachePath:     cachePath, // Set cache path directly if provided
		gitDir:        gitDir,
		enabled:       config.AutoCommit,
		config:        config,
		commitQueue:   make(chan CommitRequest, 100), // Buffer up to 100 commit requests
		stopChan:      make(chan struct{}),
		pathResolver:  pathResolver,
		xattrMgr:      xattrMgr,
	}

	// Start background commit processor if enabled
	if config.AutoCommit {
		gm.startCommitProcessor()
	}

	return gm
}

// SetIPCClient sets the IPC client for marking files as dirty
// This allows GitManager to communicate with FUSE process when files are restored/modified
// outside of the normal FUSE write path (e.g., during restore operations)
func (gm *GitManager) SetIPCClient(client interface {
	MarkDirty(path string) error
	Close() error
}) {
	gm.ipcClient = client
}

// startCommitProcessor starts a background goroutine to process commits asynchronously
func (gm *GitManager) startCommitProcessor() {
	gm.wg.Add(1)
	go func() {
		defer gm.wg.Done()
		for {
			select {
			case req := <-gm.commitQueue:
				// Process commit request
				Debug("Git commit processor: Processing commit request for %d file(s), type: %s", len(req.filePaths), req.commitType)
				var err error
				if len(req.filePaths) == 1 {
					Debug("Git commit processor: Committing file: %s", req.filePaths[0])
					err = gm.autoCommitFileSync(req.filePaths[0], req.reason, req.commitType)
				} else {
					Debug("Git commit processor: Committing batch of %d files", len(req.filePaths))
					err = gm.autoCommitFilesBatchSync(req.filePaths, req.reason, req.commitType)
				}
				if err != nil {
					Warn("Git commit processor: Error committing: %v", err)
				} else {
					Debug("Git commit processor: Successfully committed")
				}
				if req.result != nil {
					req.result <- err
				}
			case <-gm.stopChan:
				// Process remaining commits before stopping
				Debug("Git commit processor: Stopping, processing remaining commits")
				for {
					select {
					case req := <-gm.commitQueue:
						var err error
						if len(req.filePaths) == 1 {
							err = gm.autoCommitFileSync(req.filePaths[0], req.reason, req.commitType)
						} else {
							err = gm.autoCommitFilesBatchSync(req.filePaths, req.reason, req.commitType)
						}
						if req.result != nil {
							req.result <- err
						}
					default:
						return
					}
				}
			}
		}
	}()
}

// Stop stops the commit processor and waits for pending commits
func (gm *GitManager) Stop() {
	gm.once.Do(func() {
		close(gm.stopChan)
		gm.wg.Wait()
	})
}

// flushCommitQueue waits for all pending commits in the queue to complete
func (gm *GitManager) flushCommitQueue() error {
	if !gm.enabled {
		return nil // Git disabled, nothing to flush
	}

	Debug("flushCommitQueue: Flushing commit queue...")

	// Process all queued commits synchronously
	// We need to drain the queue and wait for all commits to complete
	for {
		select {
		case req := <-gm.commitQueue:
			// Process commit request synchronously
			Debug("flushCommitQueue: Processing queued commit request for %d file(s), type: %s", len(req.filePaths), req.commitType)
			var err error
			if len(req.filePaths) == 1 {
				err = gm.autoCommitFileSync(req.filePaths[0], req.reason, req.commitType)
			} else {
				err = gm.autoCommitFilesBatchSync(req.filePaths, req.reason, req.commitType)
			}
			if err != nil {
				Warn("flushCommitQueue: Error committing: %v", err)
				// Continue processing other commits even if one fails
			}
			if req.result != nil {
				req.result <- err
			}
		default:
			// Queue is empty, wait a bit for any in-flight commits to complete
			// Give the commit processor time to finish current commit
			time.Sleep(100 * time.Millisecond)

			// Check if queue is still empty after brief wait
			select {
			case req := <-gm.commitQueue:
				// Found another commit, process it
				if len(req.filePaths) == 1 {
					_ = gm.autoCommitFileSync(req.filePaths[0], req.reason, req.commitType)
				} else {
					_ = gm.autoCommitFilesBatchSync(req.filePaths, req.reason, req.commitType)
				}
				if req.result != nil {
					req.result <- nil
				}
				continue
			default:
				// Queue is truly empty
				Debug("flushCommitQueue: Commit queue flushed successfully")
				return nil
			}
		}
	}
}

// IsGitAvailable checks if Git command is available
func (gm *GitManager) IsGitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// InitializeRepo initializes Git repository in workspace
func (gm *GitManager) InitializeRepo() error {
	if !gm.enabled {
		return nil // Git disabled, nothing to do
	}

	if !gm.IsGitAvailable() {
		return fmt.Errorf("git not available, auto-versioning disabled")
	}

	// Create Git repository in workspace only
	if err := gm.createWorkspaceGit(); err != nil {
		return fmt.Errorf("failed to create workspace Git: %w", err)
	}

	return nil
}

func (gm *GitManager) RunGit(args ...string) (string, string, error) {
	cmdArgs := []string{"--git-dir", gm.gitDir}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("git", cmdArgs...)
	env := os.Environ()
	env = append(env, "SHADOWFS_CACHE_DIR="+gm.cachePath)
	cmd.Env = env
	cmd.Dir = gm.workspacePath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errStr := stderr.String()
		return stdout.String(), errStr, fmt.Errorf("git %s failed: %w (%s)", strings.Join(args, " "), err, errStr)
	}

	return stdout.String(), stderr.String(), nil
}

// createWorkspaceGit initializes Git repository in workspace
func (gm *GitManager) createWorkspaceGit() error {
	// Initialize Git repository with empty template to avoid issues
	// Use absolute path to avoid FUSE filesystem issues
	gitDir := gm.gitDir

	// if dir already exist in cache path, return nil
	if _, err := os.Stat(gitDir); err == nil {
		return nil
	}

	if err := os.MkdirAll(gitDir, 0755); err != nil {
		return fmt.Errorf("failed to create git directory: %w", err)
	}

	_, _, err := gm.RunGit("init", "--template=")
	if err != nil {
		return err
	}

	_, _, err = gm.RunGit("config", "core.worktree", gm.workspacePath)
	if err != nil {
		return err
	}

	// add Gitofsname to exclude list
	// append ".gitofs/" to the exclude file
	infoPath := filepath.Join(gitDir, "info")
	// create info directory if it doesn't exist
	if err := os.MkdirAll(infoPath, 0755); err != nil {
		return fmt.Errorf("failed to create info directory: %w", err)
	}
	excludeFile := filepath.Join(infoPath, "exclude")
	file, err := os.OpenFile(excludeFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open exclude file: %w", err)
	}
	defer file.Close()
	// write to the file
	_, err = file.WriteString(GitofsName + "/\n")
	if err != nil {
		return fmt.Errorf("failed to write to exclude file: %w", err)
	}
	file.Close()

	// Wait for Git to complete initialization and validate repository
	// This ensures the repository is fully ready before we proceed
	if err := validateGitRepository(gitDir); err != nil {
		return fmt.Errorf("git repository validation failed: %w", err)
	}

	// User configuration is deferred until first commit (Git only requires it for commits, not init)
	// This avoids creating lock files during initialization

	return nil
}

// ensureGitUserConfigured ensures Git user is configured (lazy initialization with caching)
// Uses double-check locking pattern for thread safety and efficiency
func (gm *GitManager) ensureGitUserConfigured() error {
	// Fast path: check cached flag first (no Git command, ~1ns)
	if gm.userConfigured {
		return nil
	}

	// Slow path: acquire mutex and check again (double-check pattern)
	gm.userConfigMutex.Lock()
	defer gm.userConfigMutex.Unlock()

	// Double-check after acquiring mutex (another goroutine might have configured it)
	if gm.userConfigured {
		return nil
	}

	// Check if user.name is configured
	out, _, err := gm.RunGit("config", "user.name")
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(out)) > 0 {
		gm.userConfigured = true
		return nil
	}

	_, _, err = gm.RunGit("config", "user.name", "ShadowFS")
	if err != nil {
		return err
	}
	out, _, err = gm.RunGit("config", "user.email", "shadowfs@localhost")
	if err != nil {
		return err
	}

	// Mark as configured after successful setup
	gm.userConfigured = true

	return nil
}

// AutoCommitFile stages and commits a file with automatic message (async, non-blocking)
func (gm *GitManager) AutoCommitFile(filePath string, reason string) error {
	if !gm.enabled {
		Debug("AutoCommitFile: Git disabled, skipping commit for %s", filePath)
		return nil
	}

	Debug("AutoCommitFile: Queuing commit for %s (reason: %s)", filePath, reason)
	// Queue commit request asynchronously
	result := make(chan error, 1)
	select {
	case gm.commitQueue <- CommitRequest{
		filePaths:  []string{filePath},
		reason:     reason,
		commitType: "auto-save",
		result:     result,
	}:
		// Commit queued successfully, return immediately (non-blocking)
		// Error will be logged by background processor
		Debug("AutoCommitFile: Successfully queued commit for %s", filePath)
		return nil
	default:
		// Queue full, fall back to synchronous commit
		Debug("AutoCommitFile: Commit queue full, committing synchronously: %s", filePath)
		return gm.autoCommitFileSync(filePath, reason, "auto-save")
	}
}

// CommitFileSync commits a file synchronously (blocks until complete)
// Use this for shutdown scenarios where you need to ensure commits complete
func (gm *GitManager) CommitFileSync(filePath string, reason string) error {
	return gm.autoCommitFileSync(filePath, reason, "unmount")
}

// CommitFileCheckpoint commits a file synchronously as a checkpoint (user-verified valid state)
func (gm *GitManager) CommitFileCheckpoint(filePath string, reason string) error {
	return gm.autoCommitFileSync(filePath, reason, "checkpoint")
}

// CommitFilesBatchSync commits multiple files synchronously (blocks until complete)
// Use this for shutdown scenarios where you need to ensure commits complete
func (gm *GitManager) CommitFilesBatchSync(filePaths []string, reason string) error {
	return gm.autoCommitFilesBatchSync(filePaths, reason, "unmount")
}

// CommitFilesBatchCheckpoint commits multiple files synchronously as a checkpoint
func (gm *GitManager) CommitFilesBatchCheckpoint(filePaths []string, reason string) error {
	return gm.autoCommitFilesBatchSync(filePaths, reason, "checkpoint")
}

// autoCommitFileSync performs the actual commit synchronously (internal use)
func (gm *GitManager) autoCommitFileSync(filePath string, reason string, commitType string) error {
	Debug("autoCommitFileSync: Starting commit for %s (type: %s, reason: %s)", filePath, commitType, reason)
	Debug("autoCommitFileSync: Workspace path: %s, Git dir: %s", gm.workspacePath, gm.gitDir)

	// Ensure Git user is configured before committing (lazy initialization)
	if err := gm.ensureGitUserConfigured(); err != nil {
		return fmt.Errorf("failed to ensure Git user configuration: %w", err)
	}

	// Validate and convert absolute path to relative path
	relativePath, err := gm.normalizePath(filePath)
	if err != nil {
		return fmt.Errorf("invalid path %s: %w", filePath, err)
	}
	Debug("autoCommitFileSync: Normalized path: %s -> %s", filePath, relativePath)

	// Check if file has changes before committing (efficiency improvement)
	hasChanges := gm.hasFileChanges(relativePath)
	Debug("autoCommitFileSync: File %s has changes: %v", relativePath, hasChanges)
	if !hasChanges {
		Debug("autoCommitFileSync: No changes detected for %s, skipping commit", relativePath)
		return nil // No changes, skip commit
	}

	// Stage the file

	_, _, err = gm.RunGit("add", relativePath)

	Debug("autoCommitFileSync: Staging file: git --git-dir %s add %s", gm.gitDir, relativePath)
	if err != nil {
		return fmt.Errorf("failed to stage file %s: %w", relativePath, err)
	}
	Debug("autoCommitFileSync: Successfully staged file %s", relativePath)

	// Build commit message with type metadata
	// For single file commits, show file count (not file name to keep it concise)
	var commitMsg string
	switch commitType {
	case "checkpoint":
		commitMsg = "Checkpoint: " + reason
	case "unmount":
		commitMsg = "Auto-commit on unmount: 1 file"
	default: // "auto-save"
		commitMsg = "Auto-commit: 1 file"
	}

	// Commit with message
	_, _, err = gm.RunGit("commit", "-m", commitMsg)
	if err != nil {
		if strings.Contains(err.Error(), "nothing to commit") {
			Debug("autoCommitFileSync: Nothing to commit for %s", relativePath)
			return nil // Not an error, just no changes
		}
		return fmt.Errorf("failed to commit file %s: %w", relativePath, err)
	}

	// Tag commit with type if checkpoint
	if commitType == "checkpoint" {
		tagName := fmt.Sprintf("checkpoint-%s-%d", relativePath, time.Now().Unix())
		_, _, err = gm.RunGit("tag", tagName)
		if err != nil {
			return fmt.Errorf("failed to tag commit %s: %w", relativePath, err)
		}
	}

	return nil
}

// AutoCommitFilesBatch stages and commits multiple files in a single commit (async, non-blocking)
func (gm *GitManager) AutoCommitFilesBatch(filePaths []string, reason string) error {
	if !gm.enabled {
		return nil
	}

	if len(filePaths) == 0 {
		return nil
	}

	// Queue commit request asynchronously
	result := make(chan error, 1)
	select {
	case gm.commitQueue <- CommitRequest{
		filePaths:  filePaths,
		reason:     reason,
		commitType: "auto-save",
		result:     result,
	}:
		// Commit queued successfully, return immediately (non-blocking)
		return nil
	default:
		// Queue full, fall back to synchronous commit
		Debug("Commit queue full, committing batch synchronously (%d files)", len(filePaths))
		return gm.autoCommitFilesBatchSync(filePaths, reason, "auto-save")
	}
}

// autoCommitFilesBatchSync performs the actual batch commit synchronously (internal use)
func (gm *GitManager) autoCommitFilesBatchSync(filePaths []string, reason string, commitType string) error {
	if len(filePaths) == 0 {
		return nil
	}

	// Ensure Git user is configured before committing (lazy initialization)
	if err := gm.ensureGitUserConfigured(); err != nil {
		return fmt.Errorf("failed to ensure Git user configuration: %w", err)
	}

	// Normalize all paths
	relativePaths := make([]string, 0, len(filePaths))
	for _, filePath := range filePaths {
		relPath, err := gm.normalizePath(filePath)
		if err != nil {
			Warn("Invalid path %s: %v", filePath, err)
			// Log error but continue with other files
			continue
		}
		relativePaths = append(relativePaths, relPath)
	}

	if len(relativePaths) == 0 {
		return nil // No valid paths to commit
	}

	// Separate files into those needing original state commit and those ready for modification commit
	var filesNeedingOriginalState []string
	var filesReadyForCommit []string

	// TODO: wtf vars not used ?
	for _, relPath := range relativePaths {
		if !gm.hasFileBeenCommitted(relPath) {
			filesNeedingOriginalState = append(filesNeedingOriginalState, relPath)
		} else {
			filesReadyForCommit = append(filesReadyForCommit, relPath)
		}
	}

	// Stage all files (both original states and modifications)
	for _, relPath := range relativePaths {
		_, _, err := gm.RunGit("add", relPath)
		if err != nil {
			return fmt.Errorf("failed to stage file %s: %w", relPath, err)
		}
	}

	// Build commit message with type metadata
	// Show file count only (not file list to keep it concise and scalable)
	var commitMsg string
	switch commitType {
	case "checkpoint":
		commitMsg = fmt.Sprintf("Checkpoint: %s (%d files)", reason, len(relativePaths))
	case "unmount":
		commitMsg = fmt.Sprintf("Auto-commit on unmount: %d files", len(relativePaths))
	default: // "auto-save"
		commitMsg = fmt.Sprintf("Auto-commit: %d files", len(relativePaths))
	}

	// Commit all files together
	_, _, err := gm.RunGit("commit", "-m", commitMsg)
	if err != nil {
		// Check if commit failed because there are no changes
		if strings.Contains(err.Error(), "nothing to commit") {
			return nil // Not an error, just no changes
		}
		return fmt.Errorf("failed to commit batch: %w", err)
	}

	// Tag commit with type if checkpoint
	if commitType == "checkpoint" {
		tagName := fmt.Sprintf("checkpoint-batch-%d", time.Now().Unix())
		_, _, err = gm.RunGit("tag", tagName)
		if err != nil {
			return fmt.Errorf("failed to tag commit: %w", err)
		}
	}

	return nil
}

// normalizePath converts absolute path to relative path, validates it
func (gm *GitManager) normalizePath(filePath string) (string, error) {
	if !filepath.IsAbs(filePath) {
		// Already relative, validate it's within workspace
		Debug("normalizePath: Path %s is already relative", filePath)
		return filePath, nil
	}

	// Convert absolute cache path to relative path for git
	rel, err := filepath.Rel(gm.workspacePath, filePath)
	if err != nil {
		return "", fmt.Errorf("path %s is not within workspace %s: %w", filePath, gm.workspacePath, err)
	}

	// Security: prevent path traversal
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("invalid path traversal detected: %s", rel)
	}

	return rel, nil
}

// hasFileChanges checks if a file has uncommitted changes
func (gm *GitManager) hasFileChanges(relativePath string) bool {
	// Check if file is new (untracked)
	_, _, err := gm.RunGit("ls-files", "--error-unmatch", "--", relativePath)
	if err != nil {
		// File is not tracked, so it's a new file (has changes)
		Debug("hasFileChanges: File %s is not tracked (new file), has changes: true", relativePath)
		return true
	}

	// File is tracked, check if it has modifications
	_, _, err = gm.RunGit("diff", "--quiet", "--", relativePath)
	// git diff --quiet returns 0 if no changes, 1 if changes exist
	hasChanges := err != nil
	Debug("hasFileChanges: File %s is tracked, has changes: %v", relativePath, hasChanges)
	return hasChanges
}

// hasFileBeenCommitted checks if a file has any commits in Git history
func (gm *GitManager) hasFileBeenCommitted(relativePath string) bool {
	// Check if file has any commits in history
	sOut, _, err := gm.RunGit("log", "--oneline", "--", relativePath)
	if err != nil {
		return false
	}

	// If output is empty, file hasn't been committed
	hasCommits := len(strings.TrimSpace(sOut)) > 0
	Debug("hasFileBeenCommitted: File %s has commits: %v", relativePath, hasCommits)
	return hasCommits
}

// commitOriginalStateSync commits the original state of a file from source synchronously
// This is called immediately after COW to preserve the original state before modifications
// Returns error only if critical failure occurs (non-critical errors are logged)
func (gm *GitManager) commitOriginalStateSync(relativePath string) error {
	// Check if file has been committed before (avoid duplicate commits)
	if gm.hasFileBeenCommitted(relativePath) {
		Debug("commitOriginalStateSync: File %s already committed, skipping", relativePath)
		return nil // Already committed, skip
	}

	// Stage the file (file is now in cache with original content from source)
	_, _, err := gm.RunGit("add", relativePath)
	if err != nil {
		return fmt.Errorf("failed to stage original state file %s: %w", relativePath, err)
	}
	Debug("commitOriginalStateSync: Staged original state file %s", relativePath)

	// Commit with message indicating it's the original state
	commitMsg := "Initial state: " + relativePath
	sOut, _, err := gm.RunGit("commit", "-m", commitMsg)
	if err != nil {
		// Check if commit failed because there are no changes
		if strings.Contains(sOut, "nothing to commit") {
			Debug("commitOriginalStateSync: Nothing to commit for original state of %s", relativePath)
			return nil // Not an error, just no changes
		}
		return fmt.Errorf("failed to commit original state file %s: %w", relativePath, err)
	}
	Debug("commitOriginalStateSync: Successfully committed original state file %s", relativePath)

	return nil
}

// commitDeletion commits a file or directory deletion
func (gm *GitManager) commitDeletion(relativePath string) error {
	if !gm.enabled {
		return nil // Git disabled, skip
	}

	// Step 1: Flush commit queue to ensure all pending modifications are committed first
	if err := gm.flushCommitQueue(); err != nil {
		Warn("commitDeletion: Failed to flush commit queue: %v (continuing)", err)
		// Continue even if flush fails
	}

	// Skip ignore check - git will handle ignored files automatically
	// Checking ignores can cause deadlocks when workspacePath is a FUSE mount point

	// Check if file is independent (created in cache, not from source)
	// If NOT independent, we need to commit original state first so user can restore it
	isIndependent := false
	if gm.xattrMgr != nil {
		workspaceFilePath := filepath.Join(gm.workspacePath, relativePath)
		attr, exists, errno := gm.xattrMgr.GetStatus(workspaceFilePath)
		if errno == 0 && exists && attr != nil {
			isIndependent = attr.CacheIndependent
		}
	}

	// If file is NOT independent (exists in source), commit original state first
	// This allows user to restore the original file later
	if !isIndependent {
		// Check if original state has been committed - if not, commit it now
		if !gm.hasFileBeenCommitted(relativePath) {
			Debug("commitDeletion: Path %s is not independent and not committed, committing original state first", relativePath)
			if err := gm.commitOriginalStateSync(relativePath); err != nil {
				Warn("commitDeletion: Failed to commit original state for %s: %v (continuing)", relativePath, err)
				// Continue even if original state commit failed (e.g., file doesn't exist in source)
			}
		} else {
			Debug("commitDeletion: Path %s is not independent but already has commits, original state is preserved", relativePath)
		}
	} else {
		Debug("commitDeletion: Path %s is independent (created in cache), skipping original state commit", relativePath)
	}

	// Check if file is tracked in git index - only commit deletion if tracked
	_, _, err := gm.RunGit("ls-files", "--error-unmatch", "--", relativePath)
	if err != nil {
		// File is not tracked - nothing to delete in git
		Debug("commitDeletion: Path %s is not tracked in git, skipping deletion commit", relativePath)
		return nil // Not an error - can't delete what was never tracked
	}

	// Stage the deletion (git rm to stage deletion)
	// Use git ls-files to determine if it's a file or directory, avoiding filesystem access
	// This prevents hangs when workspacePath is a FUSE mount point
	sOut, _, err := gm.RunGit("ls-files", "--directory", "--", relativePath+"/")
	isDir := err == nil && len(sOut) > 0

	if isDir {
		// It's a directory - git tracks directory deletion by removing from index
		// Use git rm -r to remove directory and all contents
		_, _, err := gm.RunGit("rm", "-r", "--cached", "--ignore-unmatch", relativePath)
		if err != nil {
			return fmt.Errorf("failed to stage directory deletion for %s: %w", relativePath, err)
		}
	} else {
		// It's a file - use git rm to stage deletion
		_, _, err := gm.RunGit("rm", "--cached", "--ignore-unmatch", relativePath)
		if err != nil {
			return fmt.Errorf("failed to stage file deletion for %s: %w", relativePath, err)
		}
	}

	Debug("commitDeletion: Successfully staged deletion for %s", relativePath)

	// Resolve source path for metadata
	sourcePath := relativePath
	if gm.pathResolver != nil {
		workspaceFilePath := filepath.Join(gm.workspacePath, relativePath)
		_, resolvedSourcePath, _, errno := gm.pathResolver.ResolvePaths(workspaceFilePath)
		if errno == 0 && resolvedSourcePath != "" {
			// Convert absolute path to relative path
			if relPath, err := filepath.Rel(gm.sourcePath, resolvedSourcePath); err == nil {
				sourcePath = filepath.ToSlash(relPath)
			}
		}
	}

	// Store metadata before committing
	metadata := Metadata{
		Deleted:          true,
		SourcePath:       sourcePath,
		CacheIndependent: isIndependent,
	}
	if err := gm.storeXAttrMetadata(relativePath, metadata); err != nil {
		Warn("commitDeletion: Failed to store metadata for %s: %v (continuing)", relativePath, err)
		// Continue even if metadata storage fails
	}

	// Stage metadata file
	metadataRelativePath := filepath.Join(".shadowfs-metadata", relativePath+".json")
	_, _, err = gm.RunGit("add", metadataRelativePath)
	if err != nil {
		return fmt.Errorf("failed to stage metadata file %s: %w", metadataRelativePath, err)
	}

	// Commit the deletion
	commitMsg := "Delete: " + relativePath
	_, _, err = gm.RunGit("commit", "-m", commitMsg)
	if err != nil {
		return fmt.Errorf("failed to commit deletion for %s: %w", relativePath, err)
	}
	Debug("commitDeletion: Successfully committed deletion for %s", relativePath)

	return nil
}

// commitRenameMetadata commits metadata for a rename operation
func (gm *GitManager) commitRenameMetadata(sourcePath, destPath string) error {
	if !gm.enabled {
		return nil // Git disabled, skip
	}

	// Step 1: Flush commit queue to ensure all pending modifications are committed first
	if err := gm.flushCommitQueue(); err != nil {
		Warn("commitRenameMetadata: Failed to flush commit queue: %v (continuing)", err)
		// Continue even if flush fails
	}

	// Convert absolute paths to relative paths
	sourceRelative := sourcePath
	destRelative := destPath
	if relPath, err := filepath.Rel(gm.sourcePath, sourcePath); err == nil {
		sourceRelative = filepath.ToSlash(relPath)
	}
	if relPath, err := filepath.Rel(gm.sourcePath, destPath); err == nil {
		destRelative = filepath.ToSlash(relPath)
	}

	// Resolve source path for metadata (handles nested renames)
	resolvedSourcePath := sourceRelative
	if gm.pathResolver != nil {
		workspaceFilePath := filepath.Join(gm.workspacePath, destRelative)
		_, resolvedSourcePathAbs, _, errno := gm.pathResolver.ResolvePaths(workspaceFilePath)
		if errno == 0 && resolvedSourcePathAbs != "" {
			if relPath, err := filepath.Rel(gm.sourcePath, resolvedSourcePathAbs); err == nil {
				resolvedSourcePath = filepath.ToSlash(relPath)
			}
		}
	}

	// Check if destination is independent
	isIndependent := false
	if gm.xattrMgr != nil {
		workspaceFilePath := filepath.Join(gm.workspacePath, destRelative)
		attr, exists, errno := gm.xattrMgr.GetStatus(workspaceFilePath)
		if errno == 0 && exists && attr != nil {
			isIndependent = attr.CacheIndependent
		}
	}

	// Skip metadata for Git internal files (.gitofs/.git/*)
	if strings.HasPrefix(sourceRelative, GitofsName+"/") || strings.HasPrefix(destRelative, GitofsName+"/") {
		return nil // Don't store metadata for Git internal files
	}

	// Store metadata
	metadata := Metadata{
		RenamedFrom:      sourceRelative,
		SourcePath:       resolvedSourcePath,
		CacheIndependent: isIndependent,
	}
	if err := gm.storeXAttrMetadata(destRelative, metadata); err != nil {
		Warn("commitRenameMetadata: Failed to store metadata for %s: %v (continuing)", destRelative, err)
		// Continue even if metadata storage fails
	}

	// Stage metadata file
	metadataRelativePath := filepath.Join(".shadowfs-metadata", destRelative+".json")
	_, _, err := gm.RunGit("add", metadataRelativePath)
	if err != nil {
		return fmt.Errorf("failed to stage metadata file %s: %w", metadataRelativePath, err)
	}

	// Commit the rename metadata
	commitMsg := fmt.Sprintf("Rename: %s → %s", sourceRelative, destRelative)
	sOut, _, err := gm.RunGit("commit", "-m", commitMsg)
	if err != nil {
		// Check if commit failed because there are no changes
		if strings.Contains(sOut, "nothing to commit") {
			Debug("commitRenameMetadata: Nothing to commit for rename %s → %s", sourceRelative, destRelative)
			return nil // Not an error, just no changes
		}
		return fmt.Errorf("failed to commit rename metadata for %s → %s: %w", sourceRelative, destRelative, err)
	}
	Debug("commitRenameMetadata: Successfully committed rename metadata for %s → %s", sourceRelative, destRelative)

	return nil
}

// commitDirectoryDeletionRecursive commits a directory deletion recursively
func (gm *GitManager) commitDirectoryDeletionRecursive(relativePath string) error {
	if !gm.enabled {
		return nil // Git disabled, skip
	}

	// Step 1: Flush commit queue to ensure all pending modifications are committed first
	if err := gm.flushCommitQueue(); err != nil {
		Warn("commitDirectoryDeletionRecursive: Failed to flush commit queue: %v (continuing)", err)
		// Continue even if flush fails
	}

	// Walk the SOURCE directory (not workspace/mount point) to find all files
	// This is safe because sourcePath is a real filesystem path, not a FUSE mount
	// We need to find ALL files (not just tracked ones) to commit original states
	sourceDirPath := filepath.Join(gm.sourcePath, relativePath)

	var filesToCommit []string
	var dirsToCommit []string

	// Walk source directory to find all files
	err := filepath.Walk(sourceDirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Continue on error (e.g., permission denied)
		}

		// Convert to relative path from source root
		relPath, err := filepath.Rel(gm.sourcePath, path)
		if err != nil {
			return nil // Skip if can't make relative
		}

		// Normalize path separators
		relPath = filepath.ToSlash(relPath)

		// Skip if not under the target directory
		if !strings.HasPrefix(relPath, relativePath+"/") && relPath != relativePath {
			if info.IsDir() {
				return filepath.SkipDir // Skip directories outside target
			}
			return nil // Skip files outside target
		}

		// Skip the root directory itself
		if relPath == relativePath {
			return nil
		}

		if info.IsDir() {
			// Add subdirectory
			dirsToCommit = append(dirsToCommit, relPath)
		} else {
			// Add file
			filesToCommit = append(filesToCommit, relPath)
		}

		return nil
	})

	if err != nil {
		Warn("commitDirectoryDeletionRecursive: Error walking source directory %s: %v (continuing with what we found)", sourceDirPath, err)
		// Continue anyway - try to commit what we found
	}

	// Also get tracked files from git (for deletion commit)
	// This ensures we commit deletions for files that were tracked
	sOut, _, err := gm.RunGit("ls-files", relativePath+"/")
	if err != nil {
		// Try without trailing slash
		sOut, _, err = gm.RunGit("ls-files", relativePath)
	}

	// Add tracked files that might not be in source (e.g., created in cache)
	if err == nil && len(sOut) > 0 {
		lines := strings.Split(strings.TrimSpace(sOut), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(filepath.ToSlash(line))
			if line == "" || line == relativePath {
				continue
			}
			// Check if already in filesToCommit
			found := false
			for _, f := range filesToCommit {
				if f == line {
					found = true
					break
				}
			}
			if !found && strings.HasPrefix(line, relativePath+"/") {
				filesToCommit = append(filesToCommit, line)
			}
		}
	}

	// Commit original states for files that haven't been committed before
	// Check if files are independent - only commit original state for non-independent files
	for _, filePath := range filesToCommit {
		// Check if file is independent
		isIndependent := false
		if gm.xattrMgr != nil {
			workspaceFilePath := filepath.Join(gm.workspacePath, filePath)
			attr, exists, errno := gm.xattrMgr.GetStatus(workspaceFilePath)
			if errno == 0 && exists && attr != nil {
				isIndependent = attr.CacheIndependent
			}
		}

		// Only commit original state for non-independent files that haven't been committed
		if !isIndependent && !gm.hasFileBeenCommitted(filePath) {
			Debug("commitDirectoryDeletionRecursive: Committing original state for non-independent file %s", filePath)
			if err := gm.commitOriginalStateSync(filePath); err != nil {
				Warn("commitDirectoryDeletionRecursive: Failed to commit original state for %s: %v (continuing)", filePath, err)
				// Continue with other files
			}
		} else if isIndependent {
			Debug("commitDirectoryDeletionRecursive: File %s is independent, skipping original state commit", filePath)
		}
	}

	// Stage all deletions: files first, then directories (bottom-up)
	// Stage file deletions
	for _, filePath := range filesToCommit {
		_, _, err := gm.RunGit("rm", "--cached", "--ignore-unmatch", filePath)
		if err != nil {
			Warn("commitDirectoryDeletionRecursive: Failed to stage file deletion %s: %v (continuing)", filePath, err)
			// Continue with other files
		}
	}

	// Stage directory deletions (bottom-up order)
	// Sort directories by depth (deeper first)
	for i := len(dirsToCommit) - 1; i >= 0; i-- {
		dirPath := dirsToCommit[i]
		_, _, err := gm.RunGit("rm", "-r", "--cached", "--ignore-unmatch", dirPath)
		if err != nil {
			Warn("commitDirectoryDeletionRecursive: Failed to stage directory deletion %s: %v (continuing)", dirPath, err)
			// Continue with other directories
		}
	}

	// Stage top-level directory deletion
	_, _, err = gm.RunGit("rm", "-r", "--cached", "--ignore-unmatch", relativePath)
	if err != nil {
		return fmt.Errorf("failed to stage directory deletion for %s: %w", relativePath, err)
	}

	// Resolve source path for metadata
	sourcePath := relativePath
	if gm.pathResolver != nil {
		workspaceFilePath := filepath.Join(gm.workspacePath, relativePath)
		_, resolvedSourcePath, _, errno := gm.pathResolver.ResolvePaths(workspaceFilePath)
		if errno == 0 && resolvedSourcePath != "" {
			// Convert absolute path to relative path
			if relPath, err := filepath.Rel(gm.sourcePath, resolvedSourcePath); err == nil {
				sourcePath = filepath.ToSlash(relPath)
			}
		}
	}

	// Store metadata for directory deletion
	metadata := Metadata{
		Deleted:          true,
		SourcePath:       sourcePath,
		CacheIndependent: false, // Directories are not independent
	}
	if err := gm.storeXAttrMetadata(relativePath, metadata); err != nil {
		Warn("commitDirectoryDeletionRecursive: Failed to store metadata for %s: %v (continuing)", relativePath, err)
		// Continue even if metadata storage fails
	}

	// Stage metadata file
	metadataRelativePath := filepath.Join(".shadowfs-metadata", relativePath+".json")
	_, _, err = gm.RunGit("add", metadataRelativePath)
	if err != nil {
		return fmt.Errorf("failed to stage metadata file %s: %w", metadataRelativePath, err)
	}

	// Commit all deletions together
	fileCount := len(filesToCommit)
	dirCount := len(dirsToCommit) + 1 // +1 for top-level directory
	commitMsg := fmt.Sprintf("Delete directory: %s (%d files, %d dirs)", relativePath, fileCount, dirCount)
	sOut, _, err = gm.RunGit("commit", "-m", commitMsg)
	if err != nil {
		// Check if commit failed because there are no changes
		if strings.Contains(sOut, "nothing to commit") {
			Debug("commitDirectoryDeletionRecursive: Nothing to commit for deletion of %s", relativePath)
			return nil // Not an error, just no changes
		}
		return fmt.Errorf("failed to commit directory deletion for %s: %w", relativePath, err)
	}
	Debug("commitDirectoryDeletionRecursive: Successfully committed directory deletion for %s", relativePath)

	return nil
}

// GetWorkspacePath returns the workspace path
func (gm *GitManager) GetWorkspacePath() string {
	return gm.workspacePath
}

// getCachePath returns the cache directory path (.root) for file operations
// This avoids FUSE deadlocks by accessing files directly in the cache
func (gm *GitManager) getCachePath() (string, error) {
	// Use cached path if available
	if gm.cachePath != "" {
		Debug("getCachePath: Using cached cache path: %s", gm.cachePath)
		return gm.cachePath, nil
	}
	// Find cache directory (session path)
	Debug("getCachePath: Finding cache directory for workspace: %s", gm.workspacePath)
	cacheDir, err := rootinit.FindCacheDirectory(gm.workspacePath)
	if err != nil {
		return "", fmt.Errorf("failed to find cache directory: %w", err)
	}
	// Cache the result
	gm.cachePath = cache.GetCachePath(cacheDir)
	Debug("getCachePath: Found cache directory: %s (session: %s)", gm.cachePath, cacheDir)
	return gm.cachePath, nil
}

// IsEnabled returns whether Git auto-versioning is enabled
func (gm *GitManager) IsEnabled() bool {
	return gm.enabled
}

// GetConfig returns the Git configuration
func (gm *GitManager) GetConfig() GitConfig {
	return gm.config
}

// LogOptions contains options for git log command
type LogOptions struct {
	Format   string   // Custom format string (e.g., "%h %ad %s")
	Reverse  bool     // Use --reverse flag
	Oneline  bool     // Use --oneline flag
	Graph    bool     // Use --graph flag
	Stat     bool     // Use --stat flag
	NameOnly bool     // Use --name-only flag to show changed files
	Limit    int      // Limit number of commits (0 = no limit)
	Paths    []string // Paths to filter commits
	Since    string   // Show commits since date/time
}

// DiffOptions contains options for git diff command
type DiffOptions struct {
	Stat       bool     // Use --stat flag
	CommitArgs []string // Commit arguments (e.g., "HEAD~1", "HEAD")
	Paths      []string // Paths to filter diff
}

// ErrNoCommits is returned when git log is run on a repository with no commits
var ErrNoCommits = fmt.Errorf("no commits found yet")

func (gm *GitManager) GetCommitTimestamp(commit, path string) (time.Time, error) {
	args := []string{"log", "-1", "--format=%ct", commit, "--", path}
	var stdout strings.Builder
	if err := gm.executeGitCommand(args, &stdout, nil); err != nil {
		return time.Time{}, fmt.Errorf("failed to get commit timestamp for %s %s: %w", commit, path, err)
	}
	secStr := strings.TrimSpace(stdout.String())
	sec, _ := strconv.ParseInt(secStr, 10, 64)
	return time.Unix(sec, 0), nil
}

func (gm *GitManager) executeGitCommand(args []string, stdout, stderr io.Writer) error {
	// Build command with consistent git directory and working directory
	cmdArgs := []string{"--git-dir", gm.gitDir}
	cmdArgs = append(cmdArgs, args...)

	Debug("executeGitCommand: Running git command: git %v", cmdArgs)

	cmd := exec.Command("git", cmdArgs...)
	cmd.Dir = gm.workspacePath
	cmd.Env = os.Environ()
	if stdout != nil {
		cmd.Stdout = stdout
	}

	// Always capture stderr to detect "no commits" error
	var stderrBuf strings.Builder
	if stderr != nil {
		// If caller provided stderr writer, use multi-writer to capture and write
		cmd.Stderr = io.MultiWriter(stderr, &stderrBuf)
	} else {
		cmd.Stderr = &stderrBuf
	}

	err := cmd.Run()
	if err != nil {
		// Always capture stderr for error messages
		stderrStr := strings.TrimSpace(stderrBuf.String())

		// Check if this is a "no commits" error
		if strings.Contains(stderrStr, "does not have any commits yet") ||
			(strings.Contains(stderrStr, "your current branch") && strings.Contains(stderrStr, "does not have any commits")) {
			return ErrNoCommits
		}

		// Wrap error with git stderr output for better error messages
		if stderrStr != "" {
			return fmt.Errorf("git error: %s: %w", stderrStr, err)
		}

		return fmt.Errorf("git command failed: %w", err)
	}

	return nil
}

// StatusPorcelain returns a list of changed files using git status --porcelain
func (gm *GitManager) StatusPorcelain() ([]string, error) {
	var output strings.Builder
	err := gm.executeGitCommand([]string{"status", "--porcelain"}, &output, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to run git status: %w", err)
	}

	var changedFiles []string
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Parse porcelain format: XY filename
		// Extract filename (skip status characters and space)
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			// Join all parts after status to handle filenames with spaces
			filename := strings.Join(parts[1:], " ")
			changedFiles = append(changedFiles, filename)
		}
	}

	return changedFiles, nil
}

// ValidateCommitHash checks if a commit hash exists in the repository
func (gm *GitManager) ValidateCommitHash(hash string) error {
	err := gm.executeGitCommand([]string{"cat-file", "-e", hash}, nil, nil)
	if err != nil {
		return fmt.Errorf("commit hash does not exist in repository: %s", hash)
	}
	return nil
}

// ResolveCommitHash resolves a commit reference (hash, HEAD~N, tag, etc.) to a full commit hash
func (gm *GitManager) ResolveCommitHash(ref string) (string, error) {
	var stdout strings.Builder
	if err := gm.executeGitCommand([]string{"rev-parse", ref}, &stdout, nil); err != nil {
		return "", fmt.Errorf("invalid commit hash or reference '%s': %w", ref, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Log executes git log with the provided options and writes output to stdout
func (gm *GitManager) Log(options LogOptions, stdout io.Writer) error {
	args := []string{"log"}

	if options.Format != "" {
		args = append(args, "--pretty=format:"+options.Format)
	} else if options.Oneline {
		args = append(args, "--oneline")
	} else {
		args = append(args, "--pretty=format:%h %ad %s", "--date=short")
	}

	if options.Graph {
		args = append(args, "--graph")
	}

	// --stat and --name-only are mutually exclusive
	if options.Stat {
		args = append(args, "--stat")
	} else if options.NameOnly {
		args = append(args, "--name-only")
	}

	if options.Reverse {
		args = append(args, "--reverse")
	}

	if options.Since != "" {
		args = append(args, "--since="+options.Since)
	}

	if options.Limit > 0 {
		args = append(args, fmt.Sprintf("-%d", options.Limit))
	}

	if len(options.Paths) > 0 {
		args = append(args, "--")
		args = append(args, options.Paths...)
	}

	return gm.executeGitCommand(args, stdout, nil)
}

// Diff executes git diff with the provided options and writes output to stdout
func (gm *GitManager) Diff(options DiffOptions, stdout io.Writer) error {
	args := []string{"diff"}

	if options.Stat {
		args = append(args, "--stat")
	}

	// Add commit arguments (e.g., "HEAD~1", "HEAD")
	args = append(args, options.CommitArgs...)

	if len(options.Paths) > 0 {
		args = append(args, "--")
		args = append(args, options.Paths...)
	}

	// Capture output to check if diff is empty
	var outputBuf strings.Builder
	var multiWriter io.Writer
	if stdout != nil {
		multiWriter = io.MultiWriter(stdout, &outputBuf)
	} else {
		multiWriter = &outputBuf
	}

	err := gm.executeGitCommand(args, multiWriter, nil)
	if err != nil {
		return err
	}

	// Check if diff output is empty (no differences)
	outputStr := strings.TrimSpace(outputBuf.String())
	if outputStr == "" && stdout != nil {
		// Write helpful message if diff is empty
		fmt.Fprintf(stdout, "No differences found\n")
	}

	return nil
}

// Checkout executes git checkout to restore files from a specific commit
func (gm *GitManager) Checkout(commitHash string, paths []string, force bool, stdout, stderr io.Writer) error {
	args := []string{"checkout"}

	if force {
		args = append(args, "-f")
	}

	args = append(args, commitHash, "--")
	args = append(args, paths...)

	return gm.executeGitCommand(args, stdout, stderr)
}

// isCommitLater checks if commit1 is later than commit2 (commit2 is ancestor of commit1)
func (gm *GitManager) isCommitLater(commit1, commit2 string) (bool, error) {
	// Use merge-base to check if commit2 is ancestor of commit1
	err := gm.executeGitCommand([]string{"merge-base", "--is-ancestor", commit2, commit1}, nil, nil)
	return err == nil, nil
}

// getCommitRange returns the list of commits between fromCommit (inclusive) and toCommit (exclusive)
func (gm *GitManager) getCommitRange(fromCommit, toCommit string) ([]string, bool, error) {
	// Determine direction using merge-base
	isForward, err := gm.isCommitLater(fromCommit, toCommit)
	if err != nil {
		return nil, false, fmt.Errorf("failed to determine commit direction: %w", err)
	}

	args := []string{
		"log",
		"--oneline",
		"--format=%H",
	}

	if isForward {
		args = append(args, "--reverse")
	}

	args = append(args, fromCommit+".."+toCommit+"~1")

	var output strings.Builder
	if err := gm.executeGitCommand(args, &output, nil); err != nil {
		// Check if commits are the same (no range)
		if strings.Contains(err.Error(), "no merge base") || strings.Contains(err.Error(), "fatal") {
			// Commits might be unrelated or same, return empty range
			return []string{}, isForward, nil
		}
		return nil, isForward, fmt.Errorf("failed to get commit range: %w", err)
	}

	// Parse output: each line is a commit hash
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	commits := make([]string, 0, len(lines)+1)
	if isForward {
		commits = append(commits, fromCommit)
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			commits = append(commits, line)
		}
	}
	if !isForward {
		commits = append(commits, fromCommit)
	}

	return commits, isForward, nil
}

// getFilesChangedInCommit returns list of files changed in a commit
func (gm *GitManager) getFilesInCommit(commitHash string, paths []string) ([]string, []string, error) {
	var stdout strings.Builder
	args := []string{"ls-tree", "--name-only", "-r", commitHash}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}

	if err := gm.executeGitCommand(args, &stdout, nil); err != nil {
		return nil, nil, fmt.Errorf("failed to get files in commit %s: %w", commitHash, err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	files := make([]string, 0, len(lines))
	metadataFiles := make(map[string]struct{})
	metadatasPaths := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ".shadowfs-metadata/") {
			metadataFiles[line] = struct{}{}
			continue
		}
		files = append(files, line)
		metadatasPaths = append(metadatasPaths, filepath.Join(".shadowfs-metadata", line+".json"))
	}

	args = []string{"ls-tree", "--name-only", "-r", commitHash, "--"}
	args = append(args, metadatasPaths...)
	if err := gm.executeGitCommand(args, &stdout, nil); err != nil {
		return nil, nil, fmt.Errorf("failed to get metadata in commit %s: %w", commitHash, err)
	}

	lines = strings.Split(strings.TrimSpace(stdout.String()), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		metadataFiles[line] = struct{}{}
	}

	medatadas := make([]string, 0, len(metadataFiles))
	for file := range metadataFiles {
		medatadas = append(medatadas, file)
	}

	return files, medatadas, nil
}

// restoreFileFromCommit restores a single file from a specific commit
func (gm *GitManager) restoreFileFromCommit(commitHash, relativePath string) error {
	Debug("restoreFileFromCommit: Restoring file %s from commit %s", relativePath, commitHash)

	// Handle conflicts before restoring
	Debug("restoreFileFromCommit: Checking for conflicts")
	if err := gm.handleRestoreConflicts(relativePath); err != nil {
		Warn("restoreFileFromCommit: Conflict handling failed for %s: %v (continuing)", relativePath, err)
		// Continue even if conflict handling fails
	}

	// Restore content from git directly to cache directory to avoid FUSE deadlocks
	Debug("restoreFileFromCommit: Restoring file content from Git")
	cacheDir, err := gm.getCachePath()
	if err != nil {
		return fmt.Errorf("failed to get cache path: %w", err)
	}
	Debug("restoreFileFromCommit: Cache directory: %s", cacheDir)

	cacheFilePath := filepath.Join(cacheDir, relativePath)
	mountPointPath := filepath.Join(gm.workspacePath, relativePath)

	Debug("restoreFileFromCommit: Writing file to mount: %s", cacheFilePath)
	parentDir := filepath.Dir(cacheFilePath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("failed to create parent directory: %w", err)
	}

	f, err := os.OpenFile(cacheFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open cache file: %w", err)
	}
	defer f.Close()

	// Use git show to get file content and write directly to cache
	Debug("restoreFileFromCommit: Executing git show for %s:%s", commitHash, relativePath)
	args := []string{"show", commitHash + ":" + relativePath}
	if err := gm.executeGitCommand(args, f, nil); err != nil {
		return fmt.Errorf("failed to get file content from commit %s: %w", commitHash, err)
	}
	f.Sync()
	f.Close()

	timestamp, err := gm.GetCommitTimestamp(commitHash, relativePath)
	if err != nil {
		return fmt.Errorf("failed to get commit timestamp: %w", err)
	}

	if err := os.Chtimes(cacheFilePath, timestamp, timestamp); err != nil {
		Debug("restoreFileFromCommit: Failed to update file times for %s: %v (continuing)", cacheFilePath, err)
	}

	// update for mount point
	if err := os.Chtimes(mountPointPath, timestamp, timestamp); err != nil {
		Debug("restoreFileFromCommit: Failed to update file times for mount point %s: %v (continuing)", mountPointPath, err)
	}

	// Mark file as dirty via IPC client if available (for FUSE cache invalidation)
	// This ensures subsequent reads use DIRECT_IO instead of stale cached content
	if gm.ipcClient != nil {
		if err := gm.ipcClient.MarkDirty(mountPointPath); err != nil {
			Debug("restoreFileFromCommit: Failed to mark file as dirty via IPC: %v (continuing)", err)
		} else {
			Debug("restoreFileFromCommit: Marked %s as dirty for FUSE cache invalidation", mountPointPath)
		}
	} else {
		Debug("restoreFileFromCommit: IPC client not available (FUSE process not running), file may have stale cache until next read/write")
	}

	Debug("restoreFileFromCommit: Successfully restored file %s", relativePath)
	return nil
}

// restoreMetadataFromCommit restores metadata and applies xattr for files changed in a commit
func (gm *GitManager) restoreMetadataFromCommit(commitHash string, relativePath string) error {
	relativeMetadataPath, metadata, err := gm.loadXAttrMetadataFromCommit(commitHash, relativePath)

	if err != nil {
		return fmt.Errorf("failed to load metadata: %w", err)
	}

	if metadata == nil {
		return fmt.Errorf("no metadata found for %s at commit %s", relativePath, commitHash)
	}

	// store it in the cache
	cacheDir, err := gm.getCachePath()
	if err != nil {
		return fmt.Errorf("failed to get cache path: %w", err)
	}

	cacheMetadataPath := filepath.Join(cacheDir, relativeMetadataPath)
	if err = gm.storeXAttrMetadata(cacheMetadataPath, *metadata); err != nil {
		return fmt.Errorf("failed to store metadata: %w", err)
	}

	// get timestamp
	timestamp, err := gm.GetCommitTimestamp(commitHash, relativeMetadataPath)
	if err != nil {
		return fmt.Errorf("failed to get commit timestamp: %w", err)
	}

	if err := os.Chtimes(cacheMetadataPath, timestamp, timestamp); err != nil {
		return fmt.Errorf("failed to update file times for %s: %w", cacheMetadataPath, err)
	}

	// apply xattr
	if gm.xattrMgr != nil {
		cachePath := filepath.Join(cacheDir, relativePath)
		attr := &xattr.XAttr{}

		// Set rename mapping if present
		if metadata.RenamedFrom != "" {
			attr.SetRenamedFromPath(metadata.RenamedFrom)
		}

		attr.PathStatus = xattr.PathStatusDeleted

		// Set independence flag
		attr.CacheIndependent = metadata.CacheIndependent

		// Only set xattr if file exists
		if _, err := os.Stat(cachePath); err == nil {
			if errno := xattr.Set(cachePath, attr); errno != 0 {
				Warn("restoreMetadataFromCommit: Failed to set xattr for %s: %v (continuing)", cachePath, errno)
			}
		}
	}

	// update for mount point
	mountPointPath := filepath.Join(gm.workspacePath, relativeMetadataPath)
	if err := os.Chtimes(mountPointPath, timestamp, timestamp); err != nil {
		return fmt.Errorf("failed to update file times for mount point %s: %w", mountPointPath, err)
	}

	return nil
}

// applyCommit applies a single commit (content + metadata + xattr)
func (gm *GitManager) applyCommit(commitHash string, paths []string) error {
	// Get files changed in this commit
	files, metadatas, err := gm.getFilesInCommit(commitHash, paths)
	if err != nil {
		return fmt.Errorf("failed to get files changed in commit %s: %w", commitHash, err)
	}

	// Restore content for each file
	for _, file := range files {
		if err := gm.restoreFileFromCommit(commitHash, file); err != nil {
			Warn("applyCommit: Failed to restore file %s: %v (continuing)", file, err)
			// Continue with other files
		}
	}

	for _, metadata := range metadatas {
		if err := gm.restoreMetadataFromCommit(commitHash, metadata); err != nil {
			Warn("applyCommit: Failed to restore metadata %s: %v (continuing)", metadata, err)
			// Continue even if metadata restoration fails
		}
	}

	return nil
}

// RestoreCommit restores selected files/directories to a specific commit
func (gm *GitManager) RestoreCommit(commitHash string, paths []string) error {

	Debug("RestoreCommit: Starting restore to commit %s for paths: %v", commitHash, paths)

	// Step 1: Flush commit queue
	Debug("RestoreCommit: Step 1 - Flushing commit queue")
	if err := gm.flushCommitQueue(); err != nil {
		return fmt.Errorf("failed to flush commit queue: %w", err)
	}
	Debug("RestoreCommit: Commit queue flushed successfully")

	currentCommit := "HEAD"

	// Step 3: Get commit range
	Debug("RestoreCommit: Step 3 - Getting commit range from %s to %s", commitHash, currentCommit)
	commits, isForward, err := gm.getCommitRange(commitHash, currentCommit)
	if err != nil {
		return fmt.Errorf("failed to get commit range: %w", err)
	}
	Debug("RestoreCommit: Found %d commits in range: %v, forward=%v", len(commits), commits, isForward)

	// Step 5: Apply commits step-by-step (selective)
	Debug("RestoreCommit: Step 5 - Applying commits (selective)")
	Debug("RestoreCommit: Applying commits forward (%d commits)", len(commits))
	for i, commit := range commits {
		Debug("RestoreCommit: Applying commit %d/%d: %s", i+1, len(commits), commit)
		if err := gm.applyCommit(commit, paths); err != nil {
			return fmt.Errorf("failed to apply commit %s: %w", commit, err)
		}
	}

	Debug("RestoreCommit: Successfully completed restore to commit %s", commitHash)
	return nil
}

// detectRenameConflict detects if restoring a path conflicts with an existing rename
func (gm *GitManager) detectRenameConflict(restorePath string) (string, bool) {
	if gm.xattrMgr == nil {
		return "", false
	}

	// Walk cache directory to find paths with rename mappings
	// Check if any path has RenamedFromPath == restorePath
	cachePath := gm.workspacePath
	err := filepath.Walk(cachePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Continue on error
		}

		// Skip metadata directory
		if strings.Contains(path, MetadataDir) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Check xattr for rename mapping
		attr, exists, errno := gm.xattrMgr.GetStatus(path)
		if errno == 0 && exists && attr != nil {
			renamedFrom := attr.GetRenamedFromPath()
			if renamedFrom == restorePath {
				// Found conflict - this path was renamed from restorePath
				relPath, err := filepath.Rel(gm.workspacePath, path)
				if err == nil {
					// Return relative path
					return fmt.Errorf("conflict found: %s", filepath.ToSlash(relPath))
				}
			}
		}

		return nil
	})

	if err != nil {
		// Extract conflict path from error
		errStr := err.Error()
		if strings.HasPrefix(errStr, "conflict found: ") {
			conflictPath := strings.TrimPrefix(errStr, "conflict found: ")
			return conflictPath, true
		}
	}

	return "", false
}

// clearRenameMapping clears the rename mapping for a path
func (gm *GitManager) clearRenameMapping(renamedPath string) error {
	if gm.xattrMgr == nil {
		return fmt.Errorf("xattr manager not available")
	}

	// Use cache directory directly to avoid FUSE deadlocks
	cacheDir, err := gm.getCachePath()
	if err != nil {
		return fmt.Errorf("failed to get cache path: %w", err)
	}
	cachePath := filepath.Join(cacheDir, renamedPath)
	attr, exists, errno := gm.xattrMgr.GetStatus(cachePath)
	if errno != 0 || !exists {
		return fmt.Errorf("failed to get xattr for %s", renamedPath)
	}

	// Clear rename mapping
	attr.SetRenamedFromPath("")
	if errno := xattr.Set(cachePath, attr); errno != 0 {
		return fmt.Errorf("failed to clear rename mapping: %v", errno)
	}

	Debug("clearRenameMapping: Cleared rename mapping for %s", renamedPath)
	return nil
}

// handleRestoreConflicts handles conflicts when restoring paths
func (gm *GitManager) handleRestoreConflicts(restorePath string) error {
	conflictPath, hasConflict := gm.detectRenameConflict(restorePath)
	if hasConflict {
		// Restoring original path = undo rename
		if err := gm.clearRenameMapping(conflictPath); err != nil {
			return fmt.Errorf("failed to clear rename mapping for %s: %w", conflictPath, err)
		}
		Debug("handleRestoreConflicts: Cleared rename mapping for %s (restoring original path %s)", conflictPath, restorePath)
	}
	return nil
}

// getInitialCommit gets the first commit that modified a file
func (gm *GitManager) getInitialCommit(relativePath string) (string, error) {
	var stdout strings.Builder
	args := []string{"log", "--reverse", "--format=%H", "--", relativePath}
	if err := gm.executeGitCommand(args, &stdout, nil); err != nil {
		return "", fmt.Errorf("failed to get initial commit for %s: %w", relativePath, err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) > 0 && lines[0] != "" {
		return strings.TrimSpace(lines[0]), nil
	}

	return "", nil // No commits found
}

// isFileSynced checks if a file has been synced (source changed since initial commit)
func (gm *GitManager) isFileSynced(relativePath string) (bool, error) {
	// Get initial commit for this file
	initialCommit, err := gm.getInitialCommit(relativePath)
	if err != nil {
		return false, fmt.Errorf("failed to get initial commit: %w", err)
	}
	if initialCommit == "" {
		return false, nil // No initial commit, not synced
	}

	// Get content from initial commit
	var initialContent strings.Builder
	args := []string{"show", initialCommit + ":" + relativePath}
	if err := gm.executeGitCommand(args, &initialContent, nil); err != nil {
		// File might not have content in initial commit (only metadata)
		return false, nil
	}

	// Get content from source
	sourcePath := filepath.Join(gm.sourcePath, relativePath)
	sourceData, err := os.ReadFile(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // Source file doesn't exist, not synced
		}
		return false, fmt.Errorf("failed to read source file: %w", err)
	}

	// Compare
	return string(sourceData) != initialContent.String(), nil
}

// listSubdirectoriesAtCommit lists subdirectories in a directory at a specific commit
func (gm *GitManager) listSubdirectoriesAtCommit(commitHash, relativePath string) ([]string, error) {
	var stdout strings.Builder
	// Use full output to distinguish directories from files
	args := []string{"ls-tree", commitHash, "--", relativePath + "/"}
	if err := gm.executeGitCommand(args, &stdout, nil); err != nil {
		return nil, fmt.Errorf("failed to list subdirectories: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	subdirs := make([]string, 0)
	seen := make(map[string]bool)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Parse git ls-tree output: <mode> <type> <hash> <path>
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		fileType := parts[1]
		filePath := strings.Join(parts[3:], " ") // Handle paths with spaces

		// Skip metadata directory
		if strings.HasPrefix(filePath, ".shadowfs-metadata/") {
			continue
		}

		// Only include directories (tree type)
		if fileType == "tree" && strings.HasPrefix(filePath, relativePath+"/") {
			relPath := strings.TrimPrefix(filePath, relativePath+"/")
			parts := strings.Split(relPath, "/")
			if len(parts) > 0 {
				subdirName := parts[0]
				subdirPath := filepath.Join(relativePath, subdirName)
				if !seen[subdirPath] {
					subdirs = append(subdirs, subdirPath)
					seen[subdirPath] = true
				}
			}
		}
	}

	return subdirs, nil
}

// GetGitRepository creates a GitManager for an existing mount point
func GetGitRepository(mountPoint string) (*GitManager, error) {
	Debug("GetGitRepository: Starting, mountPoint=%s", mountPoint)

	// Normalize mount point
	Debug("GetGitRepository: Normalizing mount point")
	normalizedMountPoint, err := rootinit.GetMountPoint(mountPoint)
	if err != nil {
		return nil, fmt.Errorf("invalid mount point: %w", err)
	}
	Debug("GetGitRepository: Normalized mount point: %s", normalizedMountPoint)

	// Find cache directory first (avoids FUSE deadlock)
	// We need the cache directory to access Git repository without going through FUSE
	Debug("GetGitRepository: Finding cache directory")
	cacheDir, err := rootinit.FindCacheDirectory(normalizedMountPoint)
	if err != nil {
		return nil, fmt.Errorf("cannot find cache directory: %w", err)
	}
	Debug("GetGitRepository: Cache directory found: %s", cacheDir)

	// Access Git directory through cache directory, not mount point (avoids FUSE deadlock)
	// Git repository is at .root/.gitofs/.git relative to the session directory
	// (all files in mount point are stored under .root in the cache)
	cachePath := cache.GetCachePath(cacheDir)
	gitDir := filepath.Join(cachePath, GitofsName, ".git")
	Debug("GetGitRepository: Checking for git directory: %s", gitDir)

	// Validate repository instead of just checking if directory exists
	// This ensures the repository is valid and ready for use
	if err := validateGitRepository(gitDir); err != nil {
		return nil, fmt.Errorf("git repository not valid: %w", err)
	}
	Debug("GetGitRepository: Git directory found and validated")

	// Read source directory from .target file
	targetFile := cache.GetTargetFilePath(cacheDir)
	Debug("GetGitRepository: Reading source directory from: %s", targetFile)
	srcDirData, err := os.ReadFile(targetFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read source directory from .target file: %w", err)
	}
	srcDir := strings.TrimSpace(string(srcDirData))
	Debug("GetGitRepository: Source directory: %s", srcDir)

	// Create GitManager (Git is already initialized)
	// Pass mount point as workspacePath, not cacheDir
	// Note: PathResolver and xattrMgr are not available in this context, pass nil
	// This means renamed/deleted file handling won't work for GetGitRepository, but that's OK
	// as it's mainly used for read-only operations like version list/restore
	config := GitConfig{
		AutoCommit: true, // Assume enabled if repo exists
	}
	// Pass cache path directly to avoid FUSE deadlocks during restore operations
	gm := NewGitManager(normalizedMountPoint, srcDir, cachePath, config, nil, nil)
	gm.enabled = true // Mark as enabled
	// Update gitDir to use cache directory path instead of mount point (avoids FUSE deadlock)
	// This way --git-dir points to cache (no FUSE), but -C still points to mount point (for path resolution)
	gm.gitDir = gitDir
	Debug("GetGitRepository: Created GitManager with gitDir=%s (cache), workspacePath=%s (mount), cachePath=%s", gm.gitDir, gm.workspacePath, gm.cachePath)

	// Try to create IPC client if FUSE process is running
	mountID := cache.ComputeMountID(normalizedMountPoint, srcDir)
	socketPath, err := cache.GetSocketPath(mountID)
	if err == nil {
		Debug("GetGitRepository: Checking for IPC socket - mountID=%s, mountPoint=%s, srcDir=%s, socketPath=%s", mountID, normalizedMountPoint, srcDir, socketPath)
		if ipc.SocketExists(socketPath) {
			ipcClient, err := ipc.NewControlClient(socketPath)
			if err == nil {
				gm.SetIPCClient(ipcClient)
				Debug("GetGitRepository: IPC client connected successfully: %s", socketPath)
			} else {
				Debug("GetGitRepository: Failed to create IPC client: %v (continuing without IPC)", err)
			}
		} else {
			Debug("GetGitRepository: IPC socket not available at %s (FUSE process may not be running or socket not accessible, continuing without IPC)", socketPath)
		}
	} else {
		Debug("GetGitRepository: Failed to get socket path: %v (continuing without IPC)", err)
	}

	return gm, nil
}

// cleanupStaleLock checks if a lock file is stale and removes it if so
// A lock file is considered stale if:
// - It's empty (0 bytes)
// - It's older than 1 second (likely left behind by a completed operation)
// Returns:
// - (true, nil) if lock was stale and successfully removed
// - (false, nil) if lock is not stale (has content or is recent) or doesn't exist
// - (false, error) if lock removal failed
func cleanupStaleLock(lockPath string) (bool, error) {
	stat, err := os.Stat(lockPath)
	if err != nil {
		return false, nil // File doesn't exist, nothing to clean up
	}

	// Check if lock file is empty
	if stat.Size() == 0 {
		// Check modification time - if older than 1 second, likely stale
		age := time.Since(stat.ModTime())
		if age > 1*time.Second {
			Debug("cleanupStaleLock: Removing stale lock file: %s (age: %v, size: 0)", lockPath, age)
			if err := os.Remove(lockPath); err != nil {
				return false, fmt.Errorf("failed to remove stale lock file %s: %w", lockPath, err)
			}
			return true, nil // Successfully removed stale lock
		}
	}

	// Lock file is not stale (has content or is recent)
	return false, nil
}

// waitForLockFiles waits for Git lock files to be released
// Checks for common lock files: config.lock, HEAD.lock, index.lock
// First cleans up stale locks, then waits for active locks
// Polls with exponential backoff up to timeout
// Returns error if locks cannot be resolved
func waitForLockFiles(gitDir string, timeout time.Duration) error {
	lockFiles := []string{"config.lock", "HEAD.lock", "index.lock"}

	// First pass: Clean up stale locks immediately
	for _, lockFile := range lockFiles {
		lockPath := filepath.Join(gitDir, lockFile)
		removed, err := cleanupStaleLock(lockPath)
		if err != nil {
			// If cleanup fails, log but continue (might be an active lock)
			Debug("waitForLockFiles: Could not clean up lock %s: %v", lockPath, err)
		} else if removed {
			Debug("waitForLockFiles: Cleaned up stale lock: %s", lockPath)
		}
	}

	// Second pass: Wait for active locks (recently modified, non-empty)
	startTime := time.Now()
	pollInterval := 100 * time.Millisecond
	maxPollInterval := 500 * time.Millisecond

	for {
		allReleased := true
		for _, lockFile := range lockFiles {
			lockPath := filepath.Join(gitDir, lockFile)
			if _, err := os.Stat(lockPath); err == nil {
				// Lock file exists - check if it's stale
				removed, err := cleanupStaleLock(lockPath)
				if err != nil {
					// Cleanup failed, treat as active lock
					allReleased = false
					break
				}
				if removed {
					// Stale lock was cleaned up, continue checking
					continue
				}
				// Lock file is active (has content or is recent)
				allReleased = false
				break
			}
		}

		if allReleased {
			return nil
		}

		if time.Since(startTime) >= timeout {
			// Check for remaining locks
			for _, lockFile := range lockFiles {
				lockPath := filepath.Join(gitDir, lockFile)
				if _, err := os.Stat(lockPath); err == nil {
					// Lock file still exists after timeout
					return fmt.Errorf("git repository locked: %s still exists after %v (Git operation may be in progress or interrupted)", lockFile, timeout)
				}
			}
			return fmt.Errorf("git repository locked: lock files persist after %v", timeout)
		}

		time.Sleep(pollInterval)
		// Exponential backoff
		pollInterval = time.Duration(float64(pollInterval) * 1.5)
		if pollInterval > maxPollInterval {
			pollInterval = maxPollInterval
		}
	}
}

// validateGitRepository validates that a Git repository is valid and ready
// Checks if .git directory exists, HEAD file exists, and uses git rev-parse to verify
// Waits for lock files to be released if they exist
func validateGitRepository(gitDir string) error {
	// Check if .git directory exists
	if _, err := os.Stat(gitDir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("git repository directory not found: %s", gitDir)
		}
		return fmt.Errorf("cannot access git repository directory: %w", err)
	}

	// Check if HEAD file exists (minimum requirement for valid Git repo)
	headFile := filepath.Join(gitDir, "HEAD")
	if _, err := os.Stat(headFile); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("invalid git repository: HEAD file not found in %s (repository may not be fully initialized)", gitDir)
		}
		return fmt.Errorf("cannot access HEAD file: %w", err)
	}

	// Wait for lock files to be released (with timeout)
	if err := waitForLockFiles(gitDir, 5*time.Second); err != nil {
		return fmt.Errorf("git repository locked: %w", err)
	}

	// Use git rev-parse --git-dir to verify Git recognizes the repository
	// Run from the parent directory (repository root), not from .git directory
	repoRoot := filepath.Dir(gitDir)
	cmd := exec.Command("git", "--git-dir", gitDir, "rev-parse", "--git-dir")
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// If git rev-parse fails, the repository is not valid
		return fmt.Errorf("git repository not recognized by Git: %v (stderr: %s). Repository may be corrupted or not fully initialized", err, stderr.String())
	}

	return nil
}

// ValidateGitRepository checks if a Git repository exists and is valid for a mount point
func ValidateGitRepository(mountPoint string) error {
	// Normalize mount point
	normalizedMountPoint, err := rootinit.GetMountPoint(mountPoint)
	if err != nil {
		return fmt.Errorf("invalid mount point: %w", err)
	}

	// git init creates .gitofs/.git/ in mount point, so check for .gitofs/.git
	gitDir := filepath.Join(normalizedMountPoint, ".gitofs", ".git")
	return validateGitRepository(gitDir)
}

// ExpandGlobPatterns expands glob patterns and returns a list of matching files
// relative to the workspace root. Handles both glob patterns and regular paths.
// Note: `**` patterns (recursive globbing) are passed directly to Git without expansion,
// as Go's filepath.Glob doesn't support them, but Git does.
func ExpandGlobPatterns(workspacePath string, patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}

	var expandedPaths []string
	seen := make(map[string]bool) // Deduplicate paths

	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}

		// Check if pattern contains recursive glob (**) - pass directly to Git
		if strings.Contains(pattern, "**") {
			normalized := filepath.ToSlash(pattern)
			if !seen[normalized] {
				expandedPaths = append(expandedPaths, normalized)
				seen[normalized] = true
			}
			continue
		}

		// Check if pattern contains glob characters
		hasGlob := strings.Contains(pattern, "*") || strings.Contains(pattern, "?") || strings.Contains(pattern, "[")

		if hasGlob {
			// Expand glob pattern relative to workspace
			matches, err := filepath.Glob(filepath.Join(workspacePath, pattern))
			if err != nil {
				return nil, fmt.Errorf("invalid glob pattern %s: %w", pattern, err)
			}

			// Convert absolute paths to relative paths
			for _, match := range matches {
				relPath, err := filepath.Rel(workspacePath, match)
				if err != nil {
					continue // Skip if can't make relative
				}

				// Normalize path separators
				relPath = filepath.ToSlash(relPath)
				if !seen[relPath] {
					expandedPaths = append(expandedPaths, relPath)
					seen[relPath] = true
				}
			}
		} else {
			// Regular path - validate it exists and make it relative
			fullPath := filepath.Join(workspacePath, pattern)
			if _, err := os.Stat(fullPath); err == nil {
				relPath, err := filepath.Rel(workspacePath, fullPath)
				if err == nil {
					relPath = filepath.ToSlash(relPath)
					if !seen[relPath] {
						expandedPaths = append(expandedPaths, relPath)
						seen[relPath] = true
					}
				}
			}
			// If path doesn't exist, still add it (Git will handle it)
			// This allows filtering by paths that existed in history but not now
			normalized := filepath.ToSlash(pattern)
			if !seen[normalized] {
				expandedPaths = append(expandedPaths, normalized)
				seen[normalized] = true
			}
		}
	}

	return expandedPaths, nil
}
