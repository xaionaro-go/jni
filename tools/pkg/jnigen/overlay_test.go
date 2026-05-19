package jnigen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOverlay_Valid(t *testing.T) {
	yaml := `
type_renames:
  jobject: Object
  jclass: Class
  jint: int32

receivers:
  env_functions: "*Env"
  vm_functions: "*VM"

functions:
  GetVersion:
    go_name: GetVersion
    returns: {go_type: int32}
  FindClass:
    go_name: FindClass
    check_exception: true
    params:
      - {name: name, c_type: "const char*", go_type: string, transform: cstring}
    returns: {go_type: "*Class"}

family_overlays:
  "Call{Type}MethodA":
    go_pattern: "Call{Type}Method"
    check_exception: true
    params:
      - {name: obj, c_type: jobject, go_type: "*Object"}
      - {name: method, c_type: jmethodID, go_type: MethodID}
      - {name: args, c_type: "const jvalue*", go_type: "...Value", transform: jvalue_variadic}
    returns:
      primitive: "{go_type}"
      Object: "*Object"
      Void: ~

param_transforms:
  cstring:
    description: "Convert Go string to C string"
    go_type: string
    c_type: "const char*"
    to_c: |
      c_{name} := C.CString({name})
      defer C.free(unsafe.Pointer(c_{name}))
    c_arg: "c_{name}"
`
	path := writeTemp(t, yaml)
	overlay, err := LoadOverlay(path)
	if err != nil {
		t.Fatalf("LoadOverlay failed: %v", err)
	}

	if overlay.TypeRenames["jobject"] != "Object" {
		t.Errorf("expected jobject -> Object, got %q", overlay.TypeRenames["jobject"])
	}
	if overlay.TypeRenames["jint"] != "int32" {
		t.Errorf("expected jint -> int32, got %q", overlay.TypeRenames["jint"])
	}
	if overlay.Receivers["env_functions"] != "*Env" {
		t.Errorf("expected *Env receiver, got %q", overlay.Receivers["env_functions"])
	}

	fc, ok := overlay.Functions["FindClass"]
	if !ok {
		t.Fatal("missing FindClass in overlay functions")
	}
	if !fc.CheckException {
		t.Error("expected FindClass to have check_exception=true")
	}
	if len(fc.Params) != 1 {
		t.Errorf("expected 1 param for FindClass, got %d", len(fc.Params))
	}
	if fc.Params[0].Transform != "cstring" {
		t.Errorf("expected cstring transform, got %q", fc.Params[0].Transform)
	}

	fam, ok := overlay.FamilyOverlays["Call{Type}MethodA"]
	if !ok {
		t.Fatal("missing Call{Type}MethodA in family overlays")
	}
	retMap := fam.FamilyReturnMap()
	if retMap == nil {
		t.Fatal("expected non-nil return map")
	}
	if retMap["Object"] != "*Object" {
		t.Errorf("expected Object return *Object, got %q", retMap["Object"])
	}
	if retMap["Void"] != "" {
		t.Errorf("expected Void return empty, got %q", retMap["Void"])
	}

	pt, ok := overlay.ParamTransforms["cstring"]
	if !ok {
		t.Fatal("missing cstring param transform")
	}
	if pt.GoType != "string" {
		t.Errorf("expected string go_type, got %q", pt.GoType)
	}
}

func TestLoadOverlay_EmptyMaps(t *testing.T) {
	path := writeTemp(t, "{}")
	overlay, err := LoadOverlay(path)
	if err != nil {
		t.Fatalf("LoadOverlay failed: %v", err)
	}
	if overlay.TypeRenames == nil {
		t.Error("expected non-nil TypeRenames")
	}
	if overlay.Functions == nil {
		t.Error("expected non-nil Functions")
	}
}

func TestLoadOverlay_RealOverlay(t *testing.T) {
	overlayPath := filepath.Join(findRepoRoot(t), "spec", "overlays", "jni.yaml")
	if _, err := os.Stat(overlayPath); err != nil {
		t.Fatalf("required real overlay file not found at %s: %v", overlayPath, err)
	}
	overlay, err := LoadOverlay(overlayPath)
	if err != nil {
		t.Fatalf("LoadOverlay on real overlay failed: %v", err)
	}
	if len(overlay.TypeRenames) == 0 {
		t.Error("expected non-empty type renames")
	}
	if len(overlay.Functions) == 0 {
		t.Error("expected non-empty functions")
	}
}
