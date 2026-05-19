package javagen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSpec_Valid(t *testing.T) {
	yaml := `
package: testpkg
go_import: "github.com/example/testpkg"

classes:
  - java_class: com.example.Foo
    go_type: Foo
    obtain: system_service
    service_name: "foo"
    methods:
      - java_method: doIt
        go_name: DoIt
        params:
          - { java_type: String, go_name: name }
        returns: int
        error: true

callbacks:
  - java_interface: com.example.Listener
    go_type: Listener
    methods:
      - { java_method: onEvent, params: [String], go_field: OnEvent }

constants:
  - { go_name: X, value: "1", go_type: int }
`
	path := writeTempYAML(t, yaml)
	spec, err := LoadSpec(path)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if spec.Package != "testpkg" {
		t.Errorf("Package = %q", spec.Package)
	}
	if len(spec.Classes) != 1 {
		t.Errorf("expected 1 class, got %d", len(spec.Classes))
	}
	if len(spec.Callbacks) != 1 {
		t.Errorf("expected 1 callback, got %d", len(spec.Callbacks))
	}
	if len(spec.Constants) != 1 {
		t.Errorf("expected 1 constant, got %d", len(spec.Constants))
	}
}

func TestLoadSpec_InvalidPackage(t *testing.T) {
	yaml := `
package: "Invalid-Pkg"
go_import: "github.com/example/test"
classes: []
`
	path := writeTempYAML(t, yaml)
	_, err := LoadSpec(path)
	if err == nil {
		t.Fatal("expected error for invalid package name")
	}
}

func TestLoadSpec_MissingGoImport(t *testing.T) {
	yaml := `
package: test
go_import: ""
classes: []
`
	path := writeTempYAML(t, yaml)
	_, err := LoadSpec(path)
	if err == nil {
		t.Fatal("expected error for missing go_import")
	}
}

func TestLoadSpec_MissingJavaClass(t *testing.T) {
	yaml := `
package: test
go_import: "github.com/example/test"
classes:
  - java_class: ""
    go_type: Foo
`
	path := writeTempYAML(t, yaml)
	_, err := LoadSpec(path)
	if err == nil {
		t.Fatal("expected error for missing java_class")
	}
}

func TestLoadSpec_MissingGoType(t *testing.T) {
	yaml := `
package: test
go_import: "github.com/example/test"
classes:
  - java_class: com.example.Foo
    go_type: ""
`
	path := writeTempYAML(t, yaml)
	_, err := LoadSpec(path)
	if err == nil {
		t.Fatal("expected error for missing go_type")
	}
}

func TestLoadSpec_MissingServiceName(t *testing.T) {
	yaml := `
package: test
go_import: "github.com/example/test"
classes:
  - java_class: com.example.Foo
    go_type: Foo
    obtain: system_service
    service_name: ""
`
	path := writeTempYAML(t, yaml)
	_, err := LoadSpec(path)
	if err == nil {
		t.Fatal("expected error for missing service_name with system_service obtain")
	}
}

func TestLoadSpec_MissingJavaMethod(t *testing.T) {
	yaml := `
package: test
go_import: "github.com/example/test"
classes:
  - java_class: com.example.Foo
    go_type: Foo
    methods:
      - java_method: ""
        go_name: DoIt
`
	path := writeTempYAML(t, yaml)
	_, err := LoadSpec(path)
	if err == nil {
		t.Fatal("expected error for missing java_method")
	}
}

func TestLoadSpec_MissingMethodGoName(t *testing.T) {
	yaml := `
package: test
go_import: "github.com/example/test"
classes:
  - java_class: com.example.Foo
    go_type: Foo
    methods:
      - java_method: doIt
        go_name: ""
`
	path := writeTempYAML(t, yaml)
	_, err := LoadSpec(path)
	if err == nil {
		t.Fatal("expected error for missing method go_name")
	}
}

func TestLoadSpec_MissingParamJavaType(t *testing.T) {
	yaml := `
package: test
go_import: "github.com/example/test"
classes:
  - java_class: com.example.Foo
    go_type: Foo
    methods:
      - java_method: doIt
        go_name: DoIt
        params:
          - { java_type: "", go_name: x }
`
	path := writeTempYAML(t, yaml)
	_, err := LoadSpec(path)
	if err == nil {
		t.Fatal("expected error for missing param java_type")
	}
}

func TestLoadSpec_MissingParamGoName(t *testing.T) {
	yaml := `
package: test
go_import: "github.com/example/test"
classes:
  - java_class: com.example.Foo
    go_type: Foo
    methods:
      - java_method: doIt
        go_name: DoIt
        params:
          - { java_type: int, go_name: "" }
`
	path := writeTempYAML(t, yaml)
	_, err := LoadSpec(path)
	if err == nil {
		t.Fatal("expected error for missing param go_name")
	}
}

func TestLoadSpec_FileNotFound(t *testing.T) {
	_, err := LoadSpec("/nonexistent/path.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadSpec_InvalidYAML(t *testing.T) {
	path := writeTempYAML(t, "{{invalid yaml")
	_, err := LoadSpec(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadSpec_RealSpecs(t *testing.T) {
	root := findRepoRoot(t)
	specsDir := filepath.Join(root, "spec", "java")
	specs, err := filepath.Glob(filepath.Join(specsDir, "*.yaml"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(specs) == 0 {
		t.Fatalf("required real spec files not found in %s", specsDir)
	}

	for _, specPath := range specs {
		name := filepath.Base(specPath)
		t.Run(name, func(t *testing.T) {
			spec, err := LoadSpec(specPath)
			if err != nil {
				t.Fatalf("LoadSpec: %v", err)
			}
			if spec.Package == "" {
				t.Error("package is empty")
			}
		})
	}
}

func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}
