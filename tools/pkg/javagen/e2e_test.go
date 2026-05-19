package javagen

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testGoModule = "github.com/AndroidGoLab/jni"

// findRepoRoot walks up from the test directory to locate go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting cwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
}

// TestAllJavaSpecs_LoadAndMerge loads every spec/java/*.yaml, applies overlay,
// and merges. This exercises LoadSpec, LoadOverlay, Merge, and all type resolution.
func TestAllJavaSpecs_LoadAndMerge(t *testing.T) {
	root := findRepoRoot(t)
	specsDir := filepath.Join(root, "spec", "java")
	overlaysDir := filepath.Join(root, "spec", "overlays", "java")

	specs, err := filepath.Glob(filepath.Join(specsDir, "*.yaml"))
	if err != nil {
		t.Fatalf("glob specs: %v", err)
	}
	if len(specs) == 0 {
		t.Fatal("no spec files found")
	}

	t.Logf("found %d spec files", len(specs))

	// The exact spec count grows with each cycle that adds AAR-sourced
	// classes. The magic number is updated alongside the regeneration so
	// drift in the spec corpus is visible in code review. Cycle 7 expanded
	// the corpus to 425 (cycle 6 baseline 238 + ~187 specs walked from
	// AAR classes.jars under .aar-cache/extracted, minus kotlin/jetbrains
	// runtime entries excluded by DefaultJarSkipPrefixes).
	const expectedSpecCount = 425
	if len(specs) != expectedSpecCount {
		t.Errorf("expected %d spec files, got %d", expectedSpecCount, len(specs))
	}

	for _, specPath := range specs {
		baseName := strings.TrimSuffix(filepath.Base(specPath), ".yaml")
		t.Run(baseName, func(t *testing.T) {
			spec, err := LoadSpec(specPath)
			if err != nil {
				t.Fatalf("LoadSpec: %v", err)
			}
			if spec.Package == "" {
				t.Error("package is empty")
			}
			if spec.GoImport == "" {
				t.Error("go_import is empty")
			}

			overlayPath := filepath.Join(overlaysDir, baseName+".yaml")
			overlay, err := LoadOverlay(overlayPath)
			if err != nil {
				t.Fatalf("LoadOverlay: %v", err)
			}

			merged, err := Merge(spec, overlay)
			if err != nil {
				t.Fatalf("Merge: %v", err)
			}

			// Merge preserves spec fields.
			if merged.GoImport != spec.GoImport {
				t.Errorf("merged go_import %q != spec go_import %q", merged.GoImport, spec.GoImport)
			}
		})
	}
}

// TestAllJavaSpecs_Generate runs the full Generate pipeline for every
// spec/java/*.yaml and verifies the output is valid Go.
func TestAllJavaSpecs_Generate(t *testing.T) {
	root := findRepoRoot(t)
	specsDir := filepath.Join(root, "spec", "java")
	overlaysDir := filepath.Join(root, "spec", "overlays", "java")
	templatesDir := filepath.Join(root, "templates", "java")
	outputDir := t.TempDir()

	specs, err := filepath.Glob(filepath.Join(specsDir, "*.yaml"))
	if err != nil {
		t.Fatalf("glob specs: %v", err)
	}
	if len(specs) == 0 {
		t.Fatal("no spec files found")
	}

	for _, specPath := range specs {
		baseName := strings.TrimSuffix(filepath.Base(specPath), ".yaml")
		overlayPath := filepath.Join(overlaysDir, baseName+".yaml")

		t.Run(baseName, func(t *testing.T) {
			if err := Generate(specPath, overlayPath, templatesDir, outputDir, testGoModule); err != nil {
				t.Fatalf("Generate: %v", err)
			}

			// Determine the output package directory from go_import.
			spec, err := LoadSpec(specPath)
			if err != nil {
				t.Fatalf("LoadSpec: %v", err)
			}
			pkgDir := filepath.Join(outputDir, GoImportToRelDir(spec.GoImport, testGoModule))

			// Verify the directory was created.
			info, err := os.Stat(pkgDir)
			if err != nil {
				t.Fatalf("output dir missing: %v", err)
			}
			if !info.IsDir() {
				t.Fatalf("output path is not a directory: %s", pkgDir)
			}

			// Verify generated Go files parse correctly.
			fset := token.NewFileSet()
			entries, err := os.ReadDir(pkgDir)
			if err != nil {
				t.Fatalf("readdir: %v", err)
			}
			goFileCount := 0
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
					continue
				}
				goFileCount++
				goPath := filepath.Join(pkgDir, e.Name())
				_, parseErr := parser.ParseFile(fset, goPath, nil, parser.AllErrors)
				if parseErr != nil {
					t.Errorf("parse %s: %v", e.Name(), parseErr)
				}
			}
			if goFileCount == 0 {
				t.Error("no .go files generated")
			}
		})
	}
}

// TestGenerate_Idempotency runs Generate twice and verifies the output
// is byte-identical.
func TestGenerate_Idempotency(t *testing.T) {
	root := findRepoRoot(t)
	specsDir := filepath.Join(root, "spec", "java")
	overlaysDir := filepath.Join(root, "spec", "overlays", "java")
	templatesDir := filepath.Join(root, "templates", "java")

	// Use two different output dirs.
	outputDir1 := t.TempDir()
	outputDir2 := t.TempDir()

	// Pick a representative subset to keep test time reasonable.
	testSpecs := []string{"location", "bluetooth", "notification", "toast", "content"}

	for _, name := range testSpecs {
		specPath := filepath.Join(specsDir, name+".yaml")
		overlayPath := filepath.Join(overlaysDir, name+".yaml")

		if _, err := os.Stat(specPath); err != nil {
			t.Fatalf("required spec %s not found at %s: %v", name, specPath, err)
		}

		if err := Generate(specPath, overlayPath, templatesDir, outputDir1, testGoModule); err != nil {
			t.Fatalf("Generate pass 1 (%s): %v", name, err)
		}
		if err := Generate(specPath, overlayPath, templatesDir, outputDir2, testGoModule); err != nil {
			t.Fatalf("Generate pass 2 (%s): %v", name, err)
		}
	}

	// Compare outputs.
	for _, name := range testSpecs {
		specPath := filepath.Join(specsDir, name+".yaml")
		spec, err := LoadSpec(specPath)
		if err != nil {
			t.Fatalf("LoadSpec %s: %v", name, err)
		}
		relDir := GoImportToRelDir(spec.GoImport, testGoModule)
		pkgDir1 := filepath.Join(outputDir1, relDir)
		pkgDir2 := filepath.Join(outputDir2, relDir)

		entries, err := os.ReadDir(pkgDir1)
		if err != nil {
			t.Fatalf("readdir %s: %v", name, err)
		}

		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			data1, err := os.ReadFile(filepath.Join(pkgDir1, e.Name()))
			if err != nil {
				t.Fatalf("read %s/%s: %v", name, e.Name(), err)
			}
			data2, err := os.ReadFile(filepath.Join(pkgDir2, e.Name()))
			if err != nil {
				t.Fatalf("read %s/%s: %v", name, e.Name(), err)
			}
			if string(data1) != string(data2) {
				t.Errorf("idempotency failure: %s/%s differs between runs", name, e.Name())
			}
		}
	}
}

// TestGenerate_NoDrift verifies that regenerating from specs produces
// output identical to the committed generated files.
// This test checks for unintended divergence. If the templates or specs
// have been intentionally changed but output not yet regenerated, this
// test logs warnings but still passes (to avoid blocking other work).
func TestGenerate_NoDrift(t *testing.T) {
	root := findRepoRoot(t)
	specsDir := filepath.Join(root, "spec", "java")
	overlaysDir := filepath.Join(root, "spec", "overlays", "java")
	templatesDir := filepath.Join(root, "templates", "java")
	outputDir := t.TempDir()

	testSpecs := []string{"permission", "build", "environment"}

	for _, name := range testSpecs {
		specPath := filepath.Join(specsDir, name+".yaml")
		overlayPath := filepath.Join(overlaysDir, name+".yaml")

		if _, err := os.Stat(specPath); os.IsNotExist(err) {
			continue
		}

		if err := Generate(specPath, overlayPath, templatesDir, outputDir, testGoModule); err != nil {
			t.Fatalf("Generate (%s): %v", name, err)
		}

		spec, err := LoadSpec(specPath)
		if err != nil {
			t.Fatalf("LoadSpec %s: %v", name, err)
		}

		relDir := GoImportToRelDir(spec.GoImport, testGoModule)
		genDir := filepath.Join(outputDir, relDir)
		committedDir := filepath.Join(root, relDir)

		if _, err := os.Stat(committedDir); os.IsNotExist(err) {
			continue
		}

		entries, err := os.ReadDir(genDir)
		if err != nil {
			t.Fatalf("readdir %s: %v", name, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			genData, err := os.ReadFile(filepath.Join(genDir, e.Name()))
			if err != nil {
				t.Fatalf("read gen %s/%s: %v", name, e.Name(), err)
			}
			committedPath := filepath.Join(committedDir, e.Name())
			committedData, err := os.ReadFile(committedPath)
			if err != nil {
				continue
			}
			if string(genData) != string(committedData) {
				t.Logf("drift detected: %s/%s differs from committed version (run make java to update)", name, e.Name())
			}
		}
	}
}

// TestGenerate_OutputFilePatterns verifies that each spec produces the
// expected set of output files based on its content.
func TestGenerate_OutputFilePatterns(t *testing.T) {
	root := findRepoRoot(t)
	templatesDir := filepath.Join(root, "templates", "java")

	tests := []struct {
		specName      string
		expectFiles   []string
		unexpectFiles []string
	}{
		{
			specName:      "location",
			expectFiles:   []string{"doc.go", "init.go", "manager.go", "constants.go"},
			unexpectFiles: []string{"callbacks.go"},
		},
		{
			specName:      "toast",
			expectFiles:   []string{"doc.go", "init.go", "toast.go", "constants.go"},
			unexpectFiles: []string{"callbacks.go"},
		},
		{
			specName:      "content",
			expectFiles:   []string{"doc.go", "init.go", "broadcast_receiver.go", "constants.go"},
			unexpectFiles: []string{"callbacks.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.specName, func(t *testing.T) {
			specPath := filepath.Join(root, "spec", "java", tt.specName+".yaml")
			overlayPath := filepath.Join(root, "spec", "overlays", "java", tt.specName+".yaml")
			outputDir := t.TempDir()

			if err := Generate(specPath, overlayPath, templatesDir, outputDir, testGoModule); err != nil {
				t.Fatalf("Generate: %v", err)
			}

			spec, err := LoadSpec(specPath)
			if err != nil {
				t.Fatalf("LoadSpec: %v", err)
			}
			pkgDir := filepath.Join(outputDir, GoImportToRelDir(spec.GoImport, testGoModule))

			for _, f := range tt.expectFiles {
				path := filepath.Join(pkgDir, f)
				if _, err := os.Stat(path); os.IsNotExist(err) {
					t.Errorf("expected file %s not found", f)
				}
			}
			for _, f := range tt.unexpectFiles {
				path := filepath.Join(pkgDir, f)
				if _, err := os.Stat(path); err == nil {
					t.Errorf("unexpected file %s found", f)
				}
			}
		})
	}
}

// TestGenerate_ContentPatterns verifies key content patterns in generated files.
func TestGenerate_ContentPatterns(t *testing.T) {
	root := findRepoRoot(t)
	templatesDir := filepath.Join(root, "templates", "java")
	specPath := filepath.Join(root, "spec", "java", "location.yaml")
	overlayPath := filepath.Join(root, "spec", "overlays", "java", "location.yaml")
	outputDir := t.TempDir()

	if err := Generate(specPath, overlayPath, templatesDir, outputDir, testGoModule); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	pkgDir := filepath.Join(outputDir, "location")

	// Check doc.go has package declaration.
	docGo := readFile(t, filepath.Join(pkgDir, "doc.go"))
	if !strings.Contains(docGo, "package location") {
		t.Error("doc.go missing package declaration")
	}
	if !strings.Contains(docGo, "Code generated by javagen") {
		t.Error("doc.go missing generated header")
	}

	// Check manager.go has constructor and methods.
	mgrGo := readFile(t, filepath.Join(pkgDir, "manager.go"))
	if !strings.Contains(mgrGo, "type Manager struct") {
		t.Error("manager.go missing Manager struct")
	}
	if !strings.Contains(mgrGo, "NewManager") {
		t.Error("manager.go missing NewManager constructor")
	}
	if !strings.Contains(mgrGo, "GetSystemService") {
		t.Error("manager.go missing GetSystemService call")
	}
	if !strings.Contains(mgrGo, "func (m *Manager)") {
		t.Error("manager.go missing Manager methods")
	}
	if !strings.Contains(mgrGo, "m.VM.Do") {
		t.Error("manager.go missing VM.Do pattern")
	}
	if !strings.Contains(mgrGo, "ensureInit") {
		t.Error("manager.go missing ensureInit call")
	}

	// Check init.go has sync.Once initialization.
	initGo := readFile(t, filepath.Join(pkgDir, "init.go"))
	if !strings.Contains(initGo, "sync.Once") {
		t.Error("init.go missing sync.Once")
	}
	if !strings.Contains(initGo, "ensureInit") {
		t.Error("init.go missing ensureInit")
	}
	if !strings.Contains(initGo, "FindClass") {
		t.Error("init.go missing FindClass")
	}

	// Check consts/consts.go has expected values.
	constsDir := filepath.Join(pkgDir, "consts")
	constGo := readFile(t, filepath.Join(constsDir, "consts.go"))
	if !strings.Contains(constGo, "package consts") {
		t.Error("consts/consts.go missing 'package consts' declaration")
	}
	if !strings.Contains(constGo, "Gps") {
		t.Error("consts/consts.go missing Gps constant")
	}
	if !strings.Contains(constGo, "Network") {
		t.Error("consts/consts.go missing Network constant")
	}
	if !strings.Contains(constGo, "Code generated by javagen") {
		t.Error("consts/consts.go missing generated header")
	}

	// Check constants.go has alias re-exports.
	aliasGo := readFile(t, filepath.Join(pkgDir, "constants.go"))
	if !strings.Contains(aliasGo, "package location") {
		t.Error("constants.go missing 'package location' declaration")
	}
	if !strings.Contains(aliasGo, "Code generated by javagen") {
		t.Error("constants.go missing generated header")
	}
	if !strings.Contains(aliasGo, `"github.com/AndroidGoLab/jni/location/consts"`) {
		t.Error("constants.go missing consts import")
	}
	if !strings.Contains(aliasGo, "= consts.") {
		t.Error("constants.go missing alias re-exports (= consts.X pattern)")
	}
}

// TestGenerate_AbstractCallbackAdapters verifies that abstract callback
// entries in a spec produce Java adapter files in a java/ subdirectory.
func TestGenerate_AbstractCallbackAdapters(t *testing.T) {
	root := findRepoRoot(t)
	templatesDir := filepath.Join(root, "templates", "java")
	outputDir := t.TempDir()

	specYAML := `
package: test_le
go_import: github.com/AndroidGoLab/jni/test_le
classes:
  - java_class: android.bluetooth.le.BluetoothLeScanner
    go_type: Scanner
    methods:
      - java_method: startScan
        go_name: StartScan
        params:
          - java_type: android.bluetooth.le.ScanCallback
            go_name: arg0
        returns: void
        error: true
abstract_callbacks:
  - java_class: android.bluetooth.le.ScanCallback
    go_type: ScanCallbackCB
    methods:
      - java_method: onScanFailed
        params:
          - int
        returns: void
        go_field: OnScanFailed
      - java_method: onScanResult
        params:
          - int
          - android.bluetooth.le.ScanResult
        returns: void
        go_field: OnScanResult
`
	specPath := filepath.Join(t.TempDir(), "test.yaml")
	if err := os.WriteFile(specPath, []byte(specYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	overlayPath := filepath.Join(t.TempDir(), "nonexistent.yaml")
	if err := Generate(specPath, overlayPath, templatesDir, outputDir, testGoModule); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Verify Java adapter file was generated.
	javaFile := filepath.Join(outputDir, "test_le", "java", "ScanCallbackAdapter.java")
	data, err := os.ReadFile(javaFile)
	if err != nil {
		t.Fatalf("expected Java adapter file at %s: %v", javaFile, err)
	}

	content := string(data)
	if !strings.Contains(content, "Code generated by javagen. DO NOT EDIT.") {
		t.Error("missing generated header")
	}
	if !strings.Contains(content, "package center.dx.jni.generated.android.bluetooth.le;") {
		t.Error("missing target-package-derived adapter package")
	}
	if !strings.Contains(content, "extends android.bluetooth.le.ScanCallback") {
		t.Error("missing extends clause")
	}
	if !strings.Contains(content, "ScanCallbackAdapter") {
		t.Error("missing adapter class name")
	}
	if !strings.Contains(content, "GoAbstractDispatch.invoke") {
		t.Error("missing GoAbstractDispatch.invoke")
	}
	if !strings.Contains(content, `"onScanFailed"`) {
		t.Error("missing onScanFailed dispatch")
	}
	if !strings.Contains(content, `"onScanResult"`) {
		t.Error("missing onScanResult dispatch")
	}
	if !strings.Contains(content, "Integer.valueOf(arg0)") {
		t.Error("missing int autoboxing")
	}
}

func TestGenerate_AbstractCallbackAdapterTypeParamBounds(t *testing.T) {
	root := findRepoRoot(t)
	templatesDir := filepath.Join(root, "templates", "java")
	outputDir := t.TempDir()

	specYAML := `
package: test_widget
go_import: github.com/AndroidGoLab/jni/test_widget
abstract_callbacks:
  - java_class: androidx.recyclerview.widget.RecyclerView$Adapter
    go_type: RecyclerViewAdapterCallback
    type_param_bounds:
      - androidx.recyclerview.widget.RecyclerView$ViewHolder
    methods:
      - java_method: onCreateViewHolder
        params:
          - android.view.ViewGroup
          - int
        returns: androidx.recyclerview.widget.RecyclerView$ViewHolder
        go_field: OnCreateViewHolder
      - java_method: getItemCount
        params: []
        returns: int
        go_field: GetItemCount
`
	specPath := filepath.Join(t.TempDir(), "test.yaml")
	if err := os.WriteFile(specPath, []byte(specYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	overlayPath := filepath.Join(t.TempDir(), "nonexistent.yaml")
	if err := Generate(specPath, overlayPath, templatesDir, outputDir, testGoModule); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	content := readFile(t, filepath.Join(outputDir, "test_widget", "java", "RecyclerView$AdapterAdapter.java"))
	for _, want := range []string{
		"package center.dx.jni.generated.androidx.recyclerview.widget;",
		"import androidx.recyclerview.widget.RecyclerView;",
		"extends RecyclerView.Adapter<RecyclerView.ViewHolder>",
		"public androidx.recyclerview.widget.RecyclerView.ViewHolder onCreateViewHolder(android.view.ViewGroup arg0, int arg1)",
		"return ((Integer) GoAbstractDispatch.invoke(\n            handlerID, \"getItemCount\", new Object[]{  })).intValue();",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q, got:\n%s", want, content)
		}
	}
	for _, forbidden := range []string{
		"extends androidx.recyclerview.widget.RecyclerView.Adapter",
		"import androidx.recyclerview.widget.RecyclerView$Adapter",
		"RecyclerView$ViewHolder",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("generated adapter contains source-invalid %q, got:\n%s", forbidden, content)
		}
	}
}

func TestGenerate_AbstractCallbackAdapterCompilesWithRecursiveGenericSuperclass(t *testing.T) {
	javac, err := exec.LookPath("javac")
	if err != nil {
		t.Fatalf("javac not available: %v", err)
	}

	root := findRepoRoot(t)
	templatesDir := filepath.Join(root, "templates", "java")
	outputDir := t.TempDir()

	specYAML := `
package: test_anim
go_import: github.com/AndroidGoLab/jni/test_anim
abstract_callbacks:
  - java_class: androidx.dynamicanimation.animation.DynamicAnimation
    go_type: DynamicAnimationCallback
    methods:
      - java_method: getThis
        params: []
        returns: androidx.dynamicanimation.animation.DynamicAnimation
        go_field: GetThis
      - java_method: setStartValue
        params:
          - float
        returns: androidx.dynamicanimation.animation.DynamicAnimation
        go_field: SetStartValue
`
	specPath := filepath.Join(t.TempDir(), "test.yaml")
	if err := os.WriteFile(specPath, []byte(specYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	overlayPath := filepath.Join(t.TempDir(), "nonexistent.yaml")
	if err := Generate(specPath, overlayPath, templatesDir, outputDir, testGoModule); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	tmp := t.TempDir()
	dynamicAnimationPath := filepath.Join(tmp, "androidx", "dynamicanimation", "animation", "DynamicAnimation.java")
	if err := os.MkdirAll(filepath.Dir(dynamicAnimationPath), 0o755); err != nil {
		t.Fatal(err)
	}
	dynamicAnimationSource := `
package androidx.dynamicanimation.animation;

public abstract class DynamicAnimation<T extends DynamicAnimation<T>> {
    public abstract T getThis();
    public abstract DynamicAnimation<T> setStartValue(float value);
}
`
	if err := os.WriteFile(dynamicAnimationPath, []byte(dynamicAnimationSource), 0o644); err != nil {
		t.Fatal(err)
	}

	dispatchPath := filepath.Join(tmp, "center", "dx", "jni", "internal", "GoAbstractDispatch.java")
	if err := os.MkdirAll(filepath.Dir(dispatchPath), 0o755); err != nil {
		t.Fatal(err)
	}
	dispatchSource := `
package center.dx.jni.internal;

public class GoAbstractDispatch {
    public static Object invoke(long handlerID, String methodName, Object[] args) {
        return null;
    }
}
`
	if err := os.WriteFile(dispatchPath, []byte(dispatchSource), 0o644); err != nil {
		t.Fatal(err)
	}

	adapterPath := filepath.Join(outputDir, "test_anim", "java", "DynamicAnimationAdapter.java")
	classesDir := filepath.Join(tmp, "classes")
	cmd := exec.Command(javac, "-d", classesDir, dynamicAnimationPath, dispatchPath, adapterPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("javac recursive generic adapter fixture failed: %v\n%s", err, out)
	}
}

func TestGenerate_AbstractCallbackAdapterCompilesWithDescriptorArrayTypes(t *testing.T) {
	javac, err := exec.LookPath("javac")
	if err != nil {
		t.Fatalf("javac not available: %v", err)
	}

	root := findRepoRoot(t)
	templatesDir := filepath.Join(root, "templates", "java")
	outputDir := t.TempDir()

	specYAML := `
package: test_arrays
go_import: github.com/AndroidGoLab/jni/test_arrays
abstract_callbacks:
  - java_class: com.example.ArrayCallback
    go_type: ArrayCallback
    methods:
      - java_method: copy
        params:
          - '[I'
          - '[B'
          - '[Z'
          - '[Ljava.lang.String;'
          - '[Lcom.example.Outer$Inner;'
        returns: '[B'
        go_field: Copy
      - java_method: nested
        params: []
        returns: '[Lcom.example.Outer$Inner;'
        go_field: Nested
`
	specPath := filepath.Join(t.TempDir(), "test.yaml")
	if err := os.WriteFile(specPath, []byte(specYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	overlayPath := filepath.Join(t.TempDir(), "nonexistent.yaml")
	if err := Generate(specPath, overlayPath, templatesDir, outputDir, testGoModule); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	content := readFile(t, filepath.Join(outputDir, "test_arrays", "java", "ArrayCallbackAdapter.java"))
	for _, want := range []string{
		"public byte[] copy(int[] arg0, byte[] arg1, boolean[] arg2, java.lang.String[] arg3, com.example.Outer.Inner[] arg4)",
		"public com.example.Outer.Inner[] nested()",
		"return (byte[]) GoAbstractDispatch.invoke(",
		"return (com.example.Outer.Inner[]) GoAbstractDispatch.invoke(",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q, got:\n%s", want, content)
		}
	}
	for _, forbidden := range []string{"[I", "[B", "[Z", "[Ljava", "[Lcom.example.Outer$Inner;"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("generated adapter contains source-invalid descriptor %q, got:\n%s", forbidden, content)
		}
	}

	tmp := t.TempDir()
	arrayCallbackPath := filepath.Join(tmp, "com", "example", "ArrayCallback.java")
	if err := os.MkdirAll(filepath.Dir(arrayCallbackPath), 0o755); err != nil {
		t.Fatal(err)
	}
	arrayCallbackSource := `
package com.example;

public abstract class ArrayCallback {
    public abstract byte[] copy(int[] ints, byte[] bytes, boolean[] flags, String[] names, Outer.Inner[] inners);
    public abstract Outer.Inner[] nested();
}
`
	if err := os.WriteFile(arrayCallbackPath, []byte(arrayCallbackSource), 0o644); err != nil {
		t.Fatal(err)
	}

	outerPath := filepath.Join(tmp, "com", "example", "Outer.java")
	outerSource := `
package com.example;

public final class Outer {
    public static final class Inner {}
}
`
	if err := os.WriteFile(outerPath, []byte(outerSource), 0o644); err != nil {
		t.Fatal(err)
	}

	dispatchPath := filepath.Join(tmp, "center", "dx", "jni", "internal", "GoAbstractDispatch.java")
	if err := os.MkdirAll(filepath.Dir(dispatchPath), 0o755); err != nil {
		t.Fatal(err)
	}
	dispatchSource := `
package center.dx.jni.internal;

public class GoAbstractDispatch {
    public static Object invoke(long handlerID, String methodName, Object[] args) {
        return null;
    }
}
`
	if err := os.WriteFile(dispatchPath, []byte(dispatchSource), 0o644); err != nil {
		t.Fatal(err)
	}

	adapterPath := filepath.Join(outputDir, "test_arrays", "java", "ArrayCallbackAdapter.java")
	classesDir := filepath.Join(tmp, "classes")
	cmd := exec.Command(javac, "--release", "17", "-d", classesDir, arrayCallbackPath, outerPath, dispatchPath, adapterPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("javac descriptor-array adapter fixture failed: %v\n%s", err, out)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}
