package lockpaths

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AndroidGoLab/jni/tools/pkg/aarresolve"
)

// CompileAll runs `aapt2 compile --dir <res> -o <out>` for every artifact
// whose extracted res/ directory exists and is non-empty. Output flats land
// under <cacheDir>/compiled/<group_slashes>/<artifact>/<version>/.
//
// aapt2Path must be the absolute path to a usable aapt2 binary.
func CompileAll(lock *aarresolve.LockFile, cacheDir, aapt2Path string) error {
	if aapt2Path == "" {
		return fmt.Errorf("aapt2 path is required")
	}
	for i := range lock.Artifacts {
		if err := compileOne(&lock.Artifacts[i], cacheDir, aapt2Path); err != nil {
			return fmt.Errorf("compile %s: %w", lock.Artifacts[i].Coordinate, err)
		}
	}
	return nil
}

// CompiledDir returns the absolute compiled flats directory for one artifact.
func CompiledDir(cacheDir string, e *aarresolve.ArtifactEntry) string {
	groupSlashes := strings.ReplaceAll(e.Group, ".", "/")
	return filepath.Join(cacheDir, "compiled", groupSlashes, e.Artifact, e.Version)
}

func compileOne(e *aarresolve.ArtifactEntry, cacheDir, aapt2Path string) error {
	res, ok := ResDirIfPopulated(cacheDir, e)
	if !ok {
		return nil
	}
	out := CompiledDir(cacheDir, e)
	if err := os.MkdirAll(out, dirPerm); err != nil {
		return fmt.Errorf("mkdir %s: %w", out, err)
	}
	cmd := exec.Command(aapt2Path, "compile", "--dir", res, "-o", out)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("aapt2 compile %s failed: %w\n%s", e.Coordinate, err, combined)
	}
	return nil
}
