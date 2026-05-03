//go:build android

// Command _material_smoke is the cycle-3 build-pipeline gate example.
// It links a NativeActivity APK against the full Material 3 / AndroidX AAR
// closure resolved by aar-resolve, exercises the cycle-3 material.mk +
// apk.mk pipeline (per-AAR aapt2 compile, multi-AAR aapt2 link with
// generated R.txt, javac with the AAR classpath, native multidex via d8),
// and renders a single sentinel string on Canvas so that we can confirm the
// example loads at runtime without depending on any not-yet-bound widget
// bindings (those land in cycles 5+).
package main

/*
#include <android/native_activity.h>
extern void goOnResume(ANativeActivity*);
static void _onResume(ANativeActivity* a) { goOnResume(a); }
extern void goOnNativeWindowCreated(ANativeActivity*, ANativeWindow*);
static void _onWindowCreated(ANativeActivity* a, ANativeWindow* w) { goOnNativeWindowCreated(a, w); }
static void _setCallbacks(ANativeActivity* a) { a->callbacks->onResume = _onResume; a->callbacks->onNativeWindowCreated = _onWindowCreated; }
static uintptr_t _getVM(ANativeActivity* a) { return (uintptr_t)a->vm; }
static uintptr_t _getClazz(ANativeActivity* a) { return (uintptr_t)a->clazz; }
*/
import "C"
import (
	"bytes"
	"fmt"
	"unsafe"

	"github.com/AndroidGoLab/jni"
	"github.com/AndroidGoLab/jni/examples/common/ui"
)

func main() {}

func init() { ui.Register(run) }

//export ANativeActivity_onCreate
func ANativeActivity_onCreate(activity *C.ANativeActivity, savedState unsafe.Pointer, savedStateSize C.size_t) {
	ui.OnCreate(
		jni.VMFromUintptr(uintptr(C._getVM(activity))),
		jni.ObjectFromUintptr(uintptr(C._getClazz(activity))),
	)
	C._setCallbacks(activity)
}

//export goOnResume
func goOnResume(activity *C.ANativeActivity) {
	ui.OnResume(
		jni.ObjectFromUintptr(uintptr(C._getClazz(activity))),
	)
}

//export goOnNativeWindowCreated
func goOnNativeWindowCreated(activity *C.ANativeActivity, window *C.ANativeWindow) {
	ui.OnNativeWindowCreated(unsafe.Pointer(window))
}

func run(vm *jni.VM, output *bytes.Buffer) error {
	// The widget-tree bindings land in cycles 5+. For the cycle-3 build gate
	// it is enough to confirm libexample.so loads on top of the merged
	// Material/AndroidX classpath; the line below renders via the existing
	// Canvas path so a manual on-device check has something to look at.
	fmt.Fprintln(output, "Material smoke ready")
	_ = vm
	return nil
}
