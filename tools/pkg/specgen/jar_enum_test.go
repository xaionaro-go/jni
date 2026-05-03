package specgen

import (
	"archive/zip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writeTestJar builds a JAR (zip) containing the provided entries (path →
// payload bytes) and returns its absolute filesystem path.
func writeTestJar(t *testing.T, entries map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	jarPath := filepath.Join(dir, "test.jar")
	f, err := os.Create(jarPath)
	if err != nil {
		t.Fatalf("create jar: %v", err)
	}
	defer func() { _ = f.Close() }()
	zw := zip.NewWriter(f)
	// Sort to keep order deterministic across platforms.
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		w, werr := zw.Create(name)
		if werr != nil {
			t.Fatalf("create entry %s: %v", name, werr)
		}
		if _, werr = w.Write(entries[name]); werr != nil {
			t.Fatalf("write entry %s: %v", name, werr)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return jarPath
}

func TestEnumerateClassesInJar_ListsClassesAndStripsSuffix(t *testing.T) {
	jar := writeTestJar(t, map[string][]byte{
		"com/example/Foo.class":       []byte("dummy123"),
		"com/example/Foo$Inner.class": []byte("dummy456"),
		"META-INF/MANIFEST.MF":        []byte("Manifest-Version: 1.0\n"),
		"module-info.class":           []byte("dummy789"),
	})

	got, err := EnumerateClassesInJar(jar)
	if err != nil {
		t.Fatalf("EnumerateClassesInJar: %v", err)
	}
	sort.Strings(got)

	want := []string{"com.example.Foo", "com.example.Foo$Inner"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestEnumerateClassesInJar_SkipsMetaInfAndModuleInfo(t *testing.T) {
	jar := writeTestJar(t, map[string][]byte{
		"com/example/Foo.class":           []byte("dummy"),
		"META-INF/MANIFEST.MF":            []byte("manifest"),
		"META-INF/services/foo.bar.Baz":   []byte("impl"),
		"META-INF/versions/9/module.class": []byte("dummy"),
		"module-info.class":               []byte("dummy"),
	})

	got, err := EnumerateClassesInJar(jar)
	if err != nil {
		t.Fatalf("EnumerateClassesInJar: %v", err)
	}
	for _, name := range got {
		if name == "module-info" {
			t.Errorf("module-info.class must be skipped, got %q", name)
		}
		// META-INF entries (including META-INF/versions/.../*.class) must
		// not leak into the output, regardless of "/"→"." translation.
		if strings.HasPrefix(name, "META-INF") || strings.HasPrefix(name, "META-INF.") {
			t.Errorf("META-INF entry leaked: %q", name)
		}
	}
	if len(got) != 1 || got[0] != "com.example.Foo" {
		t.Errorf("expected exactly [com.example.Foo], got %v", got)
	}
}

// TestEnumerateClassesInJarFiltered_DropsSkippedPrefixes verifies that
// classes whose dot-separated names start with any provided skipPrefix
// are omitted while siblings outside the skip list are kept. Cycle 7
// regression — the cycle 6 JAR walker pulled the entire Kotlin runtime
// transitively from Material AARs, producing thousands of unwanted Go
// packages.
func TestEnumerateClassesInJarFiltered_DropsSkippedPrefixes(t *testing.T) {
	jar := writeTestJar(t, map[string][]byte{
		"kotlin/Unit.class":                                 []byte("k1"),
		"kotlin/jvm/internal/Intrinsics.class":              []byte("k2"),
		"kotlinx/coroutines/Job.class":                      []byte("kx"),
		"org/jetbrains/annotations/NotNull.class":           []byte("ja"),
		"org/intellij/lang/annotations/Pattern.class":       []byte("ia"),
		"androidx/recyclerview/widget/RecyclerView.class":   []byte("rv"),
		"com/google/android/material/button/MaterialButton.class": []byte("mb"),
	})

	got, err := EnumerateClassesInJarFiltered(jar, []string{
		"kotlin.", "kotlinx.", "org.jetbrains.", "org.intellij.",
	})
	if err != nil {
		t.Fatalf("EnumerateClassesInJarFiltered: %v", err)
	}
	sort.Strings(got)

	want := []string{
		"androidx.recyclerview.widget.RecyclerView",
		"com.google.android.material.button.MaterialButton",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v (%d), want %v (%d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Dual-sided: explicitly assert the dropped names are NOT present.
	for _, name := range got {
		switch {
		case strings.HasPrefix(name, "kotlin."),
			strings.HasPrefix(name, "kotlinx."),
			strings.HasPrefix(name, "org.jetbrains."),
			strings.HasPrefix(name, "org.intellij."):
			t.Errorf("filtered class leaked: %q", name)
		}
	}
}

// TestEnumerateClassesInJarFiltered_NilSkipKeepsAll verifies that passing
// nil (or empty) skip prefixes preserves the unfiltered behavior.
func TestEnumerateClassesInJarFiltered_NilSkipKeepsAll(t *testing.T) {
	jar := writeTestJar(t, map[string][]byte{
		"kotlin/Unit.class":                                []byte("k1"),
		"androidx/recyclerview/widget/RecyclerView.class":  []byte("rv"),
	})

	got, err := EnumerateClassesInJarFiltered(jar, nil)
	if err != nil {
		t.Fatalf("EnumerateClassesInJarFiltered(nil): %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 classes (no filter), got %d: %v", len(got), got)
	}
}

// TestDefaultJarSkipPrefixes_CoversKnownRuntime asserts the default skip
// list catches the transitive runtime packages we've observed leaking
// into Material/AndroidX AAR closures.
func TestDefaultJarSkipPrefixes_CoversKnownRuntime(t *testing.T) {
	required := []string{"kotlin.", "kotlinx.", "org.intellij.", "org.jetbrains."}
	for _, want := range required {
		found := false
		for _, p := range DefaultJarSkipPrefixes {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DefaultJarSkipPrefixes is missing %q (got %v)", want, DefaultJarSkipPrefixes)
		}
	}
}

func TestFindClassesJars_WalksTree(t *testing.T) {
	root := t.TempDir()
	mustMkdir := func(p string) {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
	mustWrite := func(p string) {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	mustMkdir(filepath.Join(root, "a", "b", "c"))
	mustMkdir(filepath.Join(root, "d"))
	jar1 := filepath.Join(root, "a", "b", "c", "classes.jar")
	jar2 := filepath.Join(root, "d", "classes.jar")
	other := filepath.Join(root, "d", "other.jar")
	mustWrite(jar1)
	mustWrite(jar2)
	mustWrite(other)

	got, err := FindClassesJars(root)
	if err != nil {
		t.Fatalf("FindClassesJars: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 paths, got %d: %v", len(got), got)
	}
	for _, p := range got {
		if !filepath.IsAbs(p) {
			t.Errorf("expected absolute path, got %q", p)
		}
		if filepath.Base(p) != "classes.jar" {
			t.Errorf("unexpected basename %q", p)
		}
	}
	// Verify other.jar was not picked up.
	for _, p := range got {
		if p == other {
			t.Errorf("other.jar must not be returned: %q", p)
		}
	}
}
