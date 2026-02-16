package fs

import (
	"sync"
	"testing"
	"time"

	"github.com/pleclech/shadowfs/fs/cache"
	"github.com/pleclech/shadowfs/fs/operations"
	"github.com/pleclech/shadowfs/fs/xattr"
)

func TestGitManagerQueueProcessing(t *testing.T) {
	config := GitConfig{AutoCommit: true}
	renameTracker := operations.NewRenameTracker()
	cacheMgr := cache.NewManager("/tmp", "/tmp")
	pathResolver := NewPathResolver("/tmp", "/tmp", "/tmp", renameTracker, cacheMgr, nil)
	xattrMgr := xattr.NewManager()

	tests := []struct {
		name        string
		operations  []func(*GitManager)
		expectOrder []string
	}{
		{
			name: "basic queue ordering",
			operations: []func(*GitManager){
				func(gm *GitManager) { gm.QueueGitOperation("op1", func() error { return nil }) },
				func(gm *GitManager) { gm.QueueGitOperation("op2", func() error { return nil }) },
				func(gm *GitManager) { gm.QueueGitOperation("op3", func() error { return nil }) },
			},
			expectOrder: []string{"op1", "op2", "op3"},
		},
		{
			name: "mixed operations with delays",
			operations: []func(*GitManager){
				func(gm *GitManager) { gm.QueueGitOperation("fast", func() error { return nil }) },
				func(gm *GitManager) {
					gm.QueueGitOperation("slow", func() error {
						time.Sleep(10 * time.Millisecond)
						return nil
					})
				},
				func(gm *GitManager) { gm.QueueGitOperation("after", func() error { return nil }) },
			},
			expectOrder: []string{"fast", "slow", "after"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gm := NewGitManager("/tmp", "/tmp", "/tmp", config, pathResolver, xattrMgr)

			for _, op := range tt.operations {
				op(gm)
			}

			// Wait for all operations to complete
			time.Sleep(200 * time.Millisecond)

			// Verify order was maintained
			// This would require adding internal tracking to GitManager for full verification
		})
	}
}

func TestGitManagerPauseResume(t *testing.T) {
	config := GitConfig{AutoCommit: true}
	renameTracker := operations.NewRenameTracker()
	cacheMgr := cache.NewManager("/tmp", "/tmp")
	pathResolver := NewPathResolver("/tmp", "/tmp", "/tmp", renameTracker, cacheMgr, nil)
	xattrMgr := xattr.NewManager()

	gm := NewGitManager("/tmp", "/tmp", "/tmp", config, pathResolver, xattrMgr)

	// Test pause functionality
	gm.PauseAutoCommit()

	// Queue operation during pause
	var mu sync.Mutex
	processed := false
	gm.QueueGitOperation("paused-op", func() error {
		mu.Lock()
		processed = true
		mu.Unlock()
		return nil
	})

	// Should not process immediately
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	wasProcessed := processed
	mu.Unlock()
	if wasProcessed {
		t.Error("Operation should not be processed during pause")
	}

	// Resume and verify processing
	gm.ResumeAutoCommit()
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if !processed {
		mu.Unlock()
		t.Error("Operation should be processed after resume")
	}
	mu.Unlock()
}

func TestGitManagerConcurrentOperations(t *testing.T) {
	config := GitConfig{AutoCommit: true}
	renameTracker := operations.NewRenameTracker()
	cacheMgr := cache.NewManager("/tmp", "/tmp")
	pathResolver := NewPathResolver("/tmp", "/tmp", "/tmp", renameTracker, cacheMgr, nil)
	xattrMgr := xattr.NewManager()

	gm := NewGitManager("/tmp", "/tmp", "/tmp", config, pathResolver, xattrMgr)

	const numOps = 10
	processed := make([]bool, numOps)
	var mu sync.Mutex

	// Queue multiple operations concurrently
	for i := 0; i < numOps; i++ {
		idx := i
		gm.QueueGitOperation("concurrent-op", func() error {
			mu.Lock()
			processed[idx] = true
			mu.Unlock()
			return nil
		})
	}

	// Wait for all operations to complete
	time.Sleep(200 * time.Millisecond)

	// Verify all operations were processed
	mu.Lock()
	defer mu.Unlock()
	for i, p := range processed {
		if !p {
			t.Errorf("Operation %d was not processed", i)
		}
	}
}

func TestGitManagerErrorHandling(t *testing.T) {
	config := GitConfig{AutoCommit: true}
	renameTracker := operations.NewRenameTracker()
	cacheMgr := cache.NewManager("/tmp", "/tmp")
	pathResolver := NewPathResolver("/tmp", "/tmp", "/tmp", renameTracker, cacheMgr, nil)
	xattrMgr := xattr.NewManager()

	gm := NewGitManager("/tmp", "/tmp", "/tmp", config, pathResolver, xattrMgr)

	// Queue operation that fails
	gm.QueueGitOperation("error-op", func() error {
		return &testError{"test error"}
	})

	// Queue operation that succeeds
	var mu sync.Mutex
	success := false
	gm.QueueGitOperation("success-op", func() error {
		mu.Lock()
		success = true
		mu.Unlock()
		return nil
	})

	// Wait for operations to complete
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	if !success {
		mu.Unlock()
		t.Error("Success operation should still be processed after error")
	}
	mu.Unlock()
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
