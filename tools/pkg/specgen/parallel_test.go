package specgen

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestGenerateFromSources_ParallelWorkerSafety exercises the worker-pool path
// in GenerateFromSources by feeding it a temp ref dir with one .class file
// plus a synthetic JAR holding multiple junk class entries. The bytes are
// invalid bytecode, so every javap invocation will fail — but the orchestration
// layer (worker creation, channel close, WaitGroup, mutex around `specs`)
// must still complete cleanly with no error, no panic, no goroutine leak,
// and no data race when run under `go test -race`.
//
// The test asserts that the worker goroutines have all exited before the
// function returns — a missing or misplaced `wg.Wait()` would leak workers,
// detectable via runtime.NumGoroutine delta.
func TestGenerateFromSources_ParallelWorkerSafety(t *testing.T) {
	refDir := t.TempDir()
	jarsDir := t.TempDir()
	outputDir := t.TempDir()

	// One fake .class under refDir. javap will reject these bytes; the
	// generator must skip the class without aborting the run.
	if err := os.MkdirAll(filepath.Join(refDir, "fake", "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir refDir: %v", err)
	}
	classFile := filepath.Join(refDir, "fake", "pkg", "RefOnly.class")
	if err := os.WriteFile(classFile, []byte("not-a-real-class"), 0o644); err != nil {
		t.Fatalf("write ref class: %v", err)
	}

	// Build a synthetic JAR with multiple top-level classes plus an inner
	// class so the worker loop has a non-trivial queue to drain.
	jarPath := filepath.Join(jarsDir, "lib", "classes.jar")
	if err := os.MkdirAll(filepath.Dir(jarPath), 0o755); err != nil {
		t.Fatalf("mkdir jar dir: %v", err)
	}
	jarFile, err := os.Create(jarPath)
	if err != nil {
		t.Fatalf("create jar: %v", err)
	}
	zw := zip.NewWriter(jarFile)
	for _, entry := range []string{
		"jar/pkg/Alpha.class",
		"jar/pkg/Alpha$Inner.class",
		"jar/pkg/Beta.class",
		"jar/other/Gamma.class",
		"META-INF/MANIFEST.MF",
		"module-info.class",
	} {
		w, werr := zw.Create(entry)
		if werr != nil {
			t.Fatalf("zip create %s: %v", entry, werr)
		}
		if _, werr = w.Write([]byte("not-a-real-class")); werr != nil {
			t.Fatalf("zip write %s: %v", entry, werr)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := jarFile.Close(); err != nil {
		t.Fatalf("jar close: %v", err)
	}

	// Establish a goroutine baseline. The post-call count must return to
	// this number once GOMAXPROCS workers wind down (allow a small slack
	// for runtime/test-framework background goroutines).
	runtime.GC()
	baseline := runtime.NumGoroutine()

	// Empty extraClassPath skips LoadServiceNames (which requires javac and
	// android.jar) and isolates the test to the parallel work-loop path.
	if err := GenerateFromSources(refDir, jarsDir, "", outputDir, "github.com/example/test", nil); err != nil {
		t.Fatalf("GenerateFromSources returned error: %v", err)
	}

	// All input bytes are bogus, so no javap invocation will succeed.
	// Output dir should exist (MkdirAll) but contain no spec files.
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatalf("read outputDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".yaml" {
			t.Errorf("unexpected yaml file from invalid input: %s", e.Name())
		}
	}

	// Workers must all be drained: if wg.Wait() were missing, GenerateFromSources
	// would return while goroutines are still iterating the jobs channel.
	// Allow up to 200ms for OS-level scheduler quiescence; anything beyond
	// that indicates a leak.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		runtime.Gosched()
		if runtime.NumGoroutine() <= baseline+1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if leaked := runtime.NumGoroutine() - baseline; leaked > 1 {
		t.Errorf("goroutine leak: baseline=%d, after=%d (delta=%d)",
			baseline, runtime.NumGoroutine(), leaked)
	}
}
