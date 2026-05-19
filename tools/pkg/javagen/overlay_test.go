package javagen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOverlay_Valid(t *testing.T) {
	yaml := `
go_name_overrides:
  doIt: Execute
  getSomething: GetSomething

type_overrides:
  android.location.Location: "*Location"

extra_methods:
  - java_method: extraMethod
    go_name: ExtraMethod
    returns: void
`
	path := writeTempYAML(t, yaml)
	ov, err := LoadOverlay(path)
	if err != nil {
		t.Fatalf("LoadOverlay: %v", err)
	}

	if ov.GoNameOverrides["doIt"] != "Execute" {
		t.Errorf("GoNameOverrides[doIt] = %q", ov.GoNameOverrides["doIt"])
	}
	if ov.TypeOverrides["android.location.Location"] != "*Location" {
		t.Errorf("TypeOverrides = %q", ov.TypeOverrides["android.location.Location"])
	}
	if len(ov.ExtraMethods) != 1 {
		t.Errorf("expected 1 extra method, got %d", len(ov.ExtraMethods))
	}
}

func TestLoadOverlay_FileNotFound(t *testing.T) {
	// When the file does not exist, an empty Overlay is returned.
	ov, err := LoadOverlay("/nonexistent/overlay.yaml")
	if err != nil {
		t.Fatalf("expected no error for missing overlay, got: %v", err)
	}
	if ov == nil {
		t.Fatal("expected non-nil empty overlay")
	}
}

func TestLoadOverlay_Empty(t *testing.T) {
	path := writeTempYAML(t, "{}")
	ov, err := LoadOverlay(path)
	if err != nil {
		t.Fatalf("LoadOverlay: %v", err)
	}
	if ov == nil {
		t.Fatal("expected non-nil overlay")
	}
}

func TestLoadOverlay_InvalidYAML(t *testing.T) {
	path := writeTempYAML(t, "{{invalid")
	_, err := LoadOverlay(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadOverlay_RealOverlays(t *testing.T) {
	root := findRepoRoot(t)
	overlaysDir := filepath.Join(root, "spec", "overlays", "java")

	entries, err := os.ReadDir(overlaysDir)
	if err != nil {
		t.Fatalf("required overlay directory not found at %s: %v", overlaysDir, err)
	}

	overlayCount := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		overlayCount++
		t.Run(e.Name(), func(t *testing.T) {
			path := filepath.Join(overlaysDir, e.Name())
			_, err := LoadOverlay(path)
			if err != nil {
				t.Fatalf("LoadOverlay: %v", err)
			}
		})
	}
	if overlayCount == 0 {
		t.Fatalf("required overlay directory has no YAML files: %s", overlaysDir)
	}
}
