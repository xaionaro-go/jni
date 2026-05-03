package lockpaths

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AndroidGoLab/jni/tools/pkg/aarresolve"
)

// fakeLock builds a 3-artifact closure (one AAR with classes.jar + res, one
// AAR with classes.jar but no res, one standalone JAR) and writes the
// matching cached files under cacheDir using the same on-disk layout as
// aar-resolve. The returned LockFile mirrors a cycle-2 lock.json shape.
func fakeLock(t *testing.T, cacheDir string) *aarresolve.LockFile {
	t.Helper()
	lock := &aarresolve.LockFile{
		Artifacts: []aarresolve.ArtifactEntry{
			{
				Coordinate: "com.example:full:1.0.0",
				Group:      "com.example",
				Artifact:   "full",
				Version:    "1.0.0",
				Packaging:  "aar",
				Path:       "com/example/full/1.0.0/full-1.0.0.aar",
			},
			{
				Coordinate: "com.example:no-res:1.0.0",
				Group:      "com.example",
				Artifact:   "no-res",
				Version:    "1.0.0",
				Packaging:  "aar",
				Path:       "com/example/no-res/1.0.0/no-res-1.0.0.aar",
			},
			{
				Coordinate: "com.example:plainjar:1.0.0",
				Group:      "com.example",
				Artifact:   "plainjar",
				Version:    "1.0.0",
				Packaging:  "jar",
				Path:       "com/example/plainjar/1.0.0/plainjar-1.0.0.jar",
			},
		},
	}
	for _, e := range lock.Artifacts {
		full := filepath.Join(cacheDir, e.Path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	writeAAR(t, filepath.Join(cacheDir, lock.Artifacts[0].Path), map[string][]byte{
		"classes.jar":           dummyJAR(t, "Foo.class"),
		"AndroidManifest.xml":   []byte("<manifest/>"),
		"res/values/strings.xml": []byte(`<?xml version="1.0"?><resources><string name="hi">Hello</string></resources>`),
		"res/layout/main.xml":   []byte(`<?xml version="1.0"?><LinearLayout/>`),
	})
	writeAAR(t, filepath.Join(cacheDir, lock.Artifacts[1].Path), map[string][]byte{
		"classes.jar":         dummyJAR(t, "Bar.class"),
		"AndroidManifest.xml": []byte("<manifest/>"),
	})
	if err := os.WriteFile(filepath.Join(cacheDir, lock.Artifacts[2].Path), dummyJAR(t, "Baz.class"), 0o644); err != nil {
		t.Fatalf("write jar: %v", err)
	}
	return lock
}

// writeAAR builds a zip with the given entries at path. AARs are zip files,
// so this is exactly what the real Maven artifacts look like to ExtractAll.
func writeAAR(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// dummyJAR returns a zip containing a single named entry with sentinel
// content so jar identity can be verified after extract.
func dummyJAR(t *testing.T, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte("classfile-bytes-" + name)); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractAARAndJAR(t *testing.T) {
	cache := t.TempDir()
	lock := fakeLock(t, cache)

	if err := ExtractAll(lock, cache); err != nil {
		t.Fatalf("ExtractAll: %v", err)
	}

	fullDir := ExtractedDir(cache, &lock.Artifacts[0])
	jarBytes, err := os.ReadFile(filepath.Join(fullDir, "classes.jar"))
	if err != nil {
		t.Fatalf("read classes.jar: %v", err)
	}
	if len(jarBytes) == 0 {
		t.Fatal("classes.jar is empty")
	}
	if _, err := os.Stat(filepath.Join(fullDir, "res", "values", "strings.xml")); err != nil {
		t.Fatalf("res/values/strings.xml: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fullDir, "res", "layout", "main.xml")); err != nil {
		t.Fatalf("res/layout/main.xml: %v", err)
	}

	noResDir := ExtractedDir(cache, &lock.Artifacts[1])
	if _, err := os.Stat(filepath.Join(noResDir, "classes.jar")); err != nil {
		t.Fatalf("no-res classes.jar: %v", err)
	}
	if _, err := os.Stat(filepath.Join(noResDir, "res")); !os.IsNotExist(err) {
		t.Fatalf("no-res should not have res/, got err=%v", err)
	}

	jarDir := ExtractedDir(cache, &lock.Artifacts[2])
	plainJar, err := os.ReadFile(filepath.Join(jarDir, "classes.jar"))
	if err != nil {
		t.Fatalf("read jar->classes.jar: %v", err)
	}
	if !bytes.Equal(plainJar, dummyJAR(t, "Baz.class")) {
		t.Fatal("standalone JAR was not copied verbatim")
	}
}

func TestResDirIfPopulated(t *testing.T) {
	cache := t.TempDir()
	lock := fakeLock(t, cache)
	if err := ExtractAll(lock, cache); err != nil {
		t.Fatalf("ExtractAll: %v", err)
	}

	if _, ok := ResDirIfPopulated(cache, &lock.Artifacts[0]); !ok {
		t.Fatal("expected populated res for full artifact")
	}
	if _, ok := ResDirIfPopulated(cache, &lock.Artifacts[1]); ok {
		t.Fatal("no-res artifact must not report populated res")
	}
	if _, ok := ResDirIfPopulated(cache, &lock.Artifacts[2]); ok {
		t.Fatal("plainjar artifact must not report populated res")
	}
}

// fakeAapt2 writes a tiny shell script that pretends to be aapt2: when called
// as `compile --dir <res> -o <out>`, it touches one .flat file per source res
// file in the input directory so CompileAll has something to enumerate.
func fakeAapt2(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake aapt2 is a POSIX shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "aapt2")
	script := `#!/bin/sh
set -e
mode=$1; shift
[ "$mode" = "compile" ] || exit 2
res=""
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    --dir) res=$2; shift 2 ;;
    -o)    out=$2; shift 2 ;;
    *)     shift ;;
  esac
done
mkdir -p "$out"
i=0
find "$res" -type f | while read f; do
  i=$((i+1))
  base=$(basename "$f")
  printf 'flat-stub-%d-%s' "$i" "$base" > "$out/$(echo "$base" | tr / _).flat"
done
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake aapt2: %v", err)
	}
	return path
}

func TestCompileAll(t *testing.T) {
	cache := t.TempDir()
	lock := fakeLock(t, cache)
	if err := ExtractAll(lock, cache); err != nil {
		t.Fatalf("ExtractAll: %v", err)
	}

	aapt2 := fakeAapt2(t)
	if err := CompileAll(lock, cache, aapt2); err != nil {
		t.Fatalf("CompileAll: %v", err)
	}

	fullCompiled := CompiledDir(cache, &lock.Artifacts[0])
	entries, err := os.ReadDir(fullCompiled)
	if err != nil {
		t.Fatalf("read full compiled: %v", err)
	}
	flats := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".flat") {
			flats++
		}
	}
	if flats == 0 {
		t.Fatal("expected at least one .flat file from fake aapt2")
	}

	noResCompiled := CompiledDir(cache, &lock.Artifacts[1])
	if _, err := os.Stat(noResCompiled); !os.IsNotExist(err) {
		t.Fatalf("no-res artifact must not have a compiled dir, got err=%v", err)
	}
}

func TestCompileAllRequiresAapt2Path(t *testing.T) {
	cache := t.TempDir()
	lock := fakeLock(t, cache)
	if err := ExtractAll(lock, cache); err != nil {
		t.Fatalf("ExtractAll: %v", err)
	}
	if err := CompileAll(lock, cache, ""); err == nil {
		t.Fatal("CompileAll with empty aapt2 path must fail")
	}
}

func TestPrintFieldClassesJars(t *testing.T) {
	cache := t.TempDir()
	lock := fakeLock(t, cache)
	if err := ExtractAll(lock, cache); err != nil {
		t.Fatalf("ExtractAll: %v", err)
	}

	var buf bytes.Buffer
	if err := PrintField(&buf, lock, cache, "classes-jars"); err != nil {
		t.Fatalf("PrintField: %v", err)
	}
	got := buf.String()
	parts := strings.Fields(got)
	if len(parts) != 3 {
		t.Fatalf("expected 3 classes.jar paths, got %d (%q)", len(parts), got)
	}
	// Lock order is sorted by coordinate; the fixture coordinates sort as
	// full < no-res < plainjar, so the output must follow that.
	want := []string{
		filepath.Join(ExtractedDir(cache, &lock.Artifacts[0]), "classes.jar"),
		filepath.Join(ExtractedDir(cache, &lock.Artifacts[1]), "classes.jar"),
		filepath.Join(ExtractedDir(cache, &lock.Artifacts[2]), "classes.jar"),
	}
	for i, w := range want {
		abs, err := filepath.Abs(w)
		if err != nil {
			t.Fatalf("abs: %v", err)
		}
		if parts[i] != abs {
			t.Fatalf("classes-jars[%d] = %q, want %q", i, parts[i], abs)
		}
	}
}

func TestPrintFieldFlatArgs(t *testing.T) {
	cache := t.TempDir()
	lock := fakeLock(t, cache)
	if err := ExtractAll(lock, cache); err != nil {
		t.Fatalf("ExtractAll: %v", err)
	}
	aapt2 := fakeAapt2(t)
	if err := CompileAll(lock, cache, aapt2); err != nil {
		t.Fatalf("CompileAll: %v", err)
	}

	var buf bytes.Buffer
	if err := PrintField(&buf, lock, cache, "flat-args"); err != nil {
		t.Fatalf("PrintField: %v", err)
	}
	got := buf.String()
	tokens := strings.Fields(got)
	if len(tokens) == 0 || len(tokens)%2 != 0 {
		t.Fatalf("flat-args output malformed: %q", got)
	}
	for i := 0; i < len(tokens); i += 2 {
		if tokens[i] != "-R" {
			t.Fatalf("token %d expected -R, got %q", i, tokens[i])
		}
		if !filepath.IsAbs(tokens[i+1]) {
			t.Fatalf("flat path not absolute: %q", tokens[i+1])
		}
		if !strings.HasSuffix(tokens[i+1], ".flat") {
			t.Fatalf("flat path missing .flat suffix: %q", tokens[i+1])
		}
	}
}

func TestPrintFieldRejectsUnknown(t *testing.T) {
	cache := t.TempDir()
	lock := fakeLock(t, cache)
	if err := PrintField(&bytes.Buffer{}, lock, cache, "totally-bogus"); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestExtractRejectsUnknownPackaging(t *testing.T) {
	cache := t.TempDir()
	lock := &aarresolve.LockFile{
		Artifacts: []aarresolve.ArtifactEntry{{
			Coordinate: "com.example:weird:1.0.0",
			Group:      "com.example",
			Artifact:   "weird",
			Version:    "1.0.0",
			Packaging:  "wat",
			Path:       "com/example/weird/1.0.0/weird-1.0.0.wat",
		}},
	}
	if err := ExtractAll(lock, cache); err == nil {
		t.Fatal("expected error on unsupported packaging")
	}
}

func TestExtractSkipsPOMOnly(t *testing.T) {
	cache := t.TempDir()
	lock := &aarresolve.LockFile{
		Artifacts: []aarresolve.ArtifactEntry{{
			Coordinate: "com.example:bom:1.0.0",
			Group:      "com.example",
			Artifact:   "bom",
			Version:    "1.0.0",
			Packaging:  "pom",
			Path:       "com/example/bom/1.0.0/bom-1.0.0.pom",
		}},
	}
	if err := ExtractAll(lock, cache); err != nil {
		t.Fatalf("ExtractAll on pom-only must succeed: %v", err)
	}
}
