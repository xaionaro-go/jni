// Package lockpaths consumes a lock file produced by tools/cmd/aar-resolve
// and turns the on-disk artifact cache into a build-ready surface: extracted
// classes.jar + res/ trees per artifact, aapt2-compiled flat resource files,
// and ordered absolute-path lists for Make to feed into javac/d8/aapt2 link.
package lockpaths

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/AndroidGoLab/jni/tools/pkg/aarresolve"
)

const (
	// dirPerm is used for every directory the package creates.
	dirPerm = 0o755
	// filePerm is used for every regular file the package writes.
	filePerm = 0o644
	// classesJarEntry is the AAR-internal path of the bundled classes JAR.
	classesJarEntry = "classes.jar"
	// resPrefix is the AAR-internal directory holding XML resources.
	resPrefix = "res/"
)

// ExtractAll walks every artifact in lock and unpacks the cached AAR/JAR
// under cacheDir. AAR artifacts contribute classes.jar (when present) plus
// the res/ tree; JAR artifacts are copied verbatim as classes.jar. POM-only
// artifacts (no .aar/.jar payload) are skipped.
//
// Layout under cacheDir:
//
//	extracted/<group_slashes>/<artifact>/<version>/classes.jar
//	extracted/<group_slashes>/<artifact>/<version>/res/...
func ExtractAll(lock *aarresolve.LockFile, cacheDir string) error {
	for i := range lock.Artifacts {
		if err := extractOne(&lock.Artifacts[i], cacheDir); err != nil {
			return fmt.Errorf("extract %s: %w", lock.Artifacts[i].Coordinate, err)
		}
	}
	return nil
}

// ExtractedDir returns the absolute extracted directory for one artifact.
func ExtractedDir(cacheDir string, e *aarresolve.ArtifactEntry) string {
	groupSlashes := strings.ReplaceAll(e.Group, ".", "/")
	return filepath.Join(cacheDir, "extracted", groupSlashes, e.Artifact, e.Version)
}

// extractOne dispatches by packaging.
func extractOne(e *aarresolve.ArtifactEntry, cacheDir string) error {
	switch e.Packaging {
	case "aar":
		return extractAAR(e, cacheDir)
	case "jar":
		return extractJAR(e, cacheDir)
	case "pom":
		return nil
	default:
		return fmt.Errorf("unsupported packaging %q", e.Packaging)
	}
}

// extractAAR unzips classes.jar (if present) and the res/ tree of one AAR
// into the per-artifact extracted directory.
func extractAAR(e *aarresolve.ArtifactEntry, cacheDir string) error {
	src := filepath.Join(cacheDir, e.Path)
	dst := ExtractedDir(cacheDir, e)
	if err := os.MkdirAll(dst, dirPerm); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}

	zr, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = zr.Close() }()

	for _, f := range zr.File {
		name := f.Name
		switch {
		case name == classesJarEntry:
			if err := extractZipFile(f, filepath.Join(dst, classesJarEntry)); err != nil {
				return err
			}
		case strings.HasPrefix(name, resPrefix):
			rel := strings.TrimPrefix(name, resPrefix)
			if rel == "" {
				continue
			}
			out := filepath.Join(dst, "res", rel)
			if f.FileInfo().IsDir() {
				if err := os.MkdirAll(out, dirPerm); err != nil {
					return fmt.Errorf("mkdir %s: %w", out, err)
				}
				continue
			}
			if err := extractZipFile(f, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// extractJAR copies the cached JAR verbatim to <extracted>/classes.jar so
// callers can treat AAR-classes-jar and standalone-jar artifacts uniformly.
func extractJAR(e *aarresolve.ArtifactEntry, cacheDir string) error {
	src := filepath.Join(cacheDir, e.Path)
	dst := ExtractedDir(cacheDir, e)
	if err := os.MkdirAll(dst, dirPerm); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}
	return copyFile(src, filepath.Join(dst, classesJarEntry))
}

// extractZipFile materializes a single zip entry on disk, creating any
// missing parent directories.
func extractZipFile(f *zip.File, out string) (_err error) {
	if err := os.MkdirAll(filepath.Dir(out), dirPerm); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(out), err)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open zip entry %s: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()

	w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
	if err != nil {
		return fmt.Errorf("create %s: %w", out, err)
	}
	defer func() {
		if cerr := w.Close(); cerr != nil && _err == nil {
			_err = fmt.Errorf("close %s: %w", out, cerr)
		}
	}()
	if _, err := io.Copy(w, rc); err != nil {
		return fmt.Errorf("copy %s: %w", out, err)
	}
	return nil
}

// copyFile streams src to dst with a fresh file mode; used for JAR copy.
func copyFile(src, dst string) (_err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && _err == nil {
			_err = fmt.Errorf("close %s: %w", dst, cerr)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	return nil
}

// ResDirIfPopulated returns the artifact's extracted res/ directory when it
// exists and is non-empty; otherwise it returns ("", false).
func ResDirIfPopulated(cacheDir string, e *aarresolve.ArtifactEntry) (string, bool) {
	res := filepath.Join(ExtractedDir(cacheDir, e), "res")
	entries, err := os.ReadDir(res)
	switch {
	case err == nil:
		if len(entries) == 0 {
			return "", false
		}
		return res, true
	case errors.Is(err, os.ErrNotExist):
		return "", false
	default:
		return "", false
	}
}
