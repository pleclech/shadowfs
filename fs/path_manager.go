package fs

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PathManager handles centralized path operations and caching
type PathManager struct {
	sourceRoot string
	cacheRoot  string

	// Path transformation cache using lock-free sync.Map
	pathCache    sync.Map
	cacheTimeout time.Duration
	lastCleanup  time.Time
}

// NewPathManager creates a new PathManager instance
func NewPathManager(sourceRoot, cacheRoot string) *PathManager {
	return &PathManager{
		sourceRoot:   sourceRoot,
		cacheRoot:    cacheRoot,
		cacheTimeout: 5 * time.Minute,
		lastCleanup:  time.Now(),
	}
}

// RebaseToCache converts a source path to cache path with caching
func (pm *PathManager) RebaseToCache(path string) string {
	key := "src:" + path

	// Try to get from cache first
	if cached, exists := pm.pathCache.Load(key); exists {
		if result, ok := cached.(string); ok {
			return result
		}
	}

	// Clean up old cache entries periodically
	if time.Since(pm.lastCleanup) > pm.cacheTimeout {
		pm.cleanupCache()
		pm.lastCleanup = time.Now()
	}

	result := pm.rebaseToCacheImpl(path)
	pm.pathCache.Store(key, result)
	return result
}

// RebaseToSource converts a cache path to source path with caching
func (pm *PathManager) RebaseToSource(path string) string {
	key := "cache:" + path

	// Try to get from cache first
	if cached, exists := pm.pathCache.Load(key); exists {
		if result, ok := cached.(string); ok {
			return result
		}
	}

	// Clean up old cache entries periodically
	if time.Since(pm.lastCleanup) > pm.cacheTimeout {
		pm.cleanupCache()
		pm.lastCleanup = time.Now()
	}

	result := pm.rebaseToSourceImpl(path)
	pm.pathCache.Store(key, result)
	return result
}

// IsCachePath checks if a path is in the cache directory
func (pm *PathManager) IsCachePath(path string) bool {
	return strings.HasPrefix(path, pm.cacheRoot)
}

// IsSourcePath checks if a path is in the source directory
func (pm *PathManager) IsSourcePath(path string) bool {
	return strings.HasPrefix(path, pm.sourceRoot)
}

// GetSourceRoot returns the source root directory
func (pm *PathManager) GetSourceRoot() string {
	return pm.sourceRoot
}

// GetCacheRoot returns the cache root directory
func (pm *PathManager) GetCacheRoot() string {
	return pm.cacheRoot
}

// FullPath returns the full path for a relative path, choosing source or cache
func (pm *PathManager) FullPath(path string, useCache bool) string {
	if useCache {
		return filepath.Join(pm.cacheRoot, path)
	}
	return filepath.Join(pm.sourceRoot, path)
}

// rebaseToCacheImpl implements the actual path transformation
func (pm *PathManager) rebaseToCacheImpl(path string) string {
	if !strings.HasPrefix(path, pm.sourceRoot) {
		return path
	}

	relPath := strings.TrimPrefix(path, pm.sourceRoot)
	if relPath == "" {
		return pm.cacheRoot
	}

	relPath = strings.TrimPrefix(relPath, "/")

	return filepath.Join(pm.cacheRoot, relPath)
}

// rebaseToSourceImpl implements the actual path transformation
func (pm *PathManager) rebaseToSourceImpl(path string) string {
	if !strings.HasPrefix(path, pm.cacheRoot) {
		return path
	}

	relPath := strings.TrimPrefix(path, pm.cacheRoot)
	if relPath == "" {
		return pm.sourceRoot
	}

	relPath = strings.TrimPrefix(relPath, "/")

	return filepath.Join(pm.sourceRoot, relPath)
}

// cleanupCache removes old entries from the path cache
func (pm *PathManager) cleanupCache() {
	// Simple cleanup strategy: clear entire cache
	// sync.Map doesn't have len(), so we use a counter approach
	count := 0
	pm.pathCache.Range(func(key, value interface{}) bool {
		count++
		if count > 1000 {
			// Clear and start fresh
			pm.pathCache = sync.Map{}
			return false
		}
		return true
	})
}

// ClearCache clears the entire path cache
func (pm *PathManager) ClearCache() {
	pm.pathCache = sync.Map{}
}

// GetCacheStats returns statistics about the path cache
func (pm *PathManager) GetCacheStats() (size int, sourceEntries int, cacheEntries int) {
	pm.pathCache.Range(func(key, value interface{}) bool {
		size++
		if keyStr, ok := key.(string); ok {
			if strings.HasPrefix(keyStr, "src:") {
				sourceEntries++
			} else if strings.HasPrefix(keyStr, "cache:") {
				cacheEntries++
			}
		}
		return true
	})
	return
}
