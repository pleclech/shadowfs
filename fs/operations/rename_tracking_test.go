package operations

import (
	"path/filepath"
	"testing"
)

func TestRenameTrie_SimpleRename(t *testing.T) {
	trie := NewRenameTrie()
	
	// Insert: foo/baz -> foo/bar
	trie.Insert("foo/baz", "foo/bar", false)
	
	// Resolve: foo/baz -> foo/bar
	resolved, independent, found := trie.Resolve("foo/baz")
	if !found {
		t.Fatalf("Expected to find rename mapping for 'foo/baz'")
	}
	if independent {
		t.Fatalf("Expected path to not be independent")
	}
	if resolved != "foo/bar" {
		t.Fatalf("Expected resolved path 'foo/bar', got '%s'", resolved)
	}
	
	// Resolve nested: foo/baz/hello -> foo/bar/hello
	resolved, _, found = trie.Resolve("foo/baz/hello")
	if !found {
		t.Fatalf("Expected to find rename mapping for 'foo/baz/hello'")
	}
	if resolved != "foo/bar/hello" {
		t.Fatalf("Expected resolved path 'foo/bar/hello', got '%s'", resolved)
	}
}

func TestRenameTrie_NestedRename(t *testing.T) {
	trie := NewRenameTrie()
	
	// Insert: foo/baz -> foo/bar
	trie.Insert("foo/baz", "foo/bar", false)
	
	// Insert nested: foo/baz/hello/you -> foo/bar/hello/world
	trie.Insert("foo/baz/hello/you", "foo/bar/hello/world", false)
	
	// Resolve: foo/baz/hello/you/file.txt -> foo/bar/hello/world/file.txt
	resolved, _, found := trie.Resolve("foo/baz/hello/you/file.txt")
	if !found {
		t.Fatalf("Expected to find rename mapping for 'foo/baz/hello/you/file.txt'")
	}
	expected := filepath.Join("foo/bar/hello/world", "file.txt")
	if resolved != expected {
		t.Fatalf("Expected resolved path '%s', got '%s'", expected, resolved)
	}
}

func TestRenameTrie_MultipleSequentialRenames(t *testing.T) {
	trie := NewRenameTrie()
	
	// First rename: foo/bar -> foo/baz
	trie.Insert("foo/baz", "foo/bar", false)
	
	// Second rename: foo/baz -> foo/qux (same path renamed again)
	trie.Insert("foo/qux", "foo/baz", false)
	
	// Resolve: foo/qux -> foo/baz -> foo/bar
	// Should use the most recent rename (foo/qux -> foo/baz)
	resolved, _, found := trie.Resolve("foo/qux")
	if !found {
		t.Fatalf("Expected to find rename mapping for 'foo/qux'")
	}
	if resolved != "foo/baz" {
		t.Fatalf("Expected resolved path 'foo/baz', got '%s'", resolved)
	}
	
	// Resolve nested: foo/qux/hello -> foo/baz/hello
	resolved, _, found = trie.Resolve("foo/qux/hello")
	if !found {
		t.Fatalf("Expected to find rename mapping for 'foo/qux/hello'")
	}
	if resolved != "foo/baz/hello" {
		t.Fatalf("Expected resolved path 'foo/baz/hello', got '%s'", resolved)
	}
}

func TestRenameTrie_IndependentPath(t *testing.T) {
	trie := NewRenameTrie()
	
	// Insert independent path: foo/baz -> foo/bar (independent)
	trie.Insert("foo/baz", "foo/bar", true)
	
	// Resolve: should return independent=true
	_, independent, found := trie.Resolve("foo/baz")
	if !found {
		t.Fatalf("Expected to find rename mapping for 'foo/baz'")
	}
	if !independent {
		t.Fatalf("Expected path to be independent")
	}
	
	// Resolve nested: should also return independent=true
	_, independent, found = trie.Resolve("foo/baz/hello")
	if !found {
		t.Fatalf("Expected to find rename mapping for 'foo/baz/hello'")
	}
	if !independent {
		t.Fatalf("Expected nested path to be independent")
	}
}

func TestRenameTrie_LongestPrefixMatch(t *testing.T) {
	trie := NewRenameTrie()
	
	// Insert shorter prefix: foo -> bar
	trie.Insert("foo", "bar", false)
	
	// Insert longer prefix: foo/baz -> foo/bar
	trie.Insert("foo/baz", "foo/bar", false)
	
	// Resolve: foo/baz/hello should use longest match (foo/baz)
	resolved, _, found := trie.Resolve("foo/baz/hello")
	if !found {
		t.Fatalf("Expected to find rename mapping for 'foo/baz/hello'")
	}
	expected := filepath.Join("foo/bar", "hello")
	if resolved != expected {
		t.Fatalf("Expected resolved path '%s', got '%s'", expected, resolved)
	}
	
	// Resolve: foo/other should use shorter match (foo)
	resolved, _, found = trie.Resolve("foo/other")
	if !found {
		t.Fatalf("Expected to find rename mapping for 'foo/other'")
	}
	if resolved != "bar/other" {
		t.Fatalf("Expected resolved path 'bar/other', got '%s'", resolved)
	}
}

func TestRenameTrie_Remove(t *testing.T) {
	trie := NewRenameTrie()
	
	// Insert: foo/baz -> foo/bar
	trie.Insert("foo/baz", "foo/bar", false)
	
	// Verify it exists
	resolved, _, found := trie.Resolve("foo/baz")
	if !found || resolved != "foo/bar" {
		t.Fatalf("Expected rename mapping to exist")
	}
	
	// Remove it
	trie.Remove("foo/baz")
	
	// Verify it's gone
	_, _, found = trie.Resolve("foo/baz")
	if found {
		t.Fatalf("Expected rename mapping to be removed")
	}
}

func TestRenameTrie_SetIndependent(t *testing.T) {
	trie := NewRenameTrie()
	
	// Insert: foo/baz -> foo/bar (not independent)
	trie.Insert("foo/baz", "foo/bar", false)
	
	// Verify it's not independent
	_, independent, found := trie.Resolve("foo/baz")
	if !found || independent {
		t.Fatalf("Expected path to not be independent")
	}
	
	// Mark as independent
	trie.SetIndependent("foo/baz")
	
	// Verify it's now independent
	_, independent, found = trie.Resolve("foo/baz")
	if !found || !independent {
		t.Fatalf("Expected path to be independent")
	}
}

func TestRenameTracker_MemoryFirst(t *testing.T) {
	tracker := NewRenameTracker()
	
	// Store mapping in memory
	tracker.StoreRenameMapping("foo/bar", "foo/baz", false)
	
	// Resolve should find it in memory
	resolved, independent, found := tracker.ResolveRenamedPath("foo/baz")
	if !found {
		t.Fatalf("Expected to find rename mapping in memory")
	}
	if independent {
		t.Fatalf("Expected path to not be independent")
	}
	if resolved != "foo/bar" {
		t.Fatalf("Expected resolved path 'foo/bar', got '%s'", resolved)
	}
	
	// Remove mapping
	tracker.RemoveRenameMapping("foo/baz")
	
	// Resolve should not find it
	_, _, found = tracker.ResolveRenamedPath("foo/baz")
	if found {
		t.Fatalf("Expected rename mapping to be removed")
	}
}

func TestRenameTracker_SetIndependent(t *testing.T) {
	tracker := NewRenameTracker()
	
	// Store mapping
	tracker.StoreRenameMapping("foo/bar", "foo/baz", false)
	
	// Mark as independent
	tracker.SetIndependent("foo/baz")
	
	// Resolve should return independent=true
	_, independent, found := tracker.ResolveRenamedPath("foo/baz")
	if !found {
		t.Fatalf("Expected to find rename mapping")
	}
	if !independent {
		t.Fatalf("Expected path to be independent")
	}
}

