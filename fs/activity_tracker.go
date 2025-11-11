package fs

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// FileTimer tracks activity and commit timer for a single file
type FileTimer struct {
	timer        *time.Timer
	lastActivity time.Time
}

// ActivityTracker monitors file activity for idle detection using timers
type ActivityTracker struct {
	fileTimers map[string]*FileTimer
	shadowNode *ShadowNode
	config     GitConfig
	mutex      sync.RWMutex
	stopChan   chan bool
}

// NewActivityTracker creates a new ActivityTracker
func NewActivityTracker(shadowNode *ShadowNode, config GitConfig) *ActivityTracker {
	return &ActivityTracker{
		fileTimers: make(map[string]*FileTimer),
		shadowNode: shadowNode,
		config:     config,
		stopChan:   make(chan bool),
	}
}

// MarkActivity records activity for a file and starts/resets the commit timer
func (at *ActivityTracker) MarkActivity(filePath string) {
	if !at.shadowNode.gitManager.IsEnabled() {
		return // Git disabled, no tracking needed
	}
	at.mutex.Lock()
	defer at.mutex.Unlock()

	now := time.Now()

	// Stop existing timer if any
	if existingTimer, exists := at.fileTimers[filePath]; exists {
		existingTimer.timer.Stop()
	}

	// Create new timer that will commit after idle timeout
	timer := time.AfterFunc(at.config.IdleTimeout, func() {
		at.commitFile(filePath)
	})

	// Store the timer and activity time
	at.fileTimers[filePath] = &FileTimer{
		timer:        timer,
		lastActivity: now,
	}
}

// CommitAllPending commits all files with pending timers before shutdown
// This prevents data loss when unmounting or shutting down
func (at *ActivityTracker) CommitAllPending() error {
	at.mutex.Lock()
	defer at.mutex.Unlock()

	if !at.shadowNode.gitManager.IsEnabled() {
		return nil // Git disabled, nothing to commit
	}

	// Collect all active file paths
	activeFiles := make([]string, 0, len(at.fileTimers))
	for filePath, fileTimer := range at.fileTimers {
		// Stop the timer to prevent duplicate commits
		fileTimer.timer.Stop()
		activeFiles = append(activeFiles, filePath)
	}

	// Clear all timers
	at.fileTimers = make(map[string]*FileTimer)

	// Commit all files (batch commit for efficiency)
	if len(activeFiles) > 0 {
		return at.commitFilesBatch(activeFiles, "Auto-commit on unmount")
	}

	return nil
}

// StopIdleMonitoring stops the idle monitoring and cancels all timers
// Note: Use CommitAllPending() first to prevent data loss
func (at *ActivityTracker) StopIdleMonitoring() {
	at.mutex.Lock()
	defer at.mutex.Unlock()

	// Cancel all pending timers
	for filePath, fileTimer := range at.fileTimers {
		fileTimer.timer.Stop()
		delete(at.fileTimers, filePath)
	}
}

// commitFile commits a file when its timer expires
func (at *ActivityTracker) commitFile(filePath string) {
	at.mutex.Lock()
	
	// Check if file still exists in tracking (might have been removed)
	fileTimer, exists := at.fileTimers[filePath]
	if !exists {
		at.mutex.Unlock()
		return
	}

	// Calculate time since last activity
	timeSinceLastWrite := time.Since(fileTimer.lastActivity)
	
	// Check if safety window has passed
	safetyWindow := at.config.SafetyWindow
	if safetyWindow == 0 {
		// Default safety window: 5 seconds
		safetyWindow = 5 * time.Second
	}
	
	if timeSinceLastWrite < safetyWindow {
		// Safety window not yet passed - reschedule commit
		remainingTime := safetyWindow - timeSinceLastWrite
		at.mutex.Unlock()
		
		// Reschedule timer for remaining safety window time
		timer := time.AfterFunc(remainingTime, func() {
			at.commitFile(filePath)
		})
		
		at.mutex.Lock()
		if existingTimer, stillExists := at.fileTimers[filePath]; stillExists {
			existingTimer.timer.Stop()
			at.fileTimers[filePath] = &FileTimer{
				timer:        timer,
				lastActivity: fileTimer.lastActivity, // Keep original last activity time
			}
		}
		at.mutex.Unlock()
		return
	}

	// Safety window passed - proceed with commit
	// Remove from tracking before committing (to prevent race conditions)
	delete(at.fileTimers, filePath)
	at.mutex.Unlock()

	// Commit the file if it has significant changes (outside lock to avoid blocking)
	if at.hasSignificantChanges(filePath) {
		// Build commit reason with timing info
		reason := fmt.Sprintf("Auto-commit after idle period (idle: %v, last-write: %v ago)", 
			at.config.IdleTimeout, timeSinceLastWrite)
		err := at.shadowNode.gitManager.AutoCommitFile(filePath, reason)
		if err != nil {
			log.Printf("Failed to commit file %s: %v", filePath, err)
		}
	}
}

// commitFilesBatch commits multiple files in a single git commit for efficiency
func (at *ActivityTracker) commitFilesBatch(filePaths []string, reason string) error {
	if len(filePaths) == 0 {
		return nil
	}

	// Filter files that have significant changes
	filesToCommit := make([]string, 0, len(filePaths))
	for _, filePath := range filePaths {
		if at.hasSignificantChanges(filePath) {
			filesToCommit = append(filesToCommit, filePath)
		}
	}

	if len(filesToCommit) == 0 {
		return nil // No changes to commit
	}

	// Use sync versions directly to ensure commits complete (important for shutdown)
	if len(filesToCommit) == 1 {
		err := at.shadowNode.gitManager.CommitFileSync(filesToCommit[0], reason)
		if err != nil {
			log.Printf("Failed to commit file %s: %v", filesToCommit[0], err)
			return err
		}
		return nil
	}

	// Batch commit multiple files (sync version)
	return at.shadowNode.gitManager.CommitFilesBatchSync(filesToCommit, reason)
}

// hasSignificantChanges checks if file has meaningful changes
func (at *ActivityTracker) hasSignificantChanges(filePath string) bool {
	// For now, assume any file activity is significant
	return true
}

// IsIdle checks if a specific file is idle (no pending timer)
func (at *ActivityTracker) IsIdle(filePath string) bool {
	at.mutex.RLock()
	defer at.mutex.RUnlock()

	_, exists := at.fileTimers[filePath]
	return !exists // No pending timer means file is idle
}

// GetActiveFiles returns list of currently active files (with pending timers)
func (at *ActivityTracker) GetActiveFiles() []string {
	at.mutex.RLock()
	defer at.mutex.RUnlock()

	activeFiles := make([]string, 0, len(at.fileTimers))
	for filePath := range at.fileTimers {
		activeFiles = append(activeFiles, filePath)
	}
	return activeFiles
}
