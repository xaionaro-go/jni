package jnigen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSpec_Valid(t *testing.T) {
	yaml := `
primitives:
  - { c_type: jboolean, go_type: uint8, suffix: Boolean }
  - { c_type: jint,     go_type: int32, suffix: Int }

reference_types:
  - { c_type: jobject, parent: ~ }
  - { c_type: jclass,  parent: jobject }

opaque_types:
  - { c_type: jmethodID }

function_families:
  - pattern: "Call{Type}MethodA"
    vtable: JNIEnv
    params: [jobject, jmethodID, "const jvalue*"]
    returns: "{type}"
    expand: [Boolean, Int]

env_functions:
  - { name: GetVersion, returns: jint }

vm_functions:
  - { name: DestroyJavaVM, returns: jint }

constants:
  - { name: JNI_OK, value: 0, go_type: int32 }
`
	path := writeTemp(t, yaml)
	spec, err := LoadSpec(path)
	if err != nil {
		t.Fatalf("LoadSpec failed: %v", err)
	}

	if len(spec.Primitives) != 2 {
		t.Errorf("expected 2 primitives, got %d", len(spec.Primitives))
	}
	if spec.Primitives[0].Suffix != "Boolean" {
		t.Errorf("expected Boolean suffix, got %q", spec.Primitives[0].Suffix)
	}
	if len(spec.ReferenceTypes) != 2 {
		t.Errorf("expected 2 reference types, got %d", len(spec.ReferenceTypes))
	}
	if len(spec.FunctionFamilies) != 1 {
		t.Errorf("expected 1 function family, got %d", len(spec.FunctionFamilies))
	}
	if spec.FunctionFamilies[0].Pattern != "Call{Type}MethodA" {
		t.Errorf("unexpected pattern: %q", spec.FunctionFamilies[0].Pattern)
	}
	if len(spec.EnvFunctions) != 1 {
		t.Errorf("expected 1 env function, got %d", len(spec.EnvFunctions))
	}
	if len(spec.VMFunctions) != 1 {
		t.Errorf("expected 1 vm function, got %d", len(spec.VMFunctions))
	}
	if len(spec.Constants) != 1 {
		t.Errorf("expected 1 constant, got %d", len(spec.Constants))
	}
}

func TestLoadSpec_MissingSuffix(t *testing.T) {
	yaml := `
primitives:
  - { c_type: jboolean, go_type: uint8 }
reference_types: []
opaque_types: []
function_families: []
env_functions: []
vm_functions: []
constants: []
`
	path := writeTemp(t, yaml)
	_, err := LoadSpec(path)
	if err == nil {
		t.Fatal("expected error for missing suffix")
	}
}

func TestLoadSpec_InvalidYAML(t *testing.T) {
	path := writeTemp(t, "{{invalid yaml")
	_, err := LoadSpec(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadSpec_FileNotFound(t *testing.T) {
	_, err := LoadSpec("/nonexistent/path.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadSpec_RealSpec(t *testing.T) {
	specPath := filepath.Join(findRepoRoot(t), "spec", "jni.yaml")
	if _, err := os.Stat(specPath); err != nil {
		t.Fatalf("required real spec file not found at %s: %v", specPath, err)
	}
	spec, err := LoadSpec(specPath)
	if err != nil {
		t.Fatalf("LoadSpec on real spec failed: %v", err)
	}
	if len(spec.Primitives) != 8 {
		t.Errorf("expected 8 primitives, got %d", len(spec.Primitives))
	}
	if len(spec.FunctionFamilies) < 10 {
		t.Errorf("expected at least 10 function families, got %d", len(spec.FunctionFamilies))
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	// Walk up from the test directory to find go.mod.
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
