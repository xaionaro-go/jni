package aarresolve

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	cacheDirPerm  = 0o755
	cacheFilePerm = 0o644
)

// CacheFile writes data to relPath under cacheDir, computes the SHA-256 of
// data, and returns the hex digest. If a file already exists at the target
// path with a matching SHA-256, no write occurs.
//
// When verifyOnly is true, the function never writes: a missing file or a
// mismatched SHA-256 returns an error. This is the gate used by CI to detect
// corrupted or stale caches.
//
// The write is atomic via "<path>.tmp" + os.Rename, so a partial write cannot
// leave a half-written file in the cache.
func CacheFile(cacheDir, relPath string, data []byte, verifyOnly bool) (string, error) {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	full := filepath.Join(cacheDir, relPath)

	existing, err := os.ReadFile(full)
	switch {
	case err == nil:
		existingSum := sha256.Sum256(existing)
		if hex.EncodeToString(existingSum[:]) == digest {
			return digest, nil
		}
		if verifyOnly {
			return "", fmt.Errorf("cached file %s SHA-256 mismatch: got %x, want %s",
				full, existingSum[:], digest)
		}
	case errors.Is(err, os.ErrNotExist):
		if verifyOnly {
			return "", fmt.Errorf("cached file %s missing (verify-only mode)", full)
		}
	default:
		return "", fmt.Errorf("read cache %s: %w", full, err)
	}

	if err := os.MkdirAll(filepath.Dir(full), cacheDirPerm); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(full), err)
	}
	tmp := full + ".tmp"
	if err := os.WriteFile(tmp, data, cacheFilePerm); err != nil {
		return "", fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, full); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename %s -> %s: %w", tmp, full, err)
	}
	return digest, nil
}
