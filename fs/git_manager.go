package fs

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CommitRequest represents a request to commit files
type CommitRequest struct {
	filePaths   []string
	reason      string
	commitType  string // "auto-save", "checkpoint", "unmount"
	result      chan error
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
	gitDir := filepath.Join(workspacePath, ".gitofs")
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
				var err error
				if len(req.filePaths) == 1 {
					err = gm.autoCommitFileSync(req.filePaths[0], req.reason, req.commitType)
				} else {
					err = gm.autoCommitFilesBatchSync(req.filePaths, req.reason, req.commitType)
				}
				if req.result != nil {
					req.result <- err
				}
			case <-gm.stopChan:
				// Process remaining commits before stopping
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

	cmd := exec.Command("git", "init", gm.gitDir, "--template=")
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

// setupGitIgnore creates .gitignore to ignore source artifacts
func (gm *GitManager) setupGitIgnore() error {
	gitIgnorePath := filepath.Join(gm.workspacePath, ".gitignore")

	content := `# Ignore overlay filesystem artifacts
.shadowfs/

# Ignore temporary files
*.tmp
*.swp
*.bak
*~

# Ignore AI editor artifacts
.ai-cache/
.ai-temp/

# Ignore OS files
.DS_Store
Thumbs.db
`

	return os.WriteFile(gitIgnorePath, []byte(content), 0644)
}

// AutoCommitFile stages and commits a file with automatic message (async, non-blocking)
func (gm *GitManager) AutoCommitFile(filePath string, reason string) error {
	if !gm.enabled {
		return nil
	}

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
		return nil
	default:
		// Queue full, fall back to synchronous commit
		log.Printf("Commit queue full, committing synchronously: %s", filePath)
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
	// Validate and convert absolute path to relative path
	relativePath, err := gm.normalizePath(filePath)
	if err != nil {
		log.Printf("Invalid path %s: %v", filePath, err)
		return fmt.Errorf("invalid path %s: %w", filePath, err)
	}

	// Check if file has changes before committing (efficiency improvement)
	if !gm.hasFileChanges(relativePath) {
		return nil // No changes, skip commit
	}

	// Stage the file
	cmd := exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "add", relativePath)
	if err := cmd.Run(); err != nil {
		log.Printf("Failed to stage file %s: %v", relativePath, err)
		return fmt.Errorf("failed to stage file %s: %w", relativePath, err)
	}

	// Build commit message with type metadata
	var commitMsg string
	switch commitType {
	case "checkpoint":
		commitMsg = fmt.Sprintf("Checkpoint: %s", reason)
	case "unmount":
		commitMsg = fmt.Sprintf("Auto-commit on unmount: %s", reason)
	default: // "auto-save"
		commitMsg = fmt.Sprintf("Auto-commit: %s", reason)
	}

	// Commit with message
	cmd = exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "commit", "-m", commitMsg)
	if err := cmd.Run(); err != nil {
		// Check if commit failed because there are no changes (common case)
		if strings.Contains(err.Error(), "nothing to commit") {
			return nil // Not an error, just no changes
		}
		log.Printf("Failed to commit file %s: %v", relativePath, err)
		return fmt.Errorf("failed to commit file %s: %w", relativePath, err)
	}

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
		reason:    reason,
		commitType: "auto-save",
		result:    result,
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
	fileList := strings.Join(relativePaths, ", ")
	var commitMsg string
	switch commitType {
	case "checkpoint":
		commitMsg = fmt.Sprintf("Checkpoint: %s (%d files: %s)", reason, len(relativePaths), fileList)
	case "unmount":
		commitMsg = fmt.Sprintf("Auto-commit on unmount: %s (%d files: %s)", reason, len(relativePaths), fileList)
	default: // "auto-save"
		commitMsg = fmt.Sprintf("Auto-commit: %s (%d files: %s)", reason, len(relativePaths), fileList)
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
	if !filepath.IsAbs(filePath) {
		// Already relative, validate it's within workspace
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
	cmd := exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "ls-files", "--error-unmatch", "--", relativePath)
	if err := cmd.Run(); err != nil {
		// File is not tracked, so it's a new file (has changes)
		return true
	}

	// File is tracked, check if it has modifications
	cmd = exec.Command("git", "--git-dir", gm.gitDir, "-C", gm.gitWorkDir, "diff", "--quiet", "--", relativePath)
	err := cmd.Run()
	// git diff --quiet returns 0 if no changes, 1 if changes exist
	return err != nil
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
