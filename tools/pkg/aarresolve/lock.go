package aarresolve

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// LockFile is the on-disk representation of a resolved closure.
type LockFile struct {
	GeneratedBy string          `json:"generated_by"`
	TopLevel    []string        `json:"top_level"`
	Artifacts   []ArtifactEntry `json:"artifacts"`
}

// ArtifactEntry describes a single resolved Maven artifact in the closure.
type ArtifactEntry struct {
	Coordinate   string   `json:"coordinate"`
	Group        string   `json:"group"`
	Artifact     string   `json:"artifact"`
	Version      string   `json:"version"`
	Packaging    string   `json:"packaging"`
	Repo         string   `json:"repo"`
	Path         string   `json:"path"`
	SHA256       string   `json:"sha256"`
	POMPath      string   `json:"pom_path"`
	POMSHA256    string   `json:"pom_sha256"`
	Dependencies []string `json:"dependencies"`
}

// Write sorts Artifacts by group:artifact, marshals the LockFile as indented
// JSON with a trailing newline, and atomically writes it to path.
func (l *LockFile) Write(path string) error {
	sort.Slice(l.Artifacts, func(i, j int) bool {
		return l.Artifacts[i].Coordinate < l.Artifacts[j].Coordinate
	})
	for i := range l.Artifacts {
		sort.Strings(l.Artifacts[i].Dependencies)
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lock: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), cacheDirPerm); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, cacheFilePerm); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}
