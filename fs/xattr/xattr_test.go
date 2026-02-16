package xattr

import (
	"strings"
	"testing"

	tu "github.com/pleclech/shadowfs/fs/utils/testings"
)

func TestXAttr_ToBytes_FromBytes(t *testing.T) {
	attr := XAttr{
		PathStatus: PathStatusDeleted,
	}

	// Convert to bytes
	bytes := ToBytes(&attr)
	if len(bytes) != Size {
		tu.Failf(t, "Expected bytes length %d, got %d", Size, len(bytes))
	}

	// Convert back from bytes
	attr2 := FromBytes(bytes)
	if attr2.PathStatus != PathStatusDeleted {
		tu.Failf(t, "Expected PathStatus %d, got %d", PathStatusDeleted, attr2.PathStatus)
	}
}

func TestXAttr_FromBytes_Short(t *testing.T) {
	// Test with short byte slice
	shortBytes := []byte{0, 1}
	attr := FromBytes(shortBytes)

	// Should return zero-initialized struct
	if attr.PathStatus != PathStatusNone {
		tu.Failf(t, "Expected PathStatus %d for short bytes, got %d", PathStatusNone, attr.PathStatus)
	}
}

func TestIsPathDeleted(t *testing.T) {
	tests := []struct {
		name     string
		attr     XAttr
		expected bool
	}{
		{
			name:     "deleted path",
			attr:     XAttr{PathStatus: PathStatusDeleted},
			expected: true,
		},
		{
			name:     "none status",
			attr:     XAttr{PathStatus: PathStatusNone},
			expected: false,
		},
		{
			name:     "zero value",
			attr:     XAttr{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsPathDeleted(tt.attr)
			if result != tt.expected {
				tu.Failf(
					t, "IsPathDeleted() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestXAttr_SetOriginalSourcePath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "normal path",
			path:     "/home/user/file.txt",
			expected: "/home/user/file.txt",
		},
		{
			name:     "empty path",
			path:     "",
			expected: "",
		},
		{
			name:     "path at max length",
			path:     strings.Repeat("a", MaxPathLength),
			expected: strings.Repeat("a", MaxPathLength),
		},
		{
			name:     "path exceeds max length - should truncate",
			path:     strings.Repeat("b", MaxPathLength+100),
			expected: strings.Repeat("b", MaxPathLength),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attr := &XAttr{}
			attr.SetOriginalSourcePath(tt.path)
			result := attr.GetOriginalSourcePath()
			if result != tt.expected {
				tu.Failf(t, "GetOriginalSourcePath() = %q (len=%d), expected %q (len=%d)",
					result, len(result), tt.expected, len(tt.expected))
			}
		})
	}
}

func TestXAttr_GetOriginalSourcePath_EmptyArray(t *testing.T) {
	attr := &XAttr{}
	result := attr.GetOriginalSourcePath()
	if result != "" {
		tu.Failf(t, "Expected empty string for zero-initialized array, got %q", result)
	}
}

func TestXAttr_SetRenamedFromPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "normal path",
			path:     "/old/path/file.txt",
			expected: "/old/path/file.txt",
		},
		{
			name:     "empty path",
			path:     "",
			expected: "",
		},
		{
			name:     "path at max length",
			path:     strings.Repeat("c", MaxPathLength),
			expected: strings.Repeat("c", MaxPathLength),
		},
		{
			name:     "path exceeds max length - should truncate",
			path:     strings.Repeat("d", MaxPathLength+50),
			expected: strings.Repeat("d", MaxPathLength),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attr := &XAttr{}
			attr.SetRenamedFromPath(tt.path)
			result := attr.GetRenamedFromPath()
			if result != tt.expected {
				tu.Failf(t, "GetRenamedFromPath() = %q (len=%d), expected %q (len=%d)",
					result, len(result), tt.expected, len(tt.expected))
			}
		})
	}
}

func TestXAttr_GetRenamedFromPath_EmptyArray(t *testing.T) {
	attr := &XAttr{}
	result := attr.GetRenamedFromPath()
	if result != "" {
		tu.Failf(t, "Expected empty string for zero-initialized array, got %q", result)
	}
}

func TestXAttr_PathOverwrite(t *testing.T) {
	// Test that setting a new path clears the old one
	attr := &XAttr{}

	// Set first path
	attr.SetOriginalSourcePath("/first/path/very/long/name.txt")
	first := attr.GetOriginalSourcePath()
	if first != "/first/path/very/long/name.txt" {
		tu.Failf(t, "First path not set correctly: %q", first)
	}

	// Set shorter path - should clear old bytes
	attr.SetOriginalSourcePath("/short")
	second := attr.GetOriginalSourcePath()
	if second != "/short" {
		tu.Failf(t, "Second path not set correctly: %q (len=%d)", second, len(second))
	}
}

func TestStoreRenameMapping(t *testing.T) {
	mappings := []RenameMapping{}

	// Add first mapping
	mappings = StoreRenameMapping(mappings, "/old/path", "/new/path", 1)
	if len(mappings) != 1 {
		tu.Failf(t, "Expected 1 mapping, got %d", len(mappings))
	}
	if mappings[0].OriginalPath != "/old/path" {
		tu.Failf(t, "Expected OriginalPath '/old/path', got %q", mappings[0].OriginalPath)
	}
	if mappings[0].CurrentPath != "/new/path" {
		tu.Failf(t, "Expected CurrentPath '/new/path', got %q", mappings[0].CurrentPath)
	}
	if mappings[0].Depth != 1 {
		tu.Failf(t, "Expected Depth 1, got %d", mappings[0].Depth)
	}
	if mappings[0].Timestamp == 0 {
		tu.Failf(t, "Expected non-zero Timestamp")
	}

	// Add second mapping
	mappings = StoreRenameMapping(mappings, "/another/old", "/another/new", 2)
	if len(mappings) != 2 {
		tu.Failf(t, "Expected 2 mappings, got %d", len(mappings))
	}
}

func TestResolveRenamedPath(t *testing.T) {
	tests := []struct {
		name         string
		mappings     []RenameMapping
		originalPath string
		expected     string
	}{
		{
			name:         "no mappings",
			mappings:     []RenameMapping{},
			originalPath: "/some/path",
			expected:     "/some/path",
		},
		{
			name: "simple rename",
			mappings: []RenameMapping{
				{OriginalPath: "/old", CurrentPath: "/new", Depth: 1},
			},
			originalPath: "/old/file.txt",
			expected:     "/new/file.txt",
		},
		{
			name: "no matching prefix",
			mappings: []RenameMapping{
				{OriginalPath: "/different", CurrentPath: "/new", Depth: 1},
			},
			originalPath: "/old/file.txt",
			expected:     "/old/file.txt",
		},
		{
			name: "nested rename",
			mappings: []RenameMapping{
				{OriginalPath: "/a", CurrentPath: "/b", Depth: 1},
				{OriginalPath: "/b/c", CurrentPath: "/b/d", Depth: 2},
			},
			originalPath: "/a/c/file.txt",
			expected:     "/b/d/file.txt",
		},
		{
			name: "chained renames",
			mappings: []RenameMapping{
				{OriginalPath: "/level1", CurrentPath: "/level2", Depth: 1},
				{OriginalPath: "/level2", CurrentPath: "/level3", Depth: 2},
			},
			originalPath: "/level1/file.txt",
			expected:     "/level3/file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ResolveRenamedPath(tt.mappings, tt.originalPath)
			if result != tt.expected {
				tu.Failf(t, "ResolveRenamedPath() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

func TestFindRenameMapping(t *testing.T) {
	tests := []struct {
		name         string
		mappings     []RenameMapping
		path         string
		expectFound  bool
		expectedOrig string
	}{
		{
			name:        "no mappings",
			mappings:    []RenameMapping{},
			path:        "/some/path",
			expectFound: false,
		},
		{
			name: "matching prefix",
			mappings: []RenameMapping{
				{OriginalPath: "/old", CurrentPath: "/new", Depth: 1, Timestamp: 100},
			},
			path:         "/old/file.txt",
			expectFound:  true,
			expectedOrig: "/old",
		},
		{
			name: "no matching prefix",
			mappings: []RenameMapping{
				{OriginalPath: "/different", CurrentPath: "/new", Depth: 1, Timestamp: 100},
			},
			path:        "/old/file.txt",
			expectFound: false,
		},
		{
			name: "multiple matches - returns latest",
			mappings: []RenameMapping{
				{OriginalPath: "/old", CurrentPath: "/new1", Depth: 1, Timestamp: 100},
				{OriginalPath: "/old", CurrentPath: "/new2", Depth: 2, Timestamp: 200},
			},
			path:         "/old/file.txt",
			expectFound:  true,
			expectedOrig: "/old",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FindRenameMapping(tt.mappings, tt.path)
			if tt.expectFound {
				if result == nil {
					tu.Failf(t, "Expected to find mapping, got nil")
				}
				if result.OriginalPath != tt.expectedOrig {
					tu.Failf(t, "Expected OriginalPath %q, got %q", tt.expectedOrig, result.OriginalPath)
				}
			} else {
				if result != nil {
					tu.Failf(t, "Expected nil, got mapping with OriginalPath %q", result.OriginalPath)
				}
			}
		})
	}
}

func TestFindRenameMapping_LatestTimestamp(t *testing.T) {
	mappings := []RenameMapping{
		{OriginalPath: "/old", CurrentPath: "/new1", Depth: 1, Timestamp: 100},
		{OriginalPath: "/old", CurrentPath: "/new2", Depth: 2, Timestamp: 300},
		{OriginalPath: "/old", CurrentPath: "/new3", Depth: 3, Timestamp: 200},
	}

	result := FindRenameMapping(mappings, "/old/file.txt")
	if result == nil {
		tu.Failf(t, "Expected to find mapping")
	}
	// Should return the one with highest timestamp (300)
	if result.CurrentPath != "/new2" {
		tu.Failf(t, "Expected CurrentPath '/new2' (latest timestamp), got %q", result.CurrentPath)
	}
}

func TestXAttr_AllFields(t *testing.T) {
	// Test round-trip of all fields
	original := XAttr{
		PathStatus:        PathStatusDeleted,
		OriginalType:      0o100644, // Regular file
		CurrentType:       0o040755, // Directory
		RenameDepth:       3,
		DeletedFromSource: true,
		CacheIndependent:  true,
		TypeReplaced:      true,
	}
	original.SetOriginalSourcePath("/original/source/path")
	original.SetRenamedFromPath("/renamed/from/path")

	// Convert to bytes and back
	bytes := ToBytes(&original)
	restored := FromBytes(bytes)

	if restored.PathStatus != original.PathStatus {
		tu.Failf(t, "PathStatus mismatch: %d vs %d", restored.PathStatus, original.PathStatus)
	}
	if restored.OriginalType != original.OriginalType {
		tu.Failf(t, "OriginalType mismatch: %o vs %o", restored.OriginalType, original.OriginalType)
	}
	if restored.CurrentType != original.CurrentType {
		tu.Failf(t, "CurrentType mismatch: %o vs %o", restored.CurrentType, original.CurrentType)
	}
	if restored.RenameDepth != original.RenameDepth {
		tu.Failf(t, "RenameDepth mismatch: %d vs %d", restored.RenameDepth, original.RenameDepth)
	}
	if restored.DeletedFromSource != original.DeletedFromSource {
		tu.Failf(t, "DeletedFromSource mismatch: %v vs %v", restored.DeletedFromSource, original.DeletedFromSource)
	}
	if restored.CacheIndependent != original.CacheIndependent {
		tu.Failf(t, "CacheIndependent mismatch: %v vs %v", restored.CacheIndependent, original.CacheIndependent)
	}
	if restored.TypeReplaced != original.TypeReplaced {
		tu.Failf(t, "TypeReplaced mismatch: %v vs %v", restored.TypeReplaced, original.TypeReplaced)
	}
	if restored.GetOriginalSourcePath() != original.GetOriginalSourcePath() {
		tu.Failf(t, "OriginalSourcePath mismatch: %q vs %q",
			restored.GetOriginalSourcePath(), original.GetOriginalSourcePath())
	}
	if restored.GetRenamedFromPath() != original.GetRenamedFromPath() {
		tu.Failf(t, "RenamedFromPath mismatch: %q vs %q",
			restored.GetRenamedFromPath(), original.GetRenamedFromPath())
	}
}

func TestPathStatus_Constants(t *testing.T) {
	// Verify PathStatus constants have expected values
	if PathStatusNone != 0 {
		tu.Failf(t, "Expected PathStatusNone = 0, got %d", PathStatusNone)
	}
	if PathStatusDeleted != 1 {
		tu.Failf(t, "Expected PathStatusDeleted = 1, got %d", PathStatusDeleted)
	}
}

func TestXAttr_Name_Constant(t *testing.T) {
	expected := "user.shadow-fs"
	if Name != expected {
		tu.Failf(t, "Expected Name = %q, got %q", expected, Name)
	}
}

func TestXAttr_MaxPathLength_Constant(t *testing.T) {
	if MaxPathLength != 512 {
		tu.Failf(t, "Expected MaxPathLength = 512, got %d", MaxPathLength)
	}
}
