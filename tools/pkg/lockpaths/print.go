package lockpaths

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AndroidGoLab/jni/tools/pkg/aarresolve"
)

// PrintField writes the requested field to w as a single space-separated
// line. Supported fields:
//
//   - "flat-args": every compiled flat file rendered as `-R /abs/path.flat`,
//     traversed in lock-sorted artifact order.
//   - "classes-jars": every extracted classes.jar absolute path, in
//     lock-sorted artifact order.
//
// Unknown fields are an error.
func PrintField(w io.Writer, lock *aarresolve.LockFile, cacheDir, field string) error {
	switch field {
	case "flat-args":
		return printFlatArgs(w, lock, cacheDir)
	case "classes-jars":
		return printClassesJars(w, lock, cacheDir)
	default:
		return fmt.Errorf("unsupported field %q (want flat-args|classes-jars)", field)
	}
}

// sortedArtifacts returns a copy of the artifact slice sorted by coordinate
// so callers cannot rely on (or break) the on-disk lock order.
func sortedArtifacts(lock *aarresolve.LockFile) []aarresolve.ArtifactEntry {
	out := make([]aarresolve.ArtifactEntry, len(lock.Artifacts))
	copy(out, lock.Artifacts)
	sort.Slice(out, func(i, j int) bool { return out[i].Coordinate < out[j].Coordinate })
	return out
}

func printFlatArgs(w io.Writer, lock *aarresolve.LockFile, cacheDir string) error {
	var args []string
	for _, e := range sortedArtifacts(lock) {
		dir := CompiledDir(cacheDir, &e)
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", dir, err)
		}
		var flats []string
		for _, ent := range entries {
			if ent.IsDir() {
				continue
			}
			if !strings.HasSuffix(ent.Name(), ".flat") {
				continue
			}
			abs, err := filepath.Abs(filepath.Join(dir, ent.Name()))
			if err != nil {
				return fmt.Errorf("abs %s: %w", ent.Name(), err)
			}
			flats = append(flats, abs)
		}
		sort.Strings(flats)
		for _, f := range flats {
			args = append(args, "-R", f)
		}
	}
	_, err := fmt.Fprint(w, strings.Join(args, " "))
	return err
}

func printClassesJars(w io.Writer, lock *aarresolve.LockFile, cacheDir string) error {
	var paths []string
	for _, e := range sortedArtifacts(lock) {
		jar := filepath.Join(ExtractedDir(cacheDir, &e), classesJarEntry)
		if _, err := os.Stat(jar); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat %s: %w", jar, err)
		}
		abs, err := filepath.Abs(jar)
		if err != nil {
			return fmt.Errorf("abs %s: %w", jar, err)
		}
		paths = append(paths, abs)
	}
	_, err := fmt.Fprint(w, strings.Join(paths, " "))
	return err
}
