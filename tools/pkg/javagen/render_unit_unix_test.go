//go:build !windows

package javagen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanStaleJavaFiles_PropagatesReadFileError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Broken.java")
	if err := os.Symlink(filepath.Join(dir, "missing-target"), path); err != nil {
		t.Fatalf("create dangling symlink fixture: %v", err)
	}

	err := cleanStaleJavaFiles(dir)
	if err == nil {
		t.Fatal("expected read-file error")
	}
	if !strings.Contains(err.Error(), "read stale Java file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCleanStaleJavaFiles_PropagatesRemoveError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Generated.java")
	if err := os.WriteFile(path, []byte(javaGeneratedHeader+"public class Generated {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatalf("restore dir perms: %v", err)
		}
	}()

	err := cleanStaleJavaFiles(dir)
	if err == nil {
		t.Fatal("expected remove error")
	}
	if !strings.Contains(err.Error(), "remove stale Java file") {
		t.Fatalf("unexpected error: %v", err)
	}
}
