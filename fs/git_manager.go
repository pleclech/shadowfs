package fs

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pleclech/shadowfs/fs/cache"
	"github.com/pleclech/shadowfs/fs/rootinit"
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
	workspacePath string
	sourcePath    string
	gitWorkDir    string
	gitDir        string
	enabled       bool
	config        GitConfig
	commitQueue   chan CommitRequest
	wg            sync.WaitGroup
	stopChan      chan struct{}
	once          sync.Once
}

// GitConfig contains configuration for Git operations
type GitConfig struct {
	IdleTimeout  time.Duration
	SafetyWindow time.Duration // Delay after last write before committing (default: 5s)
	AutoCommit   bool
}

// NewGitManager creates a new GitManager instance
func NewGitManager(workspacePath, sourcePath string, config GitConfig) *GitManager {
	// gitDir points to the actual .git directory (created by git init)
	// git init .gitofs creates .gitofs/.git/, so gitDir should be .gitofs/.git
	gitDir := cache.GetGitDirPath(workspacePath)
	gm := &GitManager{
		workspacePath: workspacePath,
		sourcePath:    sourcePath,
		gitWorkDir:    workspacePath,
		gitDir:        gitDir,
		enabled:       config.AutoCommit,
		config:        config,
		commitQueue:   make(chan CommitRequest, 100), // Buffer up to 100 commit requests
		stopChan:      make(chan struct{}),
	}

	// Start background commit processor if enabled
	if config.AutoCommit {
		gm.startCommitProcessor()
	}

	return gm
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
				log.Printf("Git commit processor: Processing commit request for %d file(s), type: %s", len(req.filePaths), req.commitType)
				var err error
				if len(req.filePaths) == 1 {
					log.Printf("Git commit processor: Committing file: %s", req.filePaths[0])
					err = gm.autoCommitFileSync(req.filePaths[0], req.reason, req.commitType)
				} else {
					log.Printf("Git commit processor: Committing batch of %d files", len(req.filePaths))
					err = gm.autoCommitFilesBatchSync(req.filePaths, req.reason, req.commitType)
				}
				if err != nil {
					log.Printf("Git commit processor: Error committing: %v", err)
				} else {
					log.Printf("Git commit processor: Successfully committed")
				}
				if req.result != nil {
					req.result <- err
				}
			case <-gm.stopChan:
				// Process remaining commits before stopping
				log.Printf("Git commit processor: Stopping, processing remaining commits")
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

	// Setup .gitignore to ignore source artifacts
	// remove we don't want to overwrite an existing .gitignore in source dir
	// if err := gm.setupGitIgnore(); err != nil {
	// 	return fmt.Errorf("failed to setup .gitignore: %w", err)
	// }

	return nil
}

// createWorkspaceGit initializes Git repository in workspace
func (gm *GitManager) createWorkspaceGit() error {
	// Initialize Git repository with empty template to avoid issues
	// Use absolute path to avoid FUSE filesystem issues
	// git init creates .git/ inside the specified directory, so we pass .gitofs (without .git)
	gitInitPath := filepath.Join(gm.workspacePath, GitofsName)
	cmd := exec.Command("git", "init", gitInitPath, "--template=")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git init failed: %w", err)
	}

	// Configure Git user if not set
	if err := gm.configureGitUser(); err != nil {
		return fmt.Errorf("failed to configure Git user: %w", err)
	}

	return nil
}

// configureGitUser sets up Git user if not configured
func (gm *GitManager) configureGitUser() error {
	// Check if user.name is configured
	cmd := exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "config", "user.name")
	if output, err := cmd.Output(); err == nil && len(strings.TrimSpace(string(output))) > 0 {
		return nil // Already configured
	}

	// Set default user configuration
	cmd = exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "config", "user.name", "Overlay FS")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set git user.name: %w", err)
	}

	cmd = exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "config", "user.email", "overlay@localhost")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set git user.email: %w", err)
	}

	return nil
}

// AutoCommitFile stages and commits a file with automatic message (async, non-blocking)
func (gm *GitManager) AutoCommitFile(filePath string, reason string) error {
	if !gm.enabled {
		log.Printf("AutoCommitFile: Git disabled, skipping commit for %s", filePath)
		return nil
	}

	log.Printf("AutoCommitFile: Queuing commit for %s (reason: %s)", filePath, reason)
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
		log.Printf("AutoCommitFile: Successfully queued commit for %s", filePath)
		return nil
	default:
		// Queue full, fall back to synchronous commit
		log.Printf("AutoCommitFile: Commit queue full, committing synchronously: %s", filePath)
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
	log.Printf("autoCommitFileSync: Starting commit for %s (type: %s, reason: %s)", filePath, commitType, reason)
	log.Printf("autoCommitFileSync: Workspace path: %s, Git dir: %s, Git work dir: %s", gm.workspacePath, gm.gitDir, gm.gitWorkDir)

	// Validate and convert absolute path to relative path
	relativePath, err := gm.normalizePath(filePath)
	if err != nil {
		log.Printf("autoCommitFileSync: Invalid path %s: %v", filePath, err)
		return fmt.Errorf("invalid path %s: %w", filePath, err)
	}
	log.Printf("autoCommitFileSync: Normalized path: %s -> %s", filePath, relativePath)

	// Check if file has changes before committing (efficiency improvement)
	hasChanges := gm.hasFileChanges(relativePath)
	log.Printf("autoCommitFileSync: File %s has changes: %v", relativePath, hasChanges)
	if !hasChanges {
		log.Printf("autoCommitFileSync: No changes detected for %s, skipping commit", relativePath)
		return nil // No changes, skip commit
	}

	// Stage the file
	cmd := exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "add", relativePath)
	log.Printf("autoCommitFileSync: Staging file: git --git-dir %s -C %s add %s", gm.gitDir, gm.gitWorkDir, relativePath)
	if err := cmd.Run(); err != nil {
		log.Printf("autoCommitFileSync: Failed to stage file %s: %v", relativePath, err)
		if output, outputErr := cmd.CombinedOutput(); outputErr == nil {
			log.Printf("autoCommitFileSync: Git add output: %s", string(output))
		}
		return fmt.Errorf("failed to stage file %s: %w", relativePath, err)
	}
	log.Printf("autoCommitFileSync: Successfully staged file %s", relativePath)

	// Build commit message with type metadata
	// For single file commits, show file count (not file name to keep it concise)
	var commitMsg string
	switch commitType {
	case "checkpoint":
		commitMsg = fmt.Sprintf("Checkpoint: %s", reason)
	case "unmount":
		commitMsg = "Auto-commit on unmount: 1 file"
	default: // "auto-save"
		commitMsg = "Auto-commit: 1 file"
	}

	// Commit with message
	cmd = exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "commit", "-m", commitMsg)
	log.Printf("autoCommitFileSync: Committing: git --git-dir %s -C %s commit -m \"%s\"", gm.gitDir, gm.gitWorkDir, commitMsg)
	if err := cmd.Run(); err != nil {
		// Check if commit failed because there are no changes (common case)
		if output, outputErr := cmd.CombinedOutput(); outputErr == nil {
			log.Printf("autoCommitFileSync: Git commit output: %s", string(output))
		}
		if strings.Contains(err.Error(), "nothing to commit") {
			log.Printf("autoCommitFileSync: Nothing to commit for %s", relativePath)
			return nil // Not an error, just no changes
		}
		log.Printf("autoCommitFileSync: Failed to commit file %s: %v", relativePath, err)
		return fmt.Errorf("failed to commit file %s: %w", relativePath, err)
	}
	log.Printf("autoCommitFileSync: Successfully committed file %s", relativePath)

	// Tag commit with type if checkpoint
	if commitType == "checkpoint" {
		tagName := fmt.Sprintf("checkpoint-%s-%d", relativePath, time.Now().Unix())
		cmd = exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "tag", tagName)
		_ = cmd.Run() // Ignore tag errors (tag might already exist)
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
		log.Printf("Commit queue full, committing batch synchronously (%d files)", len(filePaths))
		return gm.autoCommitFilesBatchSync(filePaths, reason, "auto-save")
	}
}

// autoCommitFilesBatchSync performs the actual batch commit synchronously (internal use)
func (gm *GitManager) autoCommitFilesBatchSync(filePaths []string, reason string, commitType string) error {
	if len(filePaths) == 0 {
		return nil
	}

	// Normalize all paths
	relativePaths := make([]string, 0, len(filePaths))
	for _, filePath := range filePaths {
		relPath, err := gm.normalizePath(filePath)
		if err != nil {
			log.Printf("Invalid path %s: %v", filePath, err)
			// Log error but continue with other files
			continue
		}
		relativePaths = append(relativePaths, relPath)
	}

	if len(relativePaths) == 0 {
		return nil // No valid paths to commit
	}

	// Stage all files
	for _, relPath := range relativePaths {
		cmd := exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "add", relPath)
		if err := cmd.Run(); err != nil {
			log.Printf("Failed to stage file %s: %v", relPath, err)
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
	cmd := exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "commit", "-m", commitMsg)
	if err := cmd.Run(); err != nil {
		// Check if commit failed because there are no changes
		if strings.Contains(err.Error(), "nothing to commit") {
			return nil // Not an error, just no changes
		}
		log.Printf("Failed to commit batch: %v", err)
		return fmt.Errorf("failed to commit batch: %w", err)
	}

	// Tag commit with type if checkpoint
	if commitType == "checkpoint" {
		tagName := fmt.Sprintf("checkpoint-batch-%d", time.Now().Unix())
		cmd = exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "tag", tagName)
		_ = cmd.Run() // Ignore tag errors
	}

	return nil
}

// normalizePath converts absolute path to relative path, validates it
func (gm *GitManager) normalizePath(filePath string) (string, error) {
	log.Printf("normalizePath: Converting path %s (workspace: %s)", filePath, gm.workspacePath)
	if !filepath.IsAbs(filePath) {
		// Already relative, validate it's within workspace
		log.Printf("normalizePath: Path %s is already relative", filePath)
		return filePath, nil
	}

	// Convert absolute cache path to relative path for git
	rel, err := filepath.Rel(gm.workspacePath, filePath)
	if err != nil {
		log.Printf("normalizePath: Failed to compute relative path: %v", err)
		return "", fmt.Errorf("path %s is not within workspace %s: %w", filePath, gm.workspacePath, err)
	}

	// Security: prevent path traversal
	if strings.HasPrefix(rel, "..") {
		log.Printf("normalizePath: Path traversal detected: %s", rel)
		return "", fmt.Errorf("invalid path traversal detected: %s", rel)
	}

	log.Printf("normalizePath: Converted %s -> %s", filePath, rel)
	return rel, nil
}

// hasFileChanges checks if a file has uncommitted changes
func (gm *GitManager) hasFileChanges(relativePath string) bool {
	// Check if file is new (untracked)
	cmd := exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "ls-files", "--error-unmatch", "--", relativePath)
	log.Printf("hasFileChanges: Checking if file is tracked: git --git-dir %s -C %s ls-files --error-unmatch -- %s", gm.gitDir, gm.gitWorkDir, relativePath)
	if err := cmd.Run(); err != nil {
		// File is not tracked, so it's a new file (has changes)
		log.Printf("hasFileChanges: File %s is not tracked (new file), has changes: true", relativePath)
		return true
	}

	// File is tracked, check if it has modifications
	cmd = exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "diff", "--quiet", "--", relativePath)
	log.Printf("hasFileChanges: Checking if file has modifications: git --git-dir %s -C %s diff --quiet -- %s", gm.gitDir, gm.gitWorkDir, relativePath)
	err := cmd.Run()
	// git diff --quiet returns 0 if no changes, 1 if changes exist
	hasChanges := err != nil
	log.Printf("hasFileChanges: File %s is tracked, has changes: %v", relativePath, hasChanges)
	return hasChanges
}

// GetWorkspacePath returns the workspace path
func (gm *GitManager) GetWorkspacePath() string {
	return gm.workspacePath
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

// executeGitCommand executes a git command with consistent --git-dir and -C flags
func (gm *GitManager) executeGitCommand(args []string, stdout, stderr io.Writer) error {
	// Build command with consistent git directory and working directory
	cmdArgs := []string{"--git-dir", gm.gitDir, "-C", gm.gitWorkDir}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command("git", cmdArgs...)
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

// GetGitRepository creates a GitManager for an existing mount point
func GetGitRepository(mountPoint string) (*GitManager, error) {
	// Normalize mount point
	normalizedMountPoint, err := rootinit.GetMountPoint(mountPoint)
	if err != nil {
		return nil, fmt.Errorf("invalid mount point: %w", err)
	}

	// git init creates .gitofs/.git/ in mount point, so check for .gitofs/.git
	gitDir := filepath.Join(normalizedMountPoint, ".gitofs", ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return nil, fmt.Errorf("git repository not found in mount point: %w", err)
	}

	// Find cache directory to read source directory from .target file
	cacheDir, err := rootinit.FindCacheDirectory(normalizedMountPoint)
	if err != nil {
		return nil, fmt.Errorf("cannot find cache directory: %w", err)
	}

	// Read source directory from .target file
	targetFile := cache.GetTargetFilePath(cacheDir)
	srcDirData, err := os.ReadFile(targetFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read source directory from .target file: %w", err)
	}
	srcDir := strings.TrimSpace(string(srcDirData))

	// Create GitManager (Git is already initialized)
	// Pass mount point as workspacePath, not cacheDir
	config := GitConfig{
		AutoCommit: true, // Assume enabled if repo exists
	}
	gm := NewGitManager(normalizedMountPoint, srcDir, config)
	gm.enabled = true // Mark as enabled

	return gm, nil
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
	if _, err := os.Stat(gitDir); err != nil {
		return fmt.Errorf("git repository not found: %w", err)
	}

	// Check if it's a valid git repository by checking for HEAD
	headFile := filepath.Join(gitDir, "HEAD")
	if _, err := os.Stat(headFile); err != nil {
		return fmt.Errorf("invalid git repository (HEAD not found): %w", err)
	}

	return nil
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
