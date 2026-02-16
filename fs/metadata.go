package fs

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Metadata represents metadata stored in .shadowfs-metadata/ directory
type Metadata struct {
	RenamedFrom      string `json:"renamedFrom,omitempty"` // If renamed (omitted if not renamed)
	SourcePath       string `json:"sourcePath"`            // Original source path
	Deleted          bool   `json:"deleted"`               // If deleted
	CacheIndependent bool   `json:"cacheIndependent"`      // If independent
}

// metadataDir is the directory where metadata files are stored
const MetadataDir = ".shadowfs-metadata"

// getMetadataPath returns the path to the metadata file for a given relative path
func (gm *GitManager) getRelativeMetadataPath(relativePath string) string {
	if strings.HasPrefix(relativePath, MetadataDir+"/") && strings.HasSuffix(relativePath, ".json") {
		return relativePath
	}
	return filepath.Join(gm.workspacePath, MetadataDir, relativePath+".json")
}

// storeXAttrMetadata stores metadata as a JSON file in .shadowfs-metadata/
func (gm *GitManager) storeXAttrMetadata(metadataPath string, metadata Metadata) error {
	// Ensure parent directory exists
	parentDir := filepath.Dir(metadataPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("failed to create metadata directory %s: %w", parentDir, err)
	}

	// Marshal metadata to JSON
	jsonData, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Write metadata file
	if err := os.WriteFile(metadataPath, jsonData, 0644); err != nil {
		return fmt.Errorf("failed to write metadata file %s: %w", metadataPath, err)
	}

	Debug("storeXAttrMetadata: Stored metadata at %s", metadataPath)
	return nil
}

// loadXAttrMetadataFromCommit loads metadata from a specific commit
func (gm *GitManager) loadXAttrMetadataFromCommit(commitHash, relativePath string) (string, *Metadata, error) {
	metadataPath := gm.getRelativeMetadataPath(relativePath)

	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()

	errChan := make(chan error, 1)
	go func() {
		err := gm.executeGitCommand([]string{"show", commitHash + ":" + metadataPath}, w, os.Stderr)
		w.Close()
		errChan <- err
	}()

	var metadata Metadata
	if err := json.NewDecoder(r).Decode(&metadata); err != nil {
		// Check if the error is because the file doesn't exist in this commit
		gitErr := <-errChan
		if gitErr != nil && strings.Contains(gitErr.Error(), "does not exist") {
			Debug("loadXAttrMetadataFromCommit: No metadata for %s at commit %s (file not in commit)", relativePath, commitHash)
			return metadataPath, nil, nil
		}
		return metadataPath, nil, fmt.Errorf("failed to decode metadata from commit %s: %w", commitHash, err)
	}

	return metadataPath, &metadata, nil
}
