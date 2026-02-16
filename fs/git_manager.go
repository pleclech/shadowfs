package fs

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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

// GitOperation represents a queued git operation
type GitOperation struct {
	Name     string
	Function func() error
}

// PendingCommit tracks files waiting for idle timeout
type PendingCommit struct {
	filePath string
	lastSeen time.Time
	reason   string
}

// GitManager handles automatic Git operations for overlay workspace
type GitManager struct {
	workspacePath   string
	sourcePath      string
	cachePath       string // Cache directory path (.root) - set when available to avoid FUSE deadlocks
	gitDir          string
	config          GitConfig
	commitQueue     chan CommitRequest
	wg              sync.WaitGroup
	stopChan        chan struct{}
	once            sync.Once
	pathResolver    *PathResolver  // Optional: for resolving renamed paths
	xattrMgr        *xattr.Manager // Optional: for checking file status (deleted, independent)
	userConfigMutex sync.Mutex     // Mutex for thread-safe user configuration

	// Auto-commit pause mechanism
	pendingMutex sync.Mutex // Protects paused state and pending commits
	pauseMutex   sync.Mutex // Separate mutex for pause state (reentrant-safe)
	pauseCount   int        // Reference count for reentrant pauses

	// Resource monitoring metrics
	metrics struct {
		operationsQueued  int64
		operationsDropped int64
		queueOverflows    int64
		bufferOverflows   int64
		maxQueueSize      int
		maxBufferSize     int
	}

	// Unified Queue Architecture
	gitOperationQueue chan GitOperation // Single queue for all git operations
	pausedBuffer      []GitOperation    // Buffer for operations during pause
	resumeChan        chan struct{}     // Signal for resume events
	pendingCommits    []PendingCommit   // Ordered list of pending commits (Natural Order)
	checkingIdle      int32             // Atomic flag to prevent concurrent idle checks

	markDirty   func(path string) error
	refresh     func(path string) error
	restoreFile func(path string) error // Uses RESTORE_FILE IPC command for restored files

	// IPC client for marking files as dirty (for FUSE cache invalidation)
	// ipcClient interface {
	// 	MarkDirty(path string) error
	// 	Close() error
	// }

	enabled        bool
	userConfigured bool // Cache flag to avoid repeated Git config checks
	paused         bool // Whether auto-commit is paused
}

// GitConfig contains configuration for Git operations
type GitConfig struct {
	IdleTimeout  time.Duration
	SafetyWindow time.Duration // Delay after last write before committing (default: 5s)
	AutoCommit   bool

	// Robustness settings
	MaxQueueSize    int           `json:"max_queue_size"`    // Maximum queue size (default: 10000)
	QueueTimeout    time.Duration `json:"queue_timeout"`     // Timeout for queue operations (default: 10s)
	MaxPausedBuffer int           `json:"max_paused_buffer"` // Maximum pause buffer size (default: 5000)
	EnableMetrics   bool          `json:"enable_metrics"`    // Enable resource monitoring (default: true)
}

// NewGitManager creates a new GitManager instance
// cachePath is the cache directory path (.root) - if provided, avoids repeated lookups and FUSE deadlocks
// If empty, cachePath will be resolved lazily via getCachePath() when needed
func NewGitManager(workspacePath, sourcePath, cachePath string, config GitConfig, pathResolver *PathResolver, xattrMgr *xattr.Manager) *GitManager {
	// Set defaults if not provided
	if config.MaxQueueSize == 0 {
		config.MaxQueueSize = 10000
	}
	if config.MaxPausedBuffer == 0 {
		config.MaxPausedBuffer = 5000
	}

	// gitDir points to the actual .git directory (created by git init)
	// git init .gitofs creates .gitofs/.git/ in the mount point
	// From mount point view: {workspacePath}/.gitofs/.git
	gitDir := filepath.Join(cachePath, GitofsName, ".git")
	gm := &GitManager{
		workspacePath:     workspacePath,
		sourcePath:        sourcePath,
		cachePath:         cachePath, // Set cache path directly if provided
		gitDir:            gitDir,
		enabled:           config.AutoCommit,
		config:            config,
		commitQueue:       make(chan CommitRequest, 100), // Buffer up to 100 commit requests
		stopChan:          make(chan struct{}),
		pathResolver:      pathResolver,
		xattrMgr:          xattrMgr,
		gitOperationQueue: make(chan GitOperation, config.MaxQueueSize),
		resumeChan:        make(chan struct{}, 1),
	}

	// Start background commit processor if enabled
	if config.AutoCommit {
		gm.startCommitProcessor()
		gm.startGitOperationProcessor()
	}

	return gm
}

func (gm *GitManager) SetMarkDirtyFunc(markDirty func(path string) error) {
	gm.markDirty = markDirty
}

func (gm *GitManager) SetRefreshFunc(refresh func(path string) error) {
	gm.refresh = refresh
}

func (gm *GitManager) SetRestoreFileFunc(restoreFile func(path string) error) {
	gm.restoreFile = restoreFile
}

// SetIPCClient sets the IPC client for marking files as dirty
// This allows GitManager to communicate with FUSE process when files are restored/modified
// outside of the normal FUSE write path (e.g., during restore operations)
// func (gm *GitManager) SetIPCClient(client interface {
// 	MarkDirty(path string) error
// 	Close() error
// }) {
// 	gm.ipcClient = client
// }

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

// startGitOperationProcessor starts the unified queue processor
func (gm *GitManager) startGitOperationProcessor() {
	gm.wg.Add(1)
	go func() {
		defer gm.wg.Done()

		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				// Check idle timeouts for ALL pending commits
				if !gm.IsAutoCommitPaused() {
					// Run in separate goroutine to prevent blocking the consumer loop
					// Use atomic guard to ensure only one check runs at a time
					if atomic.CompareAndSwapInt32(&gm.checkingIdle, 0, 1) {
						go func() {
							defer atomic.StoreInt32(&gm.checkingIdle, 0)
							gm.checkIdleTimeouts()
						}()
					}
				}

			case operation := <-gm.gitOperationQueue:
				// Check pause state once per operation
				if gm.IsAutoCommitPaused() {
					// Add to paused buffer (maintains order) - bounded
					if len(gm.pausedBuffer) < gm.config.MaxPausedBuffer {
						gm.pausedBuffer = append(gm.pausedBuffer, operation)
						Debug("OperationProcessor: Buffered %s (buffer: %d/%d)",
							operation.Name, len(gm.pausedBuffer), gm.config.MaxPausedBuffer)
					} else {
						// Buffer full - apply backpressure
						gm.metrics.bufferOverflows++
						Warn("OperationProcessor: Pause buffer full, dropping %s (buffer: %d/%d)",
							operation.Name, len(gm.pausedBuffer), gm.config.MaxPausedBuffer)
					}
				} else {
					// Execute immediately
					gm.executeOperation(operation)
				}

			case <-gm.resumeChan:
				// Process buffered operations in order
				// 1. Execute buffer
				for _, op := range gm.pausedBuffer {
					gm.executeOperation(op)
				}
				gm.pausedBuffer = nil

				// 2. Check pending commits (Catch up on idle files)
				// Same guard pattern as ticker
				if atomic.CompareAndSwapInt32(&gm.checkingIdle, 0, 1) {
					go func() {
						defer atomic.StoreInt32(&gm.checkingIdle, 0)
						gm.checkIdleTimeouts()
					}()
				}

			case <-gm.stopChan:
				return
			}
		}
	}()
}

func (gm *GitManager) executeOperation(operation GitOperation) {
	if err := operation.Function(); err != nil {
		Warn("Git operation %s failed: %v", operation.Name, err)
	}
}

func (gm *GitManager) checkIdleTimeouts() {
	gm.pendingMutex.Lock()
	defer gm.pendingMutex.Unlock()

	now := time.Now()
	cutIndex := 0

	// Check from oldest to newest
	for i, p := range gm.pendingCommits {
		if now.Sub(p.lastSeen) >= gm.config.IdleTimeout {
			// READY: Queue it
			path := p.filePath
			reason := p.reason

			gm.QueueGitOperation("auto-commit", func() error {
				return gm.autoCommitFileSync(path, reason, "auto-save")
			})

			cutIndex = i + 1
		} else {
			// OPTIMIZATION: First non-ready file found.
			break
		}
	}

	// Remove processed items
	if cutIndex > 0 {
		gm.pendingCommits = gm.pendingCommits[cutIndex:]
	}
}

func (gm *GitManager) QueueGitOperation(name string, fn func() error) error {
	operation := GitOperation{Name: name, Function: fn}

	// Track metrics
	gm.metrics.operationsQueued++

	// CRITICAL: Never drop operations - block until space is available
	// This ensures all file changes are eventually committed
	Debug("QueueGitOperation: Attempting to queue %s (queue size: %d/%d)",
		name, len(gm.gitOperationQueue), cap(gm.gitOperationQueue))

	// Block until operation is queued (no timeout, no dropping)
	gm.gitOperationQueue <- operation

	Debug("QueueGitOperation: Successfully queued %s (queue size: %d/%d)",
		name, len(gm.gitOperationQueue), cap(gm.gitOperationQueue))
	return nil
}

func (gm *GitManager) QueueRenameMetadata(sourcePath, destPath string) {
	gm.queueAllPendingCommits()
	gm.QueueGitOperation("rename-metadata", func() error {
		return gm.commitRenameMetadata(sourcePath, destPath)
	})
}

func (gm *GitManager) QueueAutoCommit(filePath, reason string) {
	gm.QueueGitOperation("auto-commit", func() error {
		return gm.autoCommitFileSync(filePath, reason, "auto-save")
	})
}

func (gm *GitManager) QueueDelete(relativePath string) {
	gm.QueueGitOperation("delete", func() error {
		return gm.performDeletion(relativePath)
	})
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

// RunGitWithWorktreeOverride runs a git command with a temporary worktree override
// This is useful when we need to stage files from outside the normal worktree
func (gm *GitManager) RunGitWithWorktreeOverride(worktree string, args ...string) (string, string, error) {
	cmdArgs := []string{"-C", gm.gitDir, "-c", "core.worktree=" + worktree}
	cmdArgs = append(cmdArgs, args...)
	Debug("RunGitWithWorktreeOverride: Running: git %v (from dir: %s)", cmdArgs, worktree)
	cmd := exec.Command("git", cmdArgs...)
	env := os.Environ()
	env = append(env, "SHADOWFS_CACHE_DIR="+gm.cachePath)
	cmd.Env = env
	cmd.Dir = worktree

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
	_, _, err = gm.RunGit("config", "user.email", "shadowfs@localhost")
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

	// Note: Even when paused, we still add to pending queue for idle timeout processing
	// The actual execution will be buffered until resume
	if gm.IsAutoCommitPaused() {
		Debug("AutoCommitFile: Auto-commit paused, buffering commit for %s (reason: %s)", filePath, reason)
	} else {
		Debug("AutoCommitFile: Auto-commit active, queuing commit for %s (reason: %s)", filePath, reason)
	}

	// Use new unified queue logic
	gm.pendingMutex.Lock()
	defer gm.pendingMutex.Unlock()

	// 1. Remove existing entry for this path (if any)
	for i, p := range gm.pendingCommits {
		if p.filePath == filePath {
			// Delete from slice
			gm.pendingCommits = append(gm.pendingCommits[:i], gm.pendingCommits[i+1:]...)
			break
		}
	}

	// 2. Append new entry to the END (maintains time order)
	gm.pendingCommits = append(gm.pendingCommits, PendingCommit{
		filePath: filePath,
		lastSeen: time.Now(),
		reason:   reason,
	})

	Debug("AutoCommitFile: Updated pending for %s (total pending: %d)", filePath, len(gm.pendingCommits))
	return nil
}

// RemoveFromPending removes a file from pending commits (file is no longer idle)
func (gm *GitManager) RemoveFromPending(filePath string) {
	if !gm.enabled {
		return
	}

	gm.pendingMutex.Lock()
	defer gm.pendingMutex.Unlock()

	// Find and remove the file from pending
	for i, p := range gm.pendingCommits {
		if p.filePath == filePath {
			// Remove from slice
			gm.pendingCommits = append(gm.pendingCommits[:i], gm.pendingCommits[i+1:]...)
			Debug("RemoveFromPending: Removed %s (file being written, remaining: %d)", filePath, len(gm.pendingCommits))
			return
		}
	}

	Debug("RemoveFromPending: %s not found in pending (already removed)", filePath)
}

// GetPendingCount returns the number of pending commits (for testing/debugging)
func (gm *GitManager) GetPendingCount() int {
	gm.pendingMutex.Lock()
	defer gm.pendingMutex.Unlock()
	return len(gm.pendingCommits)
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
	return hasCommits
}

// commitOriginalStateSync commits the original state of a file from source synchronously
// This is called immediately after COW to preserve the original state before modifications
// Returns error only if critical failure occurs (non-critical errors are logged)
func (gm *GitManager) commitOriginalStateSync(relativePath string) error {
	// Pause auto-commit during this controlled Git operation to prevent triggering auto-commit
	gm.PauseAutoCommit()
	defer gm.ResumeAutoCommit()

	// Check if file has been committed before (avoid duplicate commits)
	if gm.hasFileBeenCommitted(relativePath) {
		return nil // Already committed, skip
	}

	// Copy file from cache to workspace before staging (file exists in cache, not workspace)
	cacheFilePath := filepath.Join(gm.cachePath, relativePath)

	// Open file fresh to bypass any caching
	freshFile, freshErr := syscall.Open(cacheFilePath, syscall.O_RDONLY, 0)
	var cacheContent []byte
	var readErr error = freshErr
	if freshErr == nil {
		var stat syscall.Stat_t
		if err := syscall.Fstat(freshFile, &stat); err == nil {
			if stat.Size > 0 {
				buf := make([]byte, stat.Size)
				n, err := syscall.Read(freshFile, buf)
				if err == nil && n > 0 {
					cacheContent = buf[:n]
				}
			}
		}
		syscall.Close(freshFile)
	}

	// Since git hash-object seems to read wrong content, let's compute the blob hash directly in Go
	// This bypasses any filesystem or git configuration issues
	var blobHash string
	if readErr == nil && len(cacheContent) > 0 {
		blobContent := fmt.Sprintf("blob %d\x00%s", len(cacheContent), string(cacheContent))
		hh := sha1.New()
		hh.Write([]byte(blobContent))
		blobHash = fmt.Sprintf("%x", hh.Sum(nil))
		Debug("commitOriginalStateSync: Computed blob hash manually for %s: %s (content len=%d)", relativePath, blobHash, len(cacheContent))

		var hashOut strings.Builder
		writeErr := gm.executeGitCommandFromDir([]string{"hash-object", "-w", cacheFilePath}, &hashOut, nil, gm.cachePath)
		if writeErr == nil {
			blobHash = strings.TrimSpace(hashOut.String())
		}
	} else {
		// Fallback to git hash-object
		var hashOut strings.Builder
		hashErr := gm.executeGitCommandFromDir([]string{"hash-object", "-w", relativePath}, &hashOut, nil, gm.cachePath)
		if hashErr != nil {
			return fmt.Errorf("failed to hash object %s: %w", relativePath, hashErr)
		}
		blobHash = strings.TrimSpace(hashOut.String())
		Debug("commitOriginalStateSync: Fallback to git hash-object for %s: %s", relativePath, blobHash)
	}

	// Get file mode
	var stat syscall.Stat_t
	if err := syscall.Lstat(cacheFilePath, &stat); err != nil {
		return fmt.Errorf("failed to stat cache file %s: %w", relativePath, err)
	}
	mode := stat.Mode & 0777
	if mode == 0 {
		mode = 0644
	}

	// Use update-index to add directly to index
	var addStderr strings.Builder
	addErr := gm.executeGitCommandFromDir([]string{"update-index", "--add", "--cacheinfo", fmt.Sprintf("0%o,%s,%s", mode, blobHash, relativePath)}, nil, &addStderr, gm.cachePath)
	if addErr != nil {
		return fmt.Errorf("failed to update-index for %s: %w (stderr: %s)", relativePath, addErr, addStderr.String())
	}

	// Debug: Check what's actually in the index
	var lsOut strings.Builder
	gm.executeGitCommandFromDir([]string{"ls-files", "-s", "--", relativePath}, &lsOut, nil, gm.cachePath)
	Debug("commitOriginalStateSync: Git ls-files -s: %s", lsOut.String())

	// Debug: Check git status to see what's staged
	var statusOut strings.Builder
	gm.executeGitCommandFromDir([]string{"status", "--porcelain"}, &statusOut, nil, gm.cachePath)
	Debug("commitOriginalStateSync: Git status after add: %s", statusOut.String())

	Debug("commitOriginalStateSync: Staged original state file %s", relativePath)

	// Commit with message indicating it's the original state
	// Run from cache directory to bypass worktree issues, also unset worktree config
	commitMsg := "Initial state: " + relativePath
	var commitStderr strings.Builder
	commitErr := gm.executeGitCommandFromDir([]string{"-c", "core.worktree=", "commit", "-m", commitMsg}, nil, &commitStderr, gm.cachePath)
	if commitErr != nil {
		// Check if commit failed because there are no changes
		if strings.Contains(commitStderr.String(), "nothing to commit") {
			Debug("commitOriginalStateSync: Nothing to commit for original state of %s", relativePath)
			return nil // Not an error, just no changes
		}
		return fmt.Errorf("failed to commit original state file %s: %w (stderr: %s)", relativePath, commitErr, commitStderr.String())
	}
	Debug("commitOriginalStateSync: Successfully committed original state file %s", relativePath)

	return nil
}

// performDeletion commits a file or directory deletion
func (gm *GitManager) performDeletion(relativePath string) error {
	// Original commitDeletion logic without flush/pause

	// Skip ignore check - git will handle ignored files automatically

	// Check if file is independent
	isIndependent := false
	if gm.xattrMgr != nil {
		// During delete, file exists in cache, so check cache path first
		// Then fallback to workspace path for normal operations
		cacheFilePath := filepath.Join(gm.cachePath, relativePath)
		attr, exists, errno := gm.xattrMgr.GetStatus(cacheFilePath)
		if errno == 0 && exists && attr != nil {
			isIndependent = attr.CacheIndependent
		} else {
			// Fallback to workspace path for files that exist there
			workspaceFilePath := filepath.Join(gm.workspacePath, relativePath)
			attr, exists, errno = gm.xattrMgr.GetStatus(workspaceFilePath)
			if errno == 0 && exists && attr != nil {
				isIndependent = attr.CacheIndependent
			}
		}
	}

	// Always commit current state before deletion if not already committed
	// This ensures complete history for restoration regardless of independence
	if !gm.hasFileBeenCommitted(relativePath) {
		Debug("commitDeletion: Path %s not committed, committing current state before deletion", relativePath)
		if err := gm.commitOriginalStateSync(relativePath); err != nil {
			Warn("commitDeletion: Failed to commit current state for %s: %v (continuing)", relativePath, err)
		}
	}

	// Check if file is tracked in git index
	_, _, err := gm.RunGit("ls-files", "--error-unmatch", "--", relativePath)
	if err != nil {
		Debug("commitDeletion: Path %s is not tracked in git, skipping deletion commit", relativePath)
		return nil
	}

	// Stage the deletion
	sOut, _, err := gm.RunGit("ls-files", "--directory", "--", relativePath+"/")
	isDir := err == nil && len(sOut) > 0

	if isDir {
		_, _, err = gm.RunGit("rm", "-r", "--cached", "--ignore-unmatch", relativePath)
		if err != nil {
			return fmt.Errorf("failed to stage directory deletion for %s: %w", relativePath, err)
		}
	} else {
		_, _, err = gm.RunGit("rm", "--cached", "--ignore-unmatch", relativePath)
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
			if relPath, err := filepath.Rel(gm.sourcePath, resolvedSourcePath); err == nil {
				sourcePath = filepath.ToSlash(relPath)
			}
		}
	}

	// Store metadata
	metadata := Metadata{
		Deleted:          true,
		SourcePath:       sourcePath,
		CacheIndependent: isIndependent,
	}

	// Stage metadata file
	metadataRelativePath := filepath.Join(".shadowfs-metadata", relativePath+".json")

	// Write metadata using absolute path in cache
	metadataAbsolutePath := filepath.Join(gm.workspacePath, metadataRelativePath)
	if err := gm.storeXAttrMetadata(metadataAbsolutePath, metadata); err != nil {
		Warn("commitDeletion: Failed to store metadata for %s: %v (continuing)", relativePath, err)
	}

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

// commitDeletion commits a file or directory deletion
func (gm *GitManager) commitDeletion(relativePath string) error {
	if !gm.enabled {
		return nil // Git disabled, skip
	}

	// 1. Pause (Stop new additions)
	gm.PauseAutoCommit()
	defer gm.ResumeAutoCommit()

	// 2. Queue all pending commits immediately
	gm.queueAllPendingCommits()

	// 3. Queue Delete (Preserve order)
	gm.QueueDelete(relativePath)

	return nil
}

func (gm *GitManager) queueAllPendingCommits() {
	gm.pendingMutex.Lock()
	// Steal whole list
	pending := gm.pendingCommits
	gm.pendingCommits = nil
	gm.pendingMutex.Unlock()

	// Queue them for execution
	for _, p := range pending {
		gm.QueueAutoCommit(p.filePath, p.reason)
	}
}

// commitRenameMetadata commits metadata for a rename operation
// This is the implementation used by QueueRenameMetadata, executed by the processor
func (gm *GitManager) commitRenameMetadata(sourcePath, destPath string) error {
	Debug("commitRenameMetadata: Starting for %s -> %s", sourcePath, destPath)

	// Note: Flush was already done before queuing in Rename operation

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
		// During rename, file exists in cache, so check cache path first
		// Then fallback to workspace path for normal operations
		cacheFilePath := filepath.Join(gm.cachePath, destRelative)
		attr, exists, errno := gm.xattrMgr.GetStatus(cacheFilePath)
		if errno == 0 && exists && attr != nil {
			isIndependent = attr.CacheIndependent
		} else {
			// Fallback to workspace path for files that exist there
			workspaceFilePath := filepath.Join(gm.workspacePath, destRelative)
			attr, exists, errno = gm.xattrMgr.GetStatus(workspaceFilePath)
			if errno == 0 && exists && attr != nil {
				isIndependent = attr.CacheIndependent
			}
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

	// Stage metadata file
	metadataRelativePath := filepath.Join(".shadowfs-metadata", destRelative+".json")

	// Write metadata using absolute path in cache
	metadataAbsolutePath := filepath.Join(gm.workspacePath, metadataRelativePath)
	if err := gm.storeXAttrMetadata(metadataAbsolutePath, metadata); err != nil {
		Warn("commitRenameMetadata: Failed to store metadata for %s: %v (continuing)", destRelative, err)
		// Continue even if metadata storage fails
	}

	_, _, err := gm.RunGit("add", metadataRelativePath)
	if err != nil {
		return fmt.Errorf("failed to stage metadata file %s: %w", metadataRelativePath, err)
	}

	// Commit the rename metadata
	commitMsg := fmt.Sprintf("Rename: %s → %s", sourceRelative, destRelative)
	Debug("commitRenameMetadata: Committing with message: %s", commitMsg)
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

// performDirectoryDeletionRecursive commits a directory deletion recursively
func (gm *GitManager) performDirectoryDeletionRecursive(relativePath string) error {
	// Original logic from commitDirectoryDeletionRecursive without flush

	// Walk the SOURCE directory (not workspace/mount point) to find all files
	// This is safe because sourcePath is a real filesystem path, not a FUSE mount
	sourceDirPath := filepath.Join(gm.sourcePath, relativePath)

	var filesToCommit []string
	var dirsToCommit []string

	// Walk source directory to find all files
	err := filepath.Walk(sourceDirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Continue on error
		}

		// Convert to relative path from source root
		relPath, err := filepath.Rel(gm.sourcePath, path)
		if err != nil {
			return nil
		}

		// Normalize path separators
		relPath = filepath.ToSlash(relPath)

		// Skip if not under the target directory
		if !strings.HasPrefix(relPath, relativePath+"/") && relPath != relativePath {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
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
		Warn("performDirectoryDeletionRecursive: Error walking source directory %s: %v (continuing)", sourceDirPath, err)
	}

	// Also get tracked files from git
	sOut, _, err := gm.RunGit("ls-files", relativePath+"/")
	if err != nil {
		sOut, _, err = gm.RunGit("ls-files", relativePath)
	}

	if err == nil && len(sOut) > 0 {
		lines := strings.Split(strings.TrimSpace(sOut), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(filepath.ToSlash(line))
			if line == "" || line == relativePath {
				continue
			}
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

	// Commit original states
	for _, filePath := range filesToCommit {
		isIndependent := false
		if gm.xattrMgr != nil {
			workspaceFilePath := filepath.Join(gm.workspacePath, filePath)
			attr, exists, errno := gm.xattrMgr.GetStatus(workspaceFilePath)
			if errno == 0 && exists && attr != nil {
				isIndependent = attr.CacheIndependent
			}
		}

		if !isIndependent && !gm.hasFileBeenCommitted(filePath) {
			Debug("performDirectoryDeletionRecursive: Committing original state for %s", filePath)
			if err := gm.commitOriginalStateSync(filePath); err != nil {
				Warn("performDirectoryDeletionRecursive: Failed to commit original state for %s: %v (continuing)", filePath, err)
			}
		}
	}

	// Stage file deletions
	for _, filePath := range filesToCommit {
		_, _, err := gm.RunGit("rm", "--cached", "--ignore-unmatch", filePath)
		if err != nil {
			Warn("performDirectoryDeletionRecursive: Failed to stage file deletion %s: %v (continuing)", filePath, err)
		}
	}

	// Stage directory deletions
	for i := len(dirsToCommit) - 1; i >= 0; i-- {
		dirPath := dirsToCommit[i]
		_, _, err := gm.RunGit("rm", "-r", "--cached", "--ignore-unmatch", dirPath)
		if err != nil {
			Warn("performDirectoryDeletionRecursive: Failed to stage directory deletion %s: %v (continuing)", dirPath, err)
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
		CacheIndependent: false,
	}

	// Stage metadata file
	metadataRelativePath := filepath.Join(".shadowfs-metadata", relativePath+".json")

	// Write metadata using absolute path in cache
	metadataAbsolutePath := filepath.Join(gm.workspacePath, metadataRelativePath)
	if err := gm.storeXAttrMetadata(metadataAbsolutePath, metadata); err != nil {
		Warn("performDirectoryDeletionRecursive: Failed to store metadata for %s: %v (continuing)", relativePath, err)
	}

	_, _, err = gm.RunGit("add", metadataRelativePath)
	if err != nil {
		return fmt.Errorf("failed to stage metadata file %s: %w", metadataRelativePath, err)
	}

	// Commit all deletions together
	fileCount := len(filesToCommit)
	dirCount := len(dirsToCommit) + 1
	commitMsg := fmt.Sprintf("Delete directory: %s (%d files, %d dirs)", relativePath, fileCount, dirCount)
	sOut, _, err = gm.RunGit("commit", "-m", commitMsg)
	if err != nil {
		if strings.Contains(sOut, "nothing to commit") {
			Debug("performDirectoryDeletionRecursive: Nothing to commit for deletion of %s", relativePath)
			return nil
		}
		return fmt.Errorf("failed to commit directory deletion for %s: %w", relativePath, err)
	}
	Debug("performDirectoryDeletionRecursive: Successfully committed directory deletion for %s", relativePath)

	return nil
}

// commitDirectoryDeletionRecursive commits a directory deletion recursively
func (gm *GitManager) commitDirectoryDeletionRecursive(relativePath string) error {
	if !gm.enabled {
		return nil // Git disabled, skip
	}

	// 1. Pause (Stop new additions)
	gm.PauseAutoCommit()
	defer gm.ResumeAutoCommit()

	// 2. Queue all pending commits immediately
	gm.queueAllPendingCommits()

	// 3. Queue Delete (Preserve order)
	gm.QueueGitOperation("delete-directory", func() error {
		return gm.performDirectoryDeletionRecursive(relativePath)
	})

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

// PauseAutoCommit pauses the auto-commit system
// During pause, expired timers will queue files instead of committing them
// Reentrant-safe: multiple calls are allowed with reference counting
func (gm *GitManager) PauseAutoCommit() {
	gm.pauseMutex.Lock()
	defer gm.pauseMutex.Unlock()

	gm.pauseCount++
	if gm.pauseCount == 1 {
		// First pause - actually pause the system
		gm.pendingMutex.Lock()
		gm.paused = true
		gm.pendingMutex.Unlock()
		Debug("PauseAutoCommit: Auto-commit paused (count: %d)", gm.pauseCount)
	} else {
		Debug("PauseAutoCommit: Reentrant pause (count: %d)", gm.pauseCount)
	}
}

// ResumeAutoCommit resumes the auto-commit system
// Processes any pending commits first, then resumes normal operation
// Reentrant-safe: only actually resumes when last resume is called
func (gm *GitManager) ResumeAutoCommit() {
	gm.pauseMutex.Lock()
	defer gm.pauseMutex.Unlock()

	if gm.pauseCount == 0 {
		Debug("ResumeAutoCommit: Not paused (count: %d)", gm.pauseCount)
		return
	}

	gm.pauseCount--
	if gm.pauseCount == 0 {
		// Last resume - actually resume the system
		gm.pendingMutex.Lock()
		gm.paused = false
		gm.pendingMutex.Unlock()

		// Signal resume to processor
		select {
		case gm.resumeChan <- struct{}{}:
			Debug("ResumeAutoCommit: Sent resume signal")
		default:
			// Signal already pending
		}
		Debug("ResumeAutoCommit: Auto-commit resumed (count: %d)", gm.pauseCount)
	} else {
		Debug("ResumeAutoCommit: Reentrant resume (count: %d)", gm.pauseCount)
	}
}

// IsAutoCommitPaused returns whether auto-commit is currently paused
func (gm *GitManager) IsAutoCommitPaused() bool {
	gm.pendingMutex.Lock()
	defer gm.pendingMutex.Unlock()
	return gm.paused
}

// GetMetrics returns current resource usage metrics
func (gm *GitManager) GetMetrics() map[string]int64 {
	gm.pendingMutex.Lock()
	defer gm.pendingMutex.Unlock()

	// Update max metrics
	currentQueueSize := len(gm.gitOperationQueue)
	if currentQueueSize > gm.metrics.maxQueueSize {
		gm.metrics.maxQueueSize = currentQueueSize
	}
	currentBufferSize := len(gm.pausedBuffer)
	if currentBufferSize > gm.metrics.maxBufferSize {
		gm.metrics.maxBufferSize = currentBufferSize
	}

	return map[string]int64{
		"operations_queued":   gm.metrics.operationsQueued,
		"operations_dropped":  gm.metrics.operationsDropped,
		"queue_overflows":     gm.metrics.queueOverflows,
		"buffer_overflows":    gm.metrics.bufferOverflows,
		"current_queue_size":  int64(currentQueueSize),
		"max_queue_size":      int64(gm.metrics.maxQueueSize),
		"current_buffer_size": int64(currentBufferSize),
		"max_buffer_size":     int64(gm.metrics.maxBufferSize),
		"current_pending":     int64(len(gm.pendingCommits)),
		"pause_count":         int64(gm.pauseCount),
		"is_paused":           boolToInt(gm.paused),
	}
}

// Helper function to convert bool to int
func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// enqueuePendingCommit adds a file to the pending commit queue during pause
func (gm *GitManager) enqueuePendingCommit(filePath string) {
	gm.pendingMutex.Lock()
	defer gm.pendingMutex.Unlock()

	if gm.paused {
		// Remove existing entry for this path (if any)
		for i, p := range gm.pendingCommits {
			if p.filePath == filePath {
				// Delete from slice
				gm.pendingCommits = append(gm.pendingCommits[:i], gm.pendingCommits[i+1:]...)
				break
			}
		}

		// Append new entry to the END (maintains time order)
		gm.pendingCommits = append(gm.pendingCommits, PendingCommit{
			filePath: filePath,
			lastSeen: time.Now(),
			reason:   "Auto-commit after pause",
		})
		Debug("enqueuePendingCommit: Queued %s for commit after resume", filePath)
	}
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
	return gm.executeGitCommandFromDir(args, stdout, stderr, "")
}

// executeGitCommandFromDir runs a git command, optionally from a different directory.
// If runFromDir is empty, uses gm.workspacePath as the working directory.
// This is useful for commands like cat-file that only need the object database
// and should not access the worktree (which may be a FUSE mount).
func (gm *GitManager) executeGitCommandFromDir(args []string, stdout, stderr io.Writer, runFromDir string) error {
	// Build command with consistent git directory and working directory
	cmdArgs := []string{"--git-dir", gm.gitDir}
	cmdArgs = append(cmdArgs, args...)

	Debug("executeGitCommand: Running git command: git %v", cmdArgs)

	cmd := exec.Command("git", cmdArgs...)

	// Use specified directory if provided, otherwise use workspacePath
	// Empty string means use workspacePath (default behavior)
	// Non-empty string (like "/") will run from that directory to avoid
	// worktree issues with FUSE mounts
	if runFromDir != "" {
		cmd.Dir = runFromDir
		Debug("executeGitCommand: Running from directory: %s", runFromDir)
	} else {
		cmd.Dir = gm.workspacePath
	}

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

			// Filter out known harmless files:
			// - Lock files (*.lock)
			// - Temporary files (tmp_*, *.tmp)
			// - Git internal files in .git/
			// - .shadowfs-metadata temp files
			// - .shadowfs-metadata directory (internal metadata, should not block restore)
			if strings.HasSuffix(filename, ".lock") ||
				strings.HasPrefix(filename, "tmp_") ||
				strings.HasSuffix(filename, ".tmp") ||
				strings.HasPrefix(filename, ".git/") ||
				strings.Contains(filename, "/tmp_obj_") ||
				strings.HasPrefix(filename, ".shadowfs-metadata/") ||
				filename == ".shadowfs-metadata" {
				Debug("StatusPorcelain: Ignoring harmless file: %s", filename)
				continue
			}

			changedFiles = append(changedFiles, filename)
		}
	}

	// Debug: log what we're returning
	if len(changedFiles) > 0 {
		Debug("StatusPorcelain: Uncommitted files: %v", changedFiles)
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
	log.Printf("DEBUG getFilesInCommit: commitHash=%s, paths=%v", commitHash, paths)
	var stdout strings.Builder
	args := []string{"ls-tree", "--name-only", "-r", commitHash}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	log.Printf("DEBUG getFilesInCommit: ls-tree command: git %v", args)
	log.Printf("DEBUG getFilesInCommit: workspacePath: %s", gm.workspacePath)
	log.Printf("DEBUG getFilesInCommit: gitDir: %s", gm.gitDir)

	if err := gm.executeGitCommand(args, &stdout, nil); err != nil {
		return nil, nil, fmt.Errorf("failed to get files in commit %s: %w", commitHash, err)
	}
	log.Printf("DEBUG getFilesInCommit: ls-tree output: %q", stdout.String())

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
	log.Printf("DEBUG: restoreFileFromCommit called: commit=%s, path=%s", commitHash, relativePath)
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

	// CRITICAL: Remove existing file first if it exists
	// This is needed because O_TRUNC keeps existing xattrs (including deletion markers)
	// We must create a fresh file without any xattrs
	if err := os.Remove(cacheFilePath); err != nil && !os.IsNotExist(err) {
		Debug("restoreFileFromCommit: Warning: failed to remove existing file: %v (continuing)", err)
	}

	f, err := os.OpenFile(cacheFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open cache file: %w", err)
	}
	defer f.Close()

	// Use git show instead of git cat-file to read file content from commits
	// git cat-file can return empty content when core.worktree points to a FUSE mount
	// git show doesn't have this issue as it reads directly from the git object database
	log.Printf("DEBUG restoreFileFromCommit: Executing git show for %s:%s", commitHash, relativePath)
	Debug("restoreFileFromCommit: Full git command will be: git --git-dir %s show %s:%s", gm.gitDir, commitHash, relativePath)

	// First check if file exists in commit using ls-tree
	var lsTreeOut strings.Builder
	lsTreeArgs := []string{"ls-tree", "--name-only", "-r", commitHash, "--", relativePath}
	if err := gm.executeGitCommand(lsTreeArgs, &lsTreeOut, nil); err != nil {
		Debug("restoreFileFromCommit: ls-tree error: %v", err)
	} else {
		Debug("restoreFileFromCommit: ls-tree output: %q", lsTreeOut.String())
	}

	// Get file content using ls-tree to get blob hash, then cat-file to read blob
	// This avoids worktree issues by using the blob directly
	log.Printf("DEBUG restoreFileFromCommit: Getting blob hash for %s in commit %s", relativePath, commitHash)

	// First get the blob hash using ls-tree (without --name-only to get the hash)
	var blobHashOut strings.Builder
	blobArgs := []string{"ls-tree", commitHash, "--", relativePath}
	if err := gm.executeGitCommand(blobArgs, &blobHashOut, nil); err != nil {
		Debug("restoreFileFromCommit: ls-tree error: %v", err)
	}

	// Parse the ls-tree output to get the blob hash
	// Format: <mode> <type> <hash> <path>
	blobHashOutStr := strings.TrimSpace(blobHashOut.String())
	log.Printf("DEBUG restoreFileFromCommit: ls-tree raw output: %q", blobHashOutStr)
	fields := strings.Fields(blobHashOutStr)
	log.Printf("DEBUG restoreFileFromCommit: ls-tree fields: %v", fields)
	var blobHash string
	for i, field := range fields {
		// Blob hash is 40 characters (SHA-1)
		if len(field) == 40 {
			blobHash = field
			log.Printf("DEBUG restoreFileFromCommit: Found blob hash: %s from field %d", blobHash, i)
			break
		}
	}

	log.Printf("DEBUG restoreFileFromCommit: Blob hash: %s", blobHash)
	Debug("restoreFileFromCommit: Blob hash from ls-tree: %s, full output: %q", blobHash, blobHashOutStr)

	// If ls-tree didn't find the blob, try git show directly as fallback
	// This handles cases like rename commits where ls-tree might not find the file
	if blobHash == "" {
		log.Printf("DEBUG restoreFileFromCommit: ls-tree found no blob, trying git show directly")
		var showOutput strings.Builder
		var showStderr strings.Builder
		showArgs := []string{"show", fmt.Sprintf("%s:%s", commitHash, relativePath)}
		if err := gm.executeGitCommand(showArgs, &showOutput, &showStderr); err != nil {
			log.Printf("DEBUG restoreFileFromCommit: git show failed: %v, stderr: %s", err, showStderr.String())
			Debug("restoreFileFromCommit: git show failed: %v (this might be expected for some commit types)", err)
			// Try to find the file using git diff-tree to see if it existed at all in this commit
			var diffTreeOut strings.Builder
			diffTreeArgs := []string{"diff-tree", "--no-commit-id", "-r", "--name-only", commitHash}
			if diffErr := gm.executeGitCommand(diffTreeArgs, &diffTreeOut, nil); diffErr == nil {
				log.Printf("DEBUG restoreFileFromCommit: diff-tree output: %q", diffTreeOut.String())
			}
			return fmt.Errorf("could not find file %s in commit %s (ls-tree and git show both failed)", relativePath, commitHash)
		}
		// Use git show output directly as content
		content := showOutput.String()
		log.Printf("DEBUG restoreFileFromCommit: got content via git show: %d bytes", len(content))
		// Write to file
		if _, err := f.WriteString(content); err != nil {
			return fmt.Errorf("failed to write file content: %w", err)
		}
		// Call restoreFile for FUSE cache invalidation
		if gm.restoreFile != nil {
			if err := gm.restoreFile(mountPointPath); err != nil {
				Warn("restoreFileFromCommit: Failed to restore file via IPC: %v (continuing with fallback)", err)
			} else {
				Debug("restoreFileFromCommit: Successfully restored file via IPC for %s", mountPointPath)
				return nil
			}
		}
		return nil
	}

	// Use git show to read file content from commit - more reliable than cat-file
	var stdoutBuf strings.Builder
	var stderrBuf strings.Builder
	showArgs := []string{"show", fmt.Sprintf("%s:%s", commitHash, relativePath)}
	if err := gm.executeGitCommand(showArgs, &stdoutBuf, &stderrBuf); err != nil {
		log.Printf("DEBUG restoreFileFromCommit: cat-file error: %v, stderr: %s", err, stderrBuf.String())
		Debug("restoreFileFromCommit: cat-file error: %v, stderr: %s", err, stderrBuf.String())
		return fmt.Errorf("failed to get file content from blob %s: %w", blobHash, err)
	}

	content := stdoutBuf.String()
	log.Printf("DEBUG restoreFileFromCommit: cat-file output length: %d bytes, content: %q", len(content), content)
	Debug("restoreFileFromCommit: cat-file output length: %d bytes, content: %q", len(content), content)
	// Also log worktree config for debugging
	var wtConfig strings.Builder
	if err := gm.executeGitCommand([]string{"config", "--get", "core.worktree"}, &wtConfig, nil); err == nil {
		Debug("restoreFileFromCommit: worktree config: %s", wtConfig.String())
	}

	// Debug: verify content before writing
	Debug("restoreFileFromCommit: About to write content to cache file: %s", cacheFilePath)
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("failed to write content to cache file: %w", err)
	}

	// Debug: verify content was written by reading back
	f2, err := os.Open(cacheFilePath)
	if err == nil {
		buf := make([]byte, 100)
		n, _ := f2.Read(buf)
		Debug("restoreFileFromCommit: Verified cache file content after write: %q", string(buf[:n]))
		f2.Close()
	}
	f.Sync()
	f.Close()

	// CRITICAL FIX: Remove deletion marker xattr if present
	// When a file is deleted, it's marked with a deletion marker xattr.
	// When restoring, we must remove this marker or FUSE will still show the file as deleted.
	Debug("restoreFileFromCommit: Checking for deletion marker on %s", cacheFilePath)
	var attr xattr.XAttr
	if exists, errno := xattr.Get(cacheFilePath, &attr); errno == 0 && exists {
		if xattr.IsPathDeleted(attr) {
			Debug("restoreFileFromCommit: Found deletion marker, removing from %s", cacheFilePath)
			if remErrno := xattr.Remove(cacheFilePath); remErrno != 0 {
				Debug("restoreFileFromCommit: Failed to remove deletion marker: %v (continuing)", remErrno)
			} else {
				Debug("restoreFileFromCommit: Successfully removed deletion marker from %s", cacheFilePath)
			}
		}
	}

	// CRITICAL FIX: Clear independent flag from rename tracker
	// When a file is deleted, it's marked as "independent" in the rename tracker.
	// When restoring, we must clear this flag or Lookup will treat the file as deleted.
	Debug("restoreFileFromCommit: Clearing independent flag from rename tracker for %s", relativePath)
	if gm.pathResolver != nil && gm.pathResolver.renameTracker != nil {
		gm.pathResolver.renameTracker.RemoveRenameMapping(relativePath)
		Debug("restoreFileFromCommit: Cleared independent flag for %s", relativePath)
	}

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

	// Use restoreFile IPC command which handles both dirty marking AND directory entry invalidation
	// This ensures restored files appear in directory listings
	Debug("restoreFileFromCommit: Calling restoreFile for path: %s", mountPointPath)
	if gm.restoreFile != nil {
		if err := gm.restoreFile(mountPointPath); err != nil {
			Debug("restoreFileFromCommit: Failed to restore file via IPC: %v (continuing with fallback)", err)
			// Fallback to separate markDirty and refresh calls
			Debug("restoreFileFromCommit: Falling back to markDirty + refresh")
			if gm.markDirty != nil {
				if err := gm.markDirty(mountPointPath); err != nil {
					Debug("restoreFileFromCommit: Failed to mark file as dirty: %v (continuing)", err)
				} else {
					Debug("restoreFileFromCommit: Marked %s as dirty for FUSE cache invalidation", mountPointPath)
				}
			}
			if gm.refresh != nil {
				if err := gm.refresh(mountPointPath); err != nil {
					Debug("restoreFileFromCommit: Failed to refresh FUSE cache: %v (continuing)", err)
				} else {
					Debug("restoreFileFromCommit: Refreshed FUSE cache for %s", mountPointPath)
				}
			}
		} else {
			Debug("restoreFileFromCommit: Successfully restored file via IPC for %s", mountPointPath)
		}
	} else {
		Debug("restoreFileFromCommit: restoreFile is nil, using legacy markDirty + refresh")
		// Legacy approach: separate markDirty and refresh calls
		Debug("restoreFileFromCommit: Calling markDirty for path: %s", mountPointPath)
		Debug("restoreFileFromCommit: markDirty func is nil: %v", gm.markDirty == nil)
		if gm.markDirty != nil {
			if err := gm.markDirty(mountPointPath); err != nil {
				Debug("restoreFileFromCommit: Failed to mark file as dirty: %v (continuing)", err)
			} else {
				Debug("restoreFileFromCommit: Marked %s as dirty for FUSE cache invalidation", mountPointPath)
			}
		} else {
			Debug("restoreFileFromCommit: markDirty is nil, skipping")
		}

		// Refresh FUSE cache to ensure the restored file is visible at mount point
		Debug("restoreFileFromCommit: Calling refresh for path: %s", mountPointPath)
		Debug("restoreFileFromCommit: refresh func is nil: %v", gm.refresh == nil)
		if gm.refresh != nil {
			if err := gm.refresh(mountPointPath); err != nil {
				Debug("restoreFileFromCommit: Failed to refresh FUSE cache: %v (continuing)", err)
			} else {
				Debug("restoreFileFromCommit: Refreshed FUSE cache for %s", mountPointPath)
			}
		} else {
			Debug("restoreFileFromCommit: refresh is nil, skipping")
		}
	}

	Debug("restoreFileFromCommit: Successfully restored file %s", relativePath)
	return nil
}

// restoreMetadataFromCommit restores metadata and applies xattr for files changed in a commit
func (gm *GitManager) restoreMetadataFromCommit(commitHash, relativePath string) error {
	relativeMetadataPath, metadata, err := gm.loadXAttrMetadataFromCommit(commitHash, relativePath)

	if err != nil {
		return fmt.Errorf("failed to load metadata: %w", err)
	}

	if metadata == nil {
		Debug("no metadata found for %s at commit %s", relativePath, commitHash)
		return nil
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
	log.Printf("DEBUG applyCommit: commit=%s, paths=%v", commitHash, paths)
	// Get files changed in this commit
	files, metadatas, err := gm.getFilesInCommit(commitHash, paths)
	log.Printf("DEBUG applyCommit: files returned: %v", files)
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

// restoreFromCommit directly restores files from a specific commit
// This is simpler and more reliable than applying commits in reverse order
func (gm *GitManager) restoreFromCommit(commitHash string, paths []string) error {
	log.Printf("DEBUG restoreFromCommit: commit=%s, paths=%v", commitHash, paths)

	// Get all files in the target commit (filtered by paths if provided)
	files, _, err := gm.getFilesInCommit(commitHash, paths)
	if err != nil {
		return fmt.Errorf("failed to get files in commit %s: %w", commitHash, err)
	}

	log.Printf("DEBUG restoreFromCommit: files found in commit: %v", files)

	// If specific paths were provided but no files found, try to find files in commit
	// This handles the case where the file was renamed - we need to find any file in the commit
	if len(files) == 0 && len(paths) > 0 {
		log.Printf("DEBUG restoreFromCommit: requested path(s) not found in commit, looking for any files")
		// Get all files from the commit (no filter)
		allFiles, _, err := gm.getFilesInCommit(commitHash, nil)
		if err == nil && len(allFiles) > 0 {
			log.Printf("DEBUG restoreFromCommit: found files in commit: %v", allFiles)
			// For each requested path, try to restore from any file in the commit
			for _, requestedPath := range paths {
				if requestedPath == "" {
					continue
				}
				// Try to restore the first available file under the requested path name
				restored := false
				for _, commitFile := range allFiles {
					log.Printf("DEBUG restoreFromCommit: attempting to restore commit file %s as %s", commitFile, requestedPath)
					if err := gm.restoreFileFromCommit(commitHash, commitFile); err == nil {
						// File was restored successfully, now rename it to requestedPath
						// This handles the rename case
						cacheDir, err := gm.getCachePath()
						if err == nil {
							commitFileCachePath := filepath.Join(cacheDir, commitFile)
							requestedCachePath := filepath.Join(cacheDir, requestedPath)
							if commitFileCachePath != requestedCachePath {
								if err := os.Rename(commitFileCachePath, requestedCachePath); err == nil {
									log.Printf("DEBUG restoreFromCommit: renamed %s to %s", commitFileCachePath, requestedCachePath)
									// Invalidate FUSE cache for the new path
									if gm.restoreFile != nil {
										requestedMountPath := filepath.Join(gm.workspacePath, requestedPath)
										gm.restoreFile(requestedMountPath)
									}
									restored = true
									break
								}
							} else {
								restored = true
								break
							}
						}
					}
				}
				if !restored {
					log.Printf("DEBUG restoreFromCommit: could not restore any file as %s", requestedPath)
				}
			}
			return nil
		}
		// If still no files, try the original behavior
		files = paths
	}

	// Restore each file
	for _, file := range files {
		if file == "" {
			continue
		}
		log.Printf("DEBUG restoreFromCommit: restoring file: %s", file)
		if err := gm.restoreFileFromCommit(commitHash, file); err != nil {
			Warn("restoreFromCommit: Failed to restore file %s: %v (continuing)", file, err)
			// Continue with other files
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

	// Step 5: Apply commits or directly restore from target commit
	Debug("RestoreCommit: Step 5 - Applying restoration")
	if isForward {
		// Forward restore: apply commits from oldest to newest
		Debug("RestoreCommit: Applying commits forward (%d commits)", len(commits))
		for i, commit := range commits {
			Debug("RestoreCommit: Applying commit %d/%d: %s", i+1, len(commits), commit)
			if err := gm.applyCommit(commit, paths); err != nil {
				return fmt.Errorf("failed to apply commit %s: %w", commit, err)
			}
		}
	} else {
		// Backward restore: directly restore files from target commit
		// The "apply commits in reverse" approach is problematic because:
		// 1. Delete commits have no files in ls-tree output (file was deleted)
		// 2. Rename commits might not have file content directly
		// Instead, directly check out files from the target commit
		Debug("RestoreCommit: Directly restoring files from target commit %s", commitHash)
		if err := gm.restoreFromCommit(commitHash, paths); err != nil {
			return fmt.Errorf("failed to restore from commit %s: %w", commitHash, err)
		}
	}

	Debug("RestoreCommit: Successfully completed restore to commit %s", commitHash)
	return nil
}

// detectRenameConflict detects if restoring a path conflicts with an existing rename
func (gm *GitManager) detectRenameConflict(restorePath string) (string, bool) {
	Debug("detectRenameConflict: Checking for rename conflicts when restoring %s", restorePath)
	Debug("detectRenameConflict: pathResolver=%v, xattrMgr=%v", gm.pathResolver != nil, gm.xattrMgr != nil)

	// Method 1: Use RenameTracker for fast memory lookup (preferred)
	if gm.pathResolver != nil && gm.pathResolver.renameTracker != nil {
		Debug("detectRenameConflict: Using method 1 (rename tracker)")
		if destPath, found := gm.pathResolver.renameTracker.FindRenamedToPath(restorePath); found {
			Debug("detectRenameConflict: Found rename conflict via tracker: %s was renamed to %s", restorePath, destPath)
			return destPath, true
		}
		Debug("detectRenameConflict: No conflict found in rename tracker for %s", restorePath)
	} else {
		Debug("detectRenameConflict: Skipping method 1 (no rename tracker available)")
	}

	// Method 2: Fallback to xattr check for the specific restore path (fast)
	if gm.xattrMgr != nil {
		Debug("detectRenameConflict: Using method 2 (xattr check)")
		cachePath := filepath.Join(gm.workspacePath, restorePath)
		if attr, exists, errno := gm.xattrMgr.GetStatus(cachePath); errno == 0 && exists && attr != nil {
			if renamedFrom := attr.GetRenamedFromPath(); renamedFrom != "" && renamedFrom == restorePath {
				Debug("detectRenameConflict: Found rename conflict via xattr: %s has rename mapping", restorePath)
				return restorePath, true
			}
		}
		Debug("detectRenameConflict: No xattr conflict found for %s", restorePath)
	} else {
		Debug("detectRenameConflict: Skipping method 2 (no xattr manager)")
	}

	// Method 3: Last resort - targeted xattr search (avoid full directory walk)
	// Only check if we absolutely need to, and with timeout protection
	if gm.xattrMgr != nil && gm.pathResolver != nil && gm.pathResolver.renameTracker != nil {
		Debug("detectRenameConflict: Using method 3 (targeted search)")
		if conflictPath, found := gm.findRenameConflictViaTargetedSearch(restorePath); found {
			Debug("detectRenameConflict: Found conflict via targeted search: %s", conflictPath)
			return conflictPath, true
		}
	} else {
		Debug("detectRenameConflict: Skipping method 3 (missing dependencies)")
	}

	// Method 4: Fallback to timeout-protected directory walk (when no rename tracker available)
	// This happens when GetGitRepository is used (CLI operations) where pathResolver is nil
	if gm.xattrMgr != nil {
		Debug("detectRenameConflict: Using method 4 (directory walk with timeout)")
		if conflictPath, found := gm.findRenameConflictViaDirectoryWalk(restorePath); found {
			Debug("detectRenameConflict: Found conflict via directory walk: %s", conflictPath)
			return conflictPath, true
		}
	} else {
		Debug("detectRenameConflict: Skipping method 4 (no xattr manager)")
	}

	Debug("detectRenameConflict: No rename conflicts found for %s", restorePath)
	return "", false
}

// findRenameConflictViaTargetedSearch performs a targeted search for rename conflicts
// This is more efficient than walking the entire directory but still slower than memory lookup
func (gm *GitManager) findRenameConflictViaTargetedSearch(restorePath string) (string, bool) {
	if gm.xattrMgr == nil || gm.pathResolver == nil || gm.pathResolver.renameTracker == nil {
		return "", false
	}

	// Get list of potential conflict paths from rename tracker
	// These are paths that have been renamed, so they might conflict
	candidatePaths := gm.pathResolver.renameTracker.GetDestinationPaths()

	// Check xattr for each candidate path (much fewer than walking entire directory)
	for _, candidatePath := range candidatePaths {
		cachePath := filepath.Join(gm.workspacePath, candidatePath)
		if attr, exists, errno := gm.xattrMgr.GetStatus(cachePath); errno == 0 && exists && attr != nil {
			if renamedFrom := attr.GetRenamedFromPath(); renamedFrom == restorePath {
				return candidatePath, true
			}
		}
	}

	return "", false
}

// findRenameConflictViaDirectoryWalk performs a timeout-protected directory walk
// This is the fallback when no rename tracker is available (e.g., CLI operations)
func (gm *GitManager) findRenameConflictViaDirectoryWalk(restorePath string) (string, bool) {
	if gm.xattrMgr == nil {
		return "", false
	}

	// Create a channel to signal completion or timeout
	type result struct {
		path  string
		found bool
	}

	resultChan := make(chan result, 1)

	// Run directory walk in a goroutine with timeout protection
	go func() {
		defer close(resultChan)

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
						resultChan <- result{path: filepath.ToSlash(relPath), found: true}
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
				resultChan <- result{path: conflictPath, found: true}
				return
			}
		}

		resultChan <- result{found: false}
	}()

	// Wait for result with timeout (5 seconds should be plenty for most cases)
	select {
	case res := <-resultChan:
		return res.path, res.found
	case <-time.After(5 * time.Second):
		Debug("detectRenameConflict: Directory walk timed out after 5 seconds for %s", restorePath)
		return "", false
	}
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
		return fmt.Errorf("failed to clear rename mapping: %w", errno)
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

		// Robustness defaults
		MaxQueueSize:    10000,            // Prevent unbounded growth
		QueueTimeout:    10 * time.Second, // Reasonable backpressure
		MaxPausedBuffer: 5000,             // Reasonable buffer limit
		EnableMetrics:   true,             // Production monitoring
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
				// gm.SetIPCClient(ipcClient)
				gm.SetMarkDirtyFunc(func(path string) error {
					return ipcClient.MarkDirty(path)
				})
				gm.SetRefreshFunc(func(path string) error {
					return ipcClient.Refresh(path)
				})
				gm.SetRestoreFileFunc(func(path string) error {
					return ipcClient.RestoreFile(path)
				})
				Debug("GetGitRepository: IPC client connected successfully: %s", socketPath)
				return gm, nil
			}
			Debug("GetGitRepository: Failed to create IPC client: %v (continuing without IPC)", err)
		} else {
			Debug("GetGitRepository: IPC socket not available at %s (FUSE process may not be running or socket not accessible, continuing without IPC)", socketPath)
		}
	} else {
		Debug("GetGitRepository: Failed to get socket path: %v (continuing without IPC)", err)
	}

	gm.SetMarkDirtyFunc(func(path string) error {
		return fmt.Errorf("IPC client not available (FUSE process not running), file may have stale cache until next read/write")
	})
	gm.SetRefreshFunc(func(path string) error {
		return fmt.Errorf("IPC client not available (FUSE process not running), file may have stale cache until next read/write")
	})
	gm.SetRestoreFileFunc(func(path string) error {
		return fmt.Errorf("IPC client not available (FUSE process not running), file may have stale cache until next read/write")
	})

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
