package jni

import (
	"strings"

	"github.com/AndroidGoLab/jni/capi"
)

// findClassByLoaderFallback retries class resolution via the proxy class
// loader when JNI's default FindClass missed. Native threads see the
// bootstrap class loader, which can't resolve AAR/APK classes; the proxy
// classloader (set via SetProxyClassLoader) is the activity's
// PathClassLoader, which knows about every dexed class in the APK
// closure.
//
// Returns 0 when no proxy classloader has been installed or when the
// classloader doesn't know the class either.
func findClassByLoaderFallback(e *Env, name string) capi.Class {
	capi.ExceptionClear(e.ptr)
	javaName := strings.ReplaceAll(name, "/", ".")
	return findClassWithFallback(e.ptr, newCString(name), javaName)
}
