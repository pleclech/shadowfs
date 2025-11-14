package fs

import (
	"path/filepath"
	"testing"
	"time"

	tu "github.com/pleclech/shadowfs/fs/utils/testings"
)

func TestNewPathManager(t *testing.T) {
	sourceRoot := "/source"
	cacheRoot := "/cache"

	pm := NewPathManager(sourceRoot, cacheRoot)

	if pm == nil {
		tu.Failf(t, "NewPathManager() returned nil")
	}

	if pm.GetSourceRoot() != sourceRoot {
		tu.Failf(t, "Expected source root %s, got %s", sourceRoot, pm.GetSourceRoot())
	}

	if pm.GetCacheRoot() != cacheRoot {
		tu.Failf(t, "Expected cache root %s, got %s", cacheRoot, pm.GetCacheRoot())
	}
}

func TestPathManager_RebaseToCache(t *testing.T) {
	pm := NewPathManager("/source", "/cache")

	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "simple file",
			path:     "/source/file.txt",
			expected: "/cache/file.txt",
		},
		{
			name:     "nested file",
			path:     "/source/dir/subdir/file.txt",
			expected: "/cache/dir/subdir/file.txt",
		},
		{
			name:     "root directory",
			path:     "/source",
			expected: "/cache",
		},
		{
			name:     "path outside source",
			path:     "/other/file.txt",
			expected: "/other/file.txt",
		},
		{
			name:     "empty relative path",
			path:     "/source/",
			expected: "/cache",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pm.RebaseToCache(tt.path)
			if result != tt.expected {
				tu.Failf(t, "RebaseToCache(%s) = %s, expected %s", tt.path, result, tt.expected)
			}
		})
	}
}

func TestPathManager_RebaseToSource(t *testing.T) {
	pm := NewPathManager("/source", "/cache")

	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "simple file",
			path:     "/cache/file.txt",
			expected: "/source/file.txt",
		},
		{
			name:     "nested file",
			path:     "/cache/dir/subdir/file.txt",
			expected: "/source/dir/subdir/file.txt",
		},
		{
			name:     "root directory",
			path:     "/cache",
			expected: "/source",
		},
		{
			name:     "path outside cache",
			path:     "/other/file.txt",
			expected: "/other/file.txt",
		},
		{
			name:     "empty relative path",
			path:     "/cache/",
			expected: "/source",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pm.RebaseToSource(tt.path)
			if result != tt.expected {
				tu.Failf(t, "RebaseToSource(%s) = %s, expected %s", tt.path, result, tt.expected)
			}
		})
	}
}

func TestPathManager_IsCachePath(t *testing.T) {
	pm := NewPathManager("/source", "/cache")

	tests := []struct {
		path     string
		expected bool
	}{
		{"/cache/file.txt", true},
		{"/cache/dir/file.txt", true},
		{"/source/file.txt", false},
		{"/other/file.txt", false},
		{"/cache", true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := pm.IsCachePath(tt.path)
			if result != tt.expected {
				tu.Failf(t, "IsCachePath(%s) = %v, expected %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestPathManager_IsSourcePath(t *testing.T) {
	pm := NewPathManager("/source", "/cache")

	tests := []struct {
		path     string
		expected bool
	}{
		{"/source/file.txt", true},
		{"/source/dir/file.txt", true},
		{"/cache/file.txt", false},
		{"/other/file.txt", false},
		{"/source", true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := pm.IsSourcePath(tt.path)
			if result != tt.expected {
				tu.Failf(t, "IsSourcePath(%s) = %v, expected %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestPathManager_FullPath(t *testing.T) {
	pm := NewPathManager("/source", "/cache")

	tests := []struct {
		name     string
		path     string
		useCache bool
		expected string
	}{
		{
			name:     "cache path",
			path:     "file.txt",
			useCache: true,
			expected: "/cache/file.txt",
		},
		{
			name:     "source path",
			path:     "file.txt",
			useCache: false,
			expected: "/source/file.txt",
		},
		{
			name:     "nested cache path",
			path:     "dir/file.txt",
			useCache: true,
			expected: "/cache/dir/file.txt",
		},
		{
			name:     "nested source path",
			path:     "dir/file.txt",
			useCache: false,
			expected: "/source/dir/file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pm.FullPath(tt.path, tt.useCache)
			if result != tt.expected {
				tu.Failf(t, "FullPath(%s, %v) = %s, expected %s", tt.path, tt.useCache, result, tt.expected)
			}
		})
	}
}

func TestPathManager_ClearCache(t *testing.T) {
	pm := NewPathManager("/source", "/cache")

	// Populate cache
	pm.RebaseToCache("/source/file1.txt")
	pm.RebaseToCache("/source/file2.txt")

	// Verify cache has entries
	size, _, _ := pm.GetCacheStats()
	if size == 0 {
		tu.Errorf(t, "Expected cache to have entries before ClearCache")
	}

	// Clear cache
	pm.ClearCache()

	// Verify cache is empty
	size, _, _ = pm.GetCacheStats()
	if size != 0 {
		tu.Failf(t, "Expected cache size 0 after ClearCache, got %d", size)
	}
}

func TestPathManager_GetCacheStats(t *testing.T) {
	pm := NewPathManager("/source", "/cache")

	// Initially empty
	size, sourceEntries, cacheEntries := pm.GetCacheStats()
	if size != 0 || sourceEntries != 0 || cacheEntries != 0 {
		tu.Failf(t, "Expected empty cache stats, got size=%d, source=%d, cache=%d", size, sourceEntries, cacheEntries)
	}

	// Add some entries
	pm.RebaseToCache("/source/file1.txt")
	pm.RebaseToCache("/source/file2.txt")
	pm.RebaseToSource("/cache/file3.txt")

	// Check stats
	size, sourceEntries, cacheEntries = pm.GetCacheStats()
	if size < 2 {
		tu.Failf(t, "Expected at least 2 cache entries, got size=%d", size)
	}
	if sourceEntries < 2 {
		tu.Failf(t, "Expected at least 2 source entries, got %d", sourceEntries)
	}
	if cacheEntries < 1 {
		tu.Failf(t, "Expected at least 1 cache entry, got %d", cacheEntries)
	}
}

func TestPathManager_Caching(t *testing.T) {
	pm := NewPathManager("/source", "/cache")

	path := "/source/file.txt"

	// First call should compute
	result1 := pm.RebaseToCache(path)

	// Second call should use cache (same result)
	result2 := pm.RebaseToCache(path)

	if result1 != result2 {
		tu.Failf(t, "Cached result differs: first=%s, second=%s", result1, result2)
	}
}

func TestPathManager_CacheCleanup(t *testing.T) {
	pm := NewPathManager("/source", "/cache")

	// Set a very short cache timeout for testing
	pm.cacheTimeout = 10 * time.Millisecond
	pm.lastCleanup = time.Now()

	// Add entries
	pm.RebaseToCache("/source/file1.txt")
	pm.RebaseToCache("/source/file2.txt")

	// Wait for cleanup timeout
	time.Sleep(20 * time.Millisecond)

	// Trigger cleanup by calling RebaseToCache again
	pm.RebaseToCache("/source/file3.txt")

	// Cache cleanup should have run (we can't easily verify it cleared old entries,
	// but we can verify the function doesn't panic)
}

func TestPathManager_ConcurrentAccess(t *testing.T) {
	pm := NewPathManager("/source", "/cache")

	// Test concurrent RebaseToCache calls
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			path := filepath.Join("/source", "file", string(rune(id))+".txt")
			pm.RebaseToCache(path)
			pm.RebaseToSource(pm.RebaseToCache(path))
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// If we get here without panic, thread safety worked
}
