package jni

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/AndroidGoLab/jni/capi"
)

// ProxyHandler is a Go function that handles method invocations on a
// Java interface proxy. It receives the method name and arguments, and
// returns a result object (or nil for void methods) and an optional error.
type ProxyHandler func(env *Env, methodName string, args []*Object) (*Object, error)

// Global registry mapping callback IDs to Go handler closures.
var (
	proxyMu           sync.Mutex
	proxyHandlers     = map[int64]ProxyHandler{}
	proxyFullHandlers = map[int64]ProxyHandlerFull{}
	proxyNextID       atomic.Int64
)

// storeProxyHandler adds a handler to the global registry under the given ID.
func storeProxyHandler(id int64, h ProxyHandler) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	proxyHandlers[id] = h
}

// storeProxyHandlerFull adds a full handler to the global registry under the given ID.
func storeProxyHandlerFull(id int64, h ProxyHandlerFull) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	proxyFullHandlers[id] = h
}

// loadProxyClassLoader returns the current fallback ClassLoader reference.
func loadProxyClassLoader() capi.Object {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	return proxyClassLoader
}

// registerProxy stores a handler and returns a unique ID.
func registerProxy(h ProxyHandler) int64 {
	id := proxyNextID.Add(1)
	storeProxyHandler(id, h)
	return id
}

// registerProxyFull stores a full handler and returns a unique ID.
func registerProxyFull(h ProxyHandlerFull) int64 {
	id := proxyNextID.Add(1)
	storeProxyHandlerFull(id, h)
	return id
}

// unregisterProxy removes a handler by ID from both registries.
func unregisterProxy(id int64) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	delete(proxyHandlers, id)
	delete(proxyFullHandlers, id)
}

// RegisterProxyHandler stores a basic ProxyHandler in the global registry
// and returns a unique handler ID. This is used by the gRPC server layer
// when creating abstract class adapter proxies (which dispatch without
// a Method object). Call UnregisterProxyHandler when the proxy is no
// longer needed.
func RegisterProxyHandler(h ProxyHandler) int64 {
	return registerProxy(h)
}

// UnregisterProxyHandler removes a handler by ID from the global registry.
func UnregisterProxyHandler(id int64) {
	unregisterProxy(id)
}

// lookupProxy retrieves a handler by ID.
func lookupProxy(id int64) (ProxyHandler, bool) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	h, ok := proxyHandlers[id]
	return h, ok
}

// lookupProxyFull retrieves a full handler by ID.
func lookupProxyFull(id int64) (ProxyHandlerFull, bool) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	h, ok := proxyFullHandlers[id]
	return h, ok
}

// proxyInit caches class/method IDs for Proxy and the InvocationHandler
// helper. Thread-safe via sync.Once.
var (
	proxyInitOnce sync.Once
	proxyInitErr  error

	// java.lang.reflect.Proxy
	clsProxy            capi.Class
	midNewProxyInstance capi.JmethodID

	// center.dx.jni.internal.GoInvocationHandler (loaded at init time)
	clsGoHandler     capi.Class
	midHandlerCtr    capi.JmethodID
	fidHandlerID     capi.JfieldID  // GoInvocationHandler.handlerID
	midMethodGetName capi.JmethodID // java.lang.reflect.Method.getName()

	// center.dx.jni.internal.GoAbstractDispatch (loaded at init time)
	clsGoAbstractDispatch capi.Class

	// java.lang.Class.getClassLoader()
	midGetClassLoader capi.JmethodID

	// java.lang.Class (for building Class[] arrays)
	clsClass capi.Class
)

// proxyNativeRegistrar is set by proxy_cgo.go's init() to register
// the native invoke() method on GoInvocationHandler via RegisterNatives.
var proxyNativeRegistrar func(envPtr *capi.Env, cls capi.Class) error

// proxyAbstractRegistrar is set by proxy_cgo.go's init() to register
// the native invoke() method on GoAbstractDispatch via RegisterNatives.
var proxyAbstractRegistrar func(envPtr *capi.Env, cls capi.Class) error

// proxyClassLoader is an optional fallback ClassLoader for finding
// GoInvocationHandler in APK mode (where JNI FindClass from native
// threads uses BootClassLoader which can't see APK classes).
var proxyClassLoader capi.Object

// SetProxyClassLoader sets a fallback ClassLoader that proxy init uses
// to find GoInvocationHandler when JNI FindClass fails. Call this with
// the APK's ClassLoader (from Context.getClassLoader()) before creating
// any proxies. The caller must pass a global ref (not a local ref).
func SetProxyClassLoader(cl *Object) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	if cl != nil {
		proxyClassLoader = cl.Ref()
	}
}

// EnsureProxyInit performs one-time initialization of the proxy
// infrastructure (native method registration for GoInvocationHandler
// and GoAbstractDispatch). Safe to call multiple times.
func EnsureProxyInit(env *Env) error {
	return ensureProxyInit(env)
}

func ensureProxyInit(env *Env) error {
	proxyInitOnce.Do(func() {
		proxyInitErr = doProxyInit(env)
	})
	return proxyInitErr
}

// findClassWithFallback tries FindClass first, then falls back to
// ClassLoader.loadClass() via proxyClassLoader for APK mode where
// native threads use BootClassLoader which can't see APK classes.
func findClassWithFallback(
	env *capi.Env,
	jniName *capi.Cchar,
	javaName string,
) capi.Class {
	cls := capi.FindClass(env, jniName)
	if cls != 0 {
		return cls
	}
	capi.ExceptionClear(env)

	cl := loadProxyClassLoader()
	if cl == 0 {
		return 0
	}

	clCls := capi.FindClass(env, newCString("java/lang/ClassLoader"))
	if clCls == 0 {
		return 0
	}
	defer capi.DeleteLocalRef(env, capi.Object(clCls))

	loadMID := capi.GetMethodID(
		env, clCls,
		newCString("loadClass"),
		newCString("(Ljava/lang/String;)Ljava/lang/Class;"),
	)
	if loadMID == nil {
		return 0
	}

	jName := capi.NewStringUTF(env, newCString(javaName))
	if jName == 0 {
		return 0
	}
	defer capi.DeleteLocalRef(env, capi.Object(jName))

	var nameVal capi.Jvalue
	binary.NativeEndian.PutUint64(nameVal[:], uint64(jName))
	loaded := capi.CallObjectMethodA(env, cl, loadMID, &nameVal)
	if capi.ExceptionCheck(env) == capi.JNI_TRUE {
		capi.ExceptionClear(env)
		return 0
	}
	if loaded != 0 {
		return capi.Class(loaded)
	}
	return 0
}

func doProxyInit(env *Env) (retErr error) {
	// cleanupOnError deletes any GlobalRefs created so far if we return
	// an error partway through initialization.
	defer func() {
		if retErr == nil {
			return
		}
		if clsProxy != 0 {
			capi.DeleteGlobalRef(env.ptr, capi.Object(clsProxy))
			clsProxy = 0
		}
		if clsClass != 0 {
			capi.DeleteGlobalRef(env.ptr, capi.Object(clsClass))
			clsClass = 0
		}
		if clsGoHandler != 0 {
			capi.DeleteGlobalRef(env.ptr, capi.Object(clsGoHandler))
			clsGoHandler = 0
		}
		if clsGoAbstractDispatch != 0 {
			capi.DeleteGlobalRef(env.ptr, capi.Object(clsGoAbstractDispatch))
			clsGoAbstractDispatch = 0
		}
	}()

	// Find java.lang.reflect.Proxy
	proxyName := newCString("java/lang/reflect/Proxy")
	cls := capi.FindClass(env.ptr, proxyName)
	if cls == 0 {
		capi.ExceptionClear(env.ptr)
		return fmt.Errorf("jni: proxy init: cannot find java.lang.reflect.Proxy")
	}
	clsProxy = capi.Class(capi.NewGlobalRef(env.ptr, capi.Object(cls)))
	capi.DeleteLocalRef(env.ptr, capi.Object(cls))

	// Find Proxy.newProxyInstance(ClassLoader, Class[], InvocationHandler) -> Object
	newProxySig := newCString("(Ljava/lang/ClassLoader;[Ljava/lang/Class;Ljava/lang/reflect/InvocationHandler;)Ljava/lang/Object;")
	newProxyName := newCString("newProxyInstance")
	midNewProxyInstance = capi.GetStaticMethodID(env.ptr, clsProxy, newProxyName, newProxySig)
	if midNewProxyInstance == nil {
		capi.ExceptionClear(env.ptr)
		return fmt.Errorf("jni: proxy init: cannot find Proxy.newProxyInstance")
	}

	// Find java.lang.Class (for creating Class[] arrays)
	className := newCString("java/lang/Class")
	cc := capi.FindClass(env.ptr, className)
	if cc == 0 {
		capi.ExceptionClear(env.ptr)
		return fmt.Errorf("jni: proxy init: cannot find java.lang.Class")
	}
	clsClass = capi.Class(capi.NewGlobalRef(env.ptr, capi.Object(cc)))
	capi.DeleteLocalRef(env.ptr, capi.Object(cc))

	// Find Class.getClassLoader()
	getClassLoaderName := newCString("getClassLoader")
	getClassLoaderSig := newCString("()Ljava/lang/ClassLoader;")
	midGetClassLoader = capi.GetMethodID(env.ptr, clsClass, getClassLoaderName, getClassLoaderSig)
	if midGetClassLoader == nil {
		capi.ExceptionClear(env.ptr)
		return fmt.Errorf("jni: proxy init: cannot find Class.getClassLoader")
	}

	// Find center.dx.jni.internal.GoInvocationHandler.
	// FindClass works in app_process mode; falls back to
	// ClassLoader.loadClass() for APK mode.
	hc := findClassWithFallback(
		env.ptr,
		newCString("center/dx/jni/internal/GoInvocationHandler"),
		"center.dx.jni.internal.GoInvocationHandler",
	)
	if hc == 0 {
		return fmt.Errorf("jni: proxy init: cannot find center.dx.jni.internal.GoInvocationHandler — " +
			"ensure the class is on the classpath")
	}
	clsGoHandler = capi.Class(capi.NewGlobalRef(env.ptr, capi.Object(hc)))
	capi.DeleteLocalRef(env.ptr, capi.Object(hc))

	// Find GoInvocationHandler(long) constructor
	initName := newCString("<init>")
	initSig := newCString("(J)V")
	midHandlerCtr = capi.GetMethodID(env.ptr, clsGoHandler, initName, initSig)
	if midHandlerCtr == nil {
		capi.ExceptionClear(env.ptr)
		return fmt.Errorf("jni: proxy init: cannot find GoInvocationHandler constructor")
	}

	// Cache GoInvocationHandler.handlerID field ID (used by native dispatch).
	handlerIDName := newCString("handlerID")
	handlerIDSig := newCString("J")
	fidHandlerID = capi.GetFieldID(env.ptr, clsGoHandler, handlerIDName, handlerIDSig)
	if fidHandlerID == nil {
		capi.ExceptionClear(env.ptr)
		return fmt.Errorf("jni: proxy init: cannot find GoInvocationHandler.handlerID field")
	}

	// Cache java.lang.reflect.Method.getName() for native dispatch.
	methodClassName := newCString("java/lang/reflect/Method")
	mc := capi.FindClass(env.ptr, methodClassName)
	if mc == 0 {
		capi.ExceptionClear(env.ptr)
		return fmt.Errorf("jni: proxy init: cannot find java.lang.reflect.Method")
	}
	defer capi.DeleteLocalRef(env.ptr, capi.Object(mc))

	getNameName := newCString("getName")
	getNameSig := newCString("()Ljava/lang/String;")
	midMethodGetName = capi.GetMethodID(env.ptr, mc, getNameName, getNameSig)
	if midMethodGetName == nil {
		capi.ExceptionClear(env.ptr)
		return fmt.Errorf("jni: proxy init: cannot find Method.getName")
	}

	// Register native invoke() method on GoInvocationHandler if CGo bridge is available.
	if proxyNativeRegistrar != nil {
		if err := proxyNativeRegistrar(env.ptr, clsGoHandler); err != nil {
			return err
		}
	}

	// Find center.dx.jni.internal.GoAbstractDispatch using the same
	// ClassLoader fallback as GoInvocationHandler.
	ac := findClassWithFallback(
		env.ptr,
		newCString("center/dx/jni/internal/GoAbstractDispatch"),
		"center.dx.jni.internal.GoAbstractDispatch",
	)
	if ac == 0 {
		return fmt.Errorf("jni: proxy init: cannot find center.dx.jni.internal.GoAbstractDispatch — " +
			"ensure the class is on the classpath")
	}
	clsGoAbstractDispatch = capi.Class(capi.NewGlobalRef(env.ptr, capi.Object(ac)))
	capi.DeleteLocalRef(env.ptr, capi.Object(ac))

	// Register native invoke() method on GoAbstractDispatch if CGo bridge is available.
	if proxyAbstractRegistrar != nil {
		if err := proxyAbstractRegistrar(env.ptr, clsGoAbstractDispatch); err != nil {
			return err
		}
	}

	return nil
}

// NewProxy creates a java.lang.reflect.Proxy that implements the given
// Java interfaces, dispatching all method calls to the Go handler.
//
// The handler receives the method name and an array of arguments (as
// *Object, with boxed primitives). It returns a result *Object (or nil
// for void) and an optional error (which becomes a RuntimeException on
// the Java side).
//
// The returned cleanup function MUST be called when the proxy is no
// longer needed, to remove the handler from the global registry.
// Failing to call cleanup leaks memory.
//
// Example:
//
//	proxy, cleanup, err := env.NewProxy(
//	    []*Class{listenerClass},
//	    func(env *Env, method string, args []*Object) (*Object, error) {
//	        switch method {
//	        case "onLocationChanged":
//	            // handle location update
//	        }
//	        return nil, nil
//	    },
//	)
//	defer cleanup()
func (e *Env) NewProxy(
	ifaces []*Class,
	handler func(env *Env, methodName string, args []*Object) (*Object, error),
) (proxy *Object, cleanup func(), err error) {
	return newProxyImpl(e, "NewProxy", ifaces, registerProxy(handler))
}

// NewProxyFull creates a java.lang.reflect.Proxy like NewProxy, but
// dispatches to a ProxyHandlerFull that also receives the
// java.lang.reflect.Method object. This allows the handler to inspect
// the method's return type (e.g. to detect void callbacks).
//
// See NewProxy for full documentation on usage and cleanup semantics.
func (e *Env) NewProxyFull(
	ifaces []*Class,
	handler ProxyHandlerFull,
) (proxy *Object, cleanup func(), err error) {
	return newProxyImpl(e, "NewProxyFull", ifaces, registerProxyFull(handler))
}

// newProxyImpl contains the shared logic for NewProxy and NewProxyFull.
// It creates a GoInvocationHandler with the given handlerID, builds the
// interface Class[] array, obtains the ClassLoader, and calls
// Proxy.newProxyInstance.
func newProxyImpl(
	e *Env,
	funcName string,
	ifaces []*Class,
	handlerID int64,
) (proxy *Object, cleanup func(), err error) {
	if len(ifaces) == 0 {
		unregisterProxy(handlerID)
		return nil, nil, fmt.Errorf("jni: %s: at least one interface required", funcName)
	}

	if err := ensureProxyInit(e); err != nil {
		unregisterProxy(handlerID)
		return nil, nil, err
	}

	// Create the GoInvocationHandler instance, passing handlerID as a jlong.
	var idVal capi.Jvalue
	binary.NativeEndian.PutUint64(idVal[:], uint64(handlerID))
	invHandler := capi.NewObjectA(e.ptr, clsGoHandler, midHandlerCtr, &idVal)
	if invHandler == 0 {
		capi.ExceptionClear(e.ptr)
		unregisterProxy(handlerID)
		return nil, nil, fmt.Errorf("jni: %s: failed to create GoInvocationHandler", funcName)
	}
	defer capi.DeleteLocalRef(e.ptr, invHandler)

	// Build the Class[] array for the interfaces.
	ifaceArray := capi.NewObjectArray(e.ptr, capi.Jsize(len(ifaces)), clsClass, 0)
	if ifaceArray == 0 {
		capi.ExceptionClear(e.ptr)
		unregisterProxy(handlerID)
		return nil, nil, fmt.Errorf("jni: %s: failed to create interface array", funcName)
	}
	defer capi.DeleteLocalRef(e.ptr, capi.Object(ifaceArray))

	for i, iface := range ifaces {
		capi.SetObjectArrayElement(e.ptr, ifaceArray, capi.Jint(i), iface.ref)
	}

	// Get the class loader from the first interface.
	// Pass &dummyJvalue instead of nil: the JNI spec does not guarantee
	// NULL is valid for the const jvalue* parameter of Call*MethodA;
	// OpenJ9 segfaults on NULL (eclipse-openj9/openj9#10480).
	var dummyJvalue capi.Jvalue
	classLoader := capi.CallObjectMethodA(e.ptr, capi.Object(ifaces[0].ref), midGetClassLoader, &dummyJvalue)
	if capi.ExceptionCheck(e.ptr) == capi.JNI_TRUE {
		capi.ExceptionClear(e.ptr)
		unregisterProxy(handlerID)
		return nil, nil, fmt.Errorf("jni: %s: failed to get class loader", funcName)
	}
	if classLoader != 0 {
		defer capi.DeleteLocalRef(e.ptr, classLoader)
	}
	// classLoader may be nil (bootstrap loader); that's valid for Proxy.newProxyInstance.

	// Call Proxy.newProxyInstance(classLoader, interfaces, handler).
	args := [3]capi.Jvalue{}
	binary.NativeEndian.PutUint64(args[0][:], uint64(classLoader))
	binary.NativeEndian.PutUint64(args[1][:], uint64(ifaceArray))
	binary.NativeEndian.PutUint64(args[2][:], uint64(invHandler))

	proxyObj := capi.CallStaticObjectMethodA(e.ptr, clsProxy, midNewProxyInstance, &args[0])
	if capi.ExceptionCheck(e.ptr) == capi.JNI_TRUE {
		capi.ExceptionClear(e.ptr)

		// Proxy.newProxyInstance only works with interfaces. For abstract
		// classes, try to find a pre-compiled adapter class that extends the
		// abstract class and delegates to GoAbstractDispatch.invoke().
		if len(ifaces) == 1 {
			adapterObj := tryAbstractAdapter(e, ifaces[0], invHandler)
			if adapterObj != 0 {
				cleanup = func() { unregisterProxy(handlerID) }
				return &Object{ref: adapterObj}, cleanup, nil
			}
		}

		unregisterProxy(handlerID)
		return nil, nil, fmt.Errorf("jni: %s: Proxy.newProxyInstance failed", funcName)
	}
	if proxyObj == 0 {
		unregisterProxy(handlerID)
		return nil, nil, fmt.Errorf("jni: %s: Proxy.newProxyInstance returned null", funcName)
	}

	cleanup = func() {
		unregisterProxy(handlerID)
	}

	return &Object{ref: proxyObj}, cleanup, nil
}

// tryAbstractAdapter attempts to instantiate a pre-compiled Java adapter
// class for an abstract class. These adapters extend the abstract class
// and delegate all method calls to GoAbstractDispatch.invoke(handlerID, ...).
//
// It searches for adapter classes using these naming conventions:
//  1. "center/dx/jni/generated/<TargetPackage>/<SimpleClassName>Adapter"
//  2. "center/dx/jni/generated/<SimpleClassName>Adapter"
//  3. "center/dx/jni/generated/Go<SimpleClassName>Adapter"
//  4. "center/dx/gatt/internal/Go<SimpleClassName>"
//
// The adapter constructor must accept a single long parameter (handlerID).
// Returns 0 if no adapter is found.
func tryAbstractAdapter(
	e *Env,
	targetClass *Class,
	invHandler capi.Object,
) capi.Object {
	// Get the class name via Class.getName(). Use the Env wrapper
	// to call toString() which returns a Java string we can convert.
	clsCls := capi.FindClass(e.ptr, newCString("java/lang/Class"))
	if clsCls == 0 {
		capi.ExceptionClear(e.ptr)
		return 0
	}
	defer capi.DeleteLocalRef(e.ptr, capi.Object(clsCls))

	getNameMID := capi.GetMethodID(e.ptr, clsCls, newCString("getName"), newCString("()Ljava/lang/String;"))
	if getNameMID == nil {
		capi.ExceptionClear(e.ptr)
		return 0
	}

	nameObj := capi.CallObjectMethodA(e.ptr, capi.Object(targetClass.ref), getNameMID, &capi.Jvalue{})
	if nameObj == 0 || capi.ExceptionCheck(e.ptr) == capi.JNI_TRUE {
		capi.ExceptionClear(e.ptr)
		return 0
	}
	defer capi.DeleteLocalRef(e.ptr, nameObj)

	fullName := e.GoString((*String)(unsafe.Pointer(&Object{ref: nameObj})))

	adapterNames := abstractAdapterCandidateNames(fullName)

	for _, name := range adapterNames {
		adapterCls := findClassWithFallback(e.ptr, newCString(name), strings.ReplaceAll(name, "/", "."))
		if adapterCls == 0 {
			continue
		}

		// Find constructor: <init>(long handlerID)
		ctorMID := capi.GetMethodID(e.ptr, adapterCls, newCString("<init>"), newCString("(J)V"))
		if ctorMID == nil {
			capi.ExceptionClear(e.ptr)
			capi.DeleteLocalRef(e.ptr, capi.Object(adapterCls))
			continue
		}

		// Get the handlerID from the GoInvocationHandler.
		handlerIDVal := capi.GetLongField(e.ptr, invHandler, fidHandlerID)

		// Create adapter instance.
		var idArg capi.Jvalue
		binary.NativeEndian.PutUint64(idArg[:], uint64(handlerIDVal))
		obj := capi.NewObjectA(e.ptr, adapterCls, ctorMID, &idArg)
		capi.DeleteLocalRef(e.ptr, capi.Object(adapterCls))
		if obj == 0 || capi.ExceptionCheck(e.ptr) == capi.JNI_TRUE {
			capi.ExceptionClear(e.ptr)
			continue
		}

		return obj
	}

	return 0
}

func abstractAdapterCandidateNames(fullName string) []string {
	simpleName := fullName
	packageName := ""
	if idx := strings.LastIndex(fullName, "."); idx >= 0 {
		packageName = fullName[:idx]
		simpleName = fullName[idx+1:]
	}

	const generatedPrefix = "center/dx/jni/generated"
	adapterNames := make([]string, 0, 4)
	if packageName != "" {
		adapterNames = append(
			adapterNames,
			generatedPrefix+"/"+strings.ReplaceAll(packageName, ".", "/")+"/"+simpleName+"Adapter",
		)
	}
	adapterNames = append(
		adapterNames,
		generatedPrefix+"/"+simpleName+"Adapter",
		generatedPrefix+"/Go"+simpleName+"Adapter",
		"center/dx/gatt/internal/Go"+simpleName,
	)
	return adapterNames
}

// throwGoError throws a Java RuntimeException with the given Go error
// message. Uses raw capi calls to avoid checkException recursion.
func throwGoError(
	env *Env,
	goErr error,
) {
	clsName := newCString("java/lang/RuntimeException")
	cls := capi.FindClass(env.ptr, clsName)
	if cls == 0 {
		capi.ExceptionClear(env.ptr)
		return
	}
	defer capi.DeleteLocalRef(env.ptr, capi.Object(cls))

	msg := goErr.Error()
	msgBytes := newCString(msg)
	capi.ThrowNew(env.ptr, cls, msgBytes)
}

// dispatchProxyInvocation is the Go-side handler called when a proxied
// Java interface method is invoked. It looks up the registered handler
// by ID and delegates to it.
//
// This function is called from the native method registered on
// GoInvocationHandler. In the CGo-enabled build, the actual //export
// entry point lives in proxy_cgo.go.
func dispatchProxyInvocation(
	envPtr *capi.Env,
	handlerID int64,
	methodNameStr capi.String,
	argsArray capi.ObjectArray,
	methodObj capi.Object,
) capi.Object {
	goEnv := &Env{ptr: envPtr}

	// Extract method name using raw capi.
	name := extractGoString(goEnv.ptr, methodNameStr)

	// Extract arguments array.
	var goArgs []*Object
	if argsArray != 0 {
		length := int(capi.GetArrayLength(goEnv.ptr, capi.Array(argsArray)))
		goArgs = make([]*Object, length)
		for i := range length {
			elem := capi.GetObjectArrayElement(goEnv.ptr, argsArray, capi.Jint(i))
			if elem != 0 {
				goArgs[i] = &Object{ref: elem}
			}
		}
	}

	// Check full handlers first (they receive the Method object).
	if hf, ok := lookupProxyFull(handlerID); ok {
		var goMethod *Object
		if methodObj != 0 {
			goMethod = &Object{ref: methodObj}
		}

		result, err := hf(goEnv, goMethod, name, goArgs)
		if err != nil {
			throwGoError(goEnv, err)
			return 0
		}

		if result == nil {
			return 0
		}
		return result.ref
	}

	h, ok := lookupProxy(handlerID)
	if !ok {
		// Handler was unregistered; return null.
		return 0
	}

	result, err := h(goEnv, name, goArgs)
	if err != nil {
		throwGoError(goEnv, err)
		return 0
	}

	if result == nil {
		return 0
	}
	return result.ref
}

// newCString allocates a null-terminated byte slice from a Go string
// and returns a *capi.Cchar pointer suitable for passing to capi functions.
// The backing array is managed by the Go GC and must remain live for the
// duration of the C call.
func newCString(s string) *capi.Cchar {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return (*capi.Cchar)(unsafe.Pointer(&b[0]))
}

// extractGoString converts a JNI String to a Go string using raw capi calls.
func extractGoString(
	env *capi.Env,
	jstr capi.String,
) string {
	if jstr == 0 {
		return ""
	}
	length := capi.GetStringUTFLength(env, jstr)
	if length == 0 {
		return ""
	}
	chars := capi.GetStringUTFChars(env, jstr, nil)
	if chars == nil {
		return ""
	}
	s := unsafe.String((*byte)(unsafe.Pointer(chars)), int(length))
	result := string([]byte(s)) // copy to detach from C memory
	capi.ReleaseStringUTFChars(env, jstr, chars)
	return result
}
