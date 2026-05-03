//go:build android

// Package widgetui is the shared Activity-lifecycle harness for JNI
// examples that build a real Java widget tree (RecyclerView,
// MaterialCardView, FloatingActionButton, …) on top of a NativeActivity.
//
// It is the widget-mode parallel to examples/common/ui, which renders
// plain text on the native Canvas surface. Examples that draw via
// Java widgets share three pieces of bootstrap that this package
// owns once for everyone:
//
//  1. Pointing the JNI proxy machinery at the activity's ClassLoader
//     so the GoInvocationHandler / GoAbstractDispatch path can find
//     classes shipped in the APK's classes.dex.
//  2. Releasing NativeActivity's takeSurface / takeInputQueue
//     callbacks and switching the window pixel format to TRANSLUCENT
//     before setContentView, so the Java view tree paints into the
//     window surface and receives input. Without this dance the
//     opaque GL surface NativeActivity owns occludes any
//     setContentView-attached view tree.
//  3. Running the example's one-shot Setup callback exactly once on
//     the first onResume — onResume is the earliest lifecycle stage
//     where setContentView is reliably accepted.
//
// The cgo //export shims (ANativeActivity_onCreate, goOnResume) must
// live in the example's own main package because cgo only honours
// //export from package main; the example forwards to the public
// OnCreate / OnResume entry points exposed below, exactly as the
// Canvas-mode examples forward to common/ui.
package widgetui

/*
#include <android/log.h>
#include <stdlib.h>

static void widgetuiLogcat(const char* tag, const char* msg) {
    __android_log_print(ANDROID_LOG_INFO, tag, "%s", msg);
}
*/
import "C"
import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/AndroidGoLab/jni"
)

// PixelFormat.TRANSLUCENT — value drawn from the public android.graphics
// PixelFormat constants. NativeActivity installs an opaque RGB_565 format
// on its window; switching to TRANSLUCENT lets the Java view tree paint.
const pixelFormatTranslucent = -3

// Setup is the example's one-shot bootstrap. It is invoked exactly once
// on the Android UI thread on the first onResume, after the proxy class
// loader has been installed and after the window has been made
// translucent. The callee is expected to build its widget tree, attach
// it via Activity.setContentView, register listeners, and return.
//
// Returning a non-nil error logs the error to logcat under LogTag and
// otherwise leaves the activity running so the user still gets a
// (mostly empty) Material window rather than a black NativeActivity
// surface — that lets test harnesses inspect uiautomator dumps even on
// partial-failure paths.
type Setup func(vm *jni.VM, activity *jni.Object) error

// LogTag is the logcat tag used by Logf. Examples can override it
// from init() if they want a per-example tag, but the default matches
// the test_all_apks.sh harness which greps for "GoJNI".
var LogTag = "GoJNI"

// state holds the widgetui-side singleton. Filled in on OnCreate and
// drained on the first OnResume. Guarded by mu.
type state struct {
	mu          sync.Mutex
	vm          *jni.VM
	activity    *jni.Object
	setupFn     Setup
	startedOnce sync.Once
}

var s state

// Register installs setupFn as the example's one-shot setup callback.
// Call from the example's init().
func Register(setupFn Setup) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setupFn = setupFn
}

// OnCreate is called from the cgo ANativeActivity_onCreate shim in the
// example's main package. It captures the VM and activity references
// for use on the first onResume.
func OnCreate(vm *jni.VM, activity *jni.Object) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vm = vm
	s.activity = activity
}

// OnResume is called from the cgo onResume callback in the example's
// main package. It runs the example's Setup exactly once, on the Android
// UI thread (which is where onResume is delivered).
func OnResume() {
	s.startedOnce.Do(func() {
		s.mu.Lock()
		vm, act, setup := s.vm, s.activity, s.setupFn
		s.mu.Unlock()
		runSetup(vm, act, setup)
	})
}

// runSetup performs the bootstrap dance and dispatches to the
// example-supplied Setup. Each step that fails is logged to logcat;
// the boot continues as far as it can to maximise the amount of UI
// state visible to a test harness.
func runSetup(vm *jni.VM, activity *jni.Object, setup Setup) {
	if vm == nil || activity == nil {
		Logf("widgetui: vm or activity unset; aborting setup")
		return
	}
	if setup == nil {
		Logf("widgetui: no Setup registered; aborting setup")
		return
	}

	if err := vm.Do(func(env *jni.Env) error {
		return installProxyClassLoader(env, activity)
	}); err != nil {
		Logf("widgetui: install proxy class loader: %v", err)
		return
	}

	if err := makeWindowTranslucent(vm, activity); err != nil {
		Logf("widgetui: make window translucent: %v", err)
		// Continue: many examples still render usefully without it
		// (e.g., simple Toast-only apps); leave failure visible in
		// logcat for the harness.
	}

	if err := setup(vm, activity); err != nil {
		Logf("widgetui: setup: %v", err)
	}
}

// installProxyClassLoader points the proxy machinery at the activity's
// ClassLoader. The default ClassLoader cannot find classes shipped in
// the APK's classes.dex (GoInvocationHandler, the abstract-adapter
// shims). Without this step env.NewProxy throws ClassNotFoundException
// the moment we try to wrap a Java listener interface.
func installProxyClassLoader(env *jni.Env, activity *jni.Object) error {
	cls := env.GetObjectClass(activity)
	mid, err := env.GetMethodID(cls, "getClassLoader", "()Ljava/lang/ClassLoader;")
	if err != nil {
		return fmt.Errorf("get getClassLoader: %w", err)
	}
	cl, err := env.CallObjectMethod(activity, mid)
	if err != nil {
		return fmt.Errorf("call getClassLoader: %w", err)
	}
	jni.SetProxyClassLoader(env.NewGlobalRef(cl))
	if err := jni.EnsureProxyInit(env); err != nil {
		return fmt.Errorf("EnsureProxyInit: %w", err)
	}
	return nil
}

// makeWindowTranslucent reverts NativeActivity's surface and input
// ownership so the Java view hierarchy can render and dispatch input.
// NativeActivity.onCreate installs itself as both the SurfaceHolder
// callback (takeSurface) and the InputQueue callback (takeInputQueue);
// neither is consumed by our code. We release both back so the view
// tree paints the widgets and the Java input dispatcher receives
// touches. setFormat(TRANSLUCENT) additionally drops the opaque
// RGB_565 format installed by NativeActivity.
//
// Must run BEFORE setContentView; this is the cycle-9 critical
// lifecycle finding.
func makeWindowTranslucent(vm *jni.VM, activity *jni.Object) error {
	return vm.Do(func(env *jni.Env) error {
		actCls := env.GetObjectClass(activity)
		getWindowMid, err := env.GetMethodID(actCls, "getWindow", "()Landroid/view/Window;")
		if err != nil {
			return fmt.Errorf("getMethodID getWindow: %w", err)
		}
		window, err := env.CallObjectMethod(activity, getWindowMid)
		if err != nil {
			return fmt.Errorf("call getWindow: %w", err)
		}
		if window == nil || window.Ref() == 0 {
			return fmt.Errorf("getWindow returned nil")
		}
		defer env.DeleteLocalRef(window)
		winCls := env.GetObjectClass(window)
		takeSurfaceMid, err := env.GetMethodID(winCls, "takeSurface",
			"(Landroid/view/SurfaceHolder$Callback2;)V")
		if err != nil {
			return fmt.Errorf("getMethodID takeSurface: %w", err)
		}
		if err := env.CallVoidMethod(window, takeSurfaceMid, jni.ObjectValue(nil)); err != nil {
			return fmt.Errorf("call takeSurface(null): %w", err)
		}
		takeInputMid, err := env.GetMethodID(winCls, "takeInputQueue",
			"(Landroid/view/InputQueue$Callback;)V")
		if err != nil {
			return fmt.Errorf("getMethodID takeInputQueue: %w", err)
		}
		if err := env.CallVoidMethod(window, takeInputMid, jni.ObjectValue(nil)); err != nil {
			return fmt.Errorf("call takeInputQueue(null): %w", err)
		}
		setFormatMid, err := env.GetMethodID(winCls, "setFormat", "(I)V")
		if err != nil {
			return fmt.Errorf("getMethodID setFormat: %w", err)
		}
		if err := env.CallVoidMethod(window, setFormatMid, jni.IntValue(pixelFormatTranslucent)); err != nil {
			return fmt.Errorf("call setFormat(TRANSLUCENT): %w", err)
		}
		return nil
	})
}

// logSink lets tests intercept Logf output without going through cgo.
// Production code leaves it nil and writes only to logcat.
var (
	logSinkMu sync.Mutex
	logSink   *bytes.Buffer
)

// SetLogSinkForTest captures every Logf line into buf instead of
// emitting via __android_log_print. Pass nil to restore the production
// behaviour. Intended for unit tests of widgetui itself; example code
// must not call this.
func SetLogSinkForTest(buf *bytes.Buffer) {
	logSinkMu.Lock()
	defer logSinkMu.Unlock()
	logSink = buf
}

// Logf formats a line and writes it to logcat under LogTag. Multi-line
// messages are split so each line carries the tag.
func Logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	logSinkMu.Lock()
	sink := logSink
	logSinkMu.Unlock()
	if sink != nil {
		sink.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			sink.WriteByte('\n')
		}
		return
	}
	for _, l := range strings.Split(strings.TrimRight(line, "\n"), "\n") {
		if l == "" {
			continue
		}
		cTag := C.CString(LogTag)
		cMsg := C.CString(l)
		C.widgetuiLogcat(cTag, cMsg)
		C.free(unsafe.Pointer(cTag))
		C.free(unsafe.Pointer(cMsg))
	}
}
