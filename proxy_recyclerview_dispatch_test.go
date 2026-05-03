//go:build !android

package jni

// proxy_recyclerview_dispatch_test.go verifies that tryAbstractAdapter
// drives the dispatch path correctly for the surface shape exposed by
// RecyclerView.Adapter (post type erasure):
//
//   - Object onCreateViewHolder(Object parent, int viewType)
//   - void   onBindViewHolder(Object holder, int position)
//   - int    getItemCount()
//
// The fixture classes live under internal/testjvm/testdata:
//   - center/dx/jni/internal/TestAbstractAdapter.java       (the abstract class)
//   - center/dx/jni/generated/TestAbstractAdapterAdapter.java (the dispatching subclass)
//
// These four tests exercise: primitive-int return, Object return, void
// return, and primitive-int parameter unboxing on the Go side.

import (
	"testing"
	"unsafe"
)

const (
	abstractAdapterJNIName  = "center/dx/jni/internal/TestAbstractAdapter"
	abstractAdapterJavaName = "center.dx.jni.internal.TestAbstractAdapter"
)

// newAbstractAdapterProxy creates a proxy backed by
// TestAbstractAdapterAdapter via tryAbstractAdapter. It wires `handler` as
// the Go-side dispatch and returns the proxy plus the abstract class
// (needed to resolve method IDs for invocation).
func newAbstractAdapterProxy(
	t *testing.T,
	env *Env,
	handler ProxyHandler,
) (proxy *Object, cls *Class, cleanup func()) {
	t.Helper()
	cls, err := env.FindClass(abstractAdapterJNIName)
	if err != nil {
		t.Fatalf("FindClass(%s): %v", abstractAdapterJavaName, err)
	}

	proxy, proxyCleanup, err := env.NewProxy([]*Class{cls}, handler)
	if err != nil {
		env.DeleteLocalRef(&cls.Object)
		t.Fatalf("NewProxy(TestAbstractAdapter): %v", err)
	}
	if proxy == nil {
		env.DeleteLocalRef(&cls.Object)
		t.Fatal("NewProxy returned nil proxy")
	}

	cleanup = func() {
		proxyCleanup()
		env.DeleteLocalRef(proxy)
		env.DeleteLocalRef(&cls.Object)
	}
	return proxy, cls, cleanup
}

// boxInt creates a fresh java.lang.Integer wrapping `n`. The
// abstract_adapter template emits `(Integer) result.intValue()` for
// primitive int returns, so the Go handler MUST return a boxed Integer
// rather than a raw value.
func boxInt(t *testing.T, env *Env, n int32) *Object {
	t.Helper()
	cls, err := env.FindClass("java/lang/Integer")
	if err != nil {
		t.Fatalf("FindClass(Integer): %v", err)
	}
	defer env.DeleteLocalRef(&cls.Object)
	mid, err := env.GetStaticMethodID(cls, "valueOf", "(I)Ljava/lang/Integer;")
	if err != nil {
		t.Fatalf("GetStaticMethodID(Integer.valueOf): %v", err)
	}
	obj, err := env.CallStaticObjectMethod(cls, mid, IntValue(n))
	if err != nil {
		t.Fatalf("Integer.valueOf(%d): %v", n, err)
	}
	if obj == nil {
		t.Fatalf("Integer.valueOf(%d) returned nil", n)
	}
	return obj
}

// unboxInt extracts the int value out of a java.lang.Integer.
func unboxInt(t *testing.T, env *Env, obj *Object) int32 {
	t.Helper()
	if obj == nil {
		t.Fatal("unboxInt: nil Object")
	}
	cls, err := env.FindClass("java/lang/Integer")
	if err != nil {
		t.Fatalf("FindClass(Integer): %v", err)
	}
	defer env.DeleteLocalRef(&cls.Object)
	mid, err := env.GetMethodID(cls, "intValue", "()I")
	if err != nil {
		t.Fatalf("GetMethodID(intValue): %v", err)
	}
	v, err := env.CallIntMethod(obj, mid)
	if err != nil {
		t.Fatalf("intValue(): %v", err)
	}
	return v
}

// stringFromObject returns the Java toString() of obj as a Go string.
func stringFromObject(t *testing.T, env *Env, obj *Object) string {
	t.Helper()
	if obj == nil {
		t.Fatal("stringFromObject: nil Object")
	}
	objCls, err := env.FindClass("java/lang/Object")
	if err != nil {
		t.Fatalf("FindClass(Object): %v", err)
	}
	defer env.DeleteLocalRef(&objCls.Object)
	mid, err := env.GetMethodID(objCls, "toString", "()Ljava/lang/String;")
	if err != nil {
		t.Fatalf("GetMethodID(toString): %v", err)
	}
	s, err := env.CallObjectMethod(obj, mid)
	if err != nil {
		t.Fatalf("toString(): %v", err)
	}
	defer env.DeleteLocalRef(s)
	// Reinterpret *Object as *String — same in-memory layout (proxy.go uses
	// the same pattern for nameObj at line 542).
	return env.GoString((*String)(unsafe.Pointer(s)))
}

// TestAbstractAdapterDispatch_PrimitiveIntReturn verifies that the
// generated adapter unboxes the Go handler's java.lang.Integer return
// value and delivers it as a primitive int back to Java.
func TestAbstractAdapterDispatch_PrimitiveIntReturn(t *testing.T) {
	withEnv(t, func(env *Env) {
		var seenMethod string
		var seenArgs []*Object
		handler := ProxyHandler(func(e *Env, method string, args []*Object) (*Object, error) {
			seenMethod = method
			seenArgs = args
			return boxInt(t, e, 42), nil
		})

		proxy, cls, cleanup := newAbstractAdapterProxy(t, env, handler)
		defer cleanup()

		mid, err := env.GetMethodID(cls, "getItemCount", "()I")
		if err != nil {
			t.Fatalf("GetMethodID(getItemCount): %v", err)
		}
		got, err := env.CallIntMethod(proxy, mid)
		if err != nil {
			t.Fatalf("CallIntMethod(getItemCount): %v", err)
		}
		if got != 42 {
			t.Errorf("getItemCount() = %d, want 42", got)
		}
		if seenMethod != "getItemCount" {
			t.Errorf("seenMethod = %q, want %q", seenMethod, "getItemCount")
		}
		if len(seenArgs) != 0 {
			t.Errorf("len(seenArgs) = %d, want 0", len(seenArgs))
		}
	})
}

// TestAbstractAdapterDispatch_ObjectReturn verifies that the generated
// adapter passes the Go handler's *Object result back through to the
// Java caller unchanged for an Object-returning method.
func TestAbstractAdapterDispatch_ObjectReturn(t *testing.T) {
	withEnv(t, func(env *Env) {
		const want = "vh"
		handler := ProxyHandler(func(e *Env, method string, args []*Object) (*Object, error) {
			if method != "onCreateViewHolder" {
				t.Errorf("method = %q, want onCreateViewHolder", method)
			}
			s, err := e.NewStringUTF(want)
			if err != nil {
				return nil, err
			}
			return &s.Object, nil
		})

		proxy, cls, cleanup := newAbstractAdapterProxy(t, env, handler)
		defer cleanup()

		mid, err := env.GetMethodID(
			cls, "onCreateViewHolder",
			"(Ljava/lang/Object;I)Ljava/lang/Object;",
		)
		if err != nil {
			t.Fatalf("GetMethodID(onCreateViewHolder): %v", err)
		}
		got, err := env.CallObjectMethod(
			proxy, mid,
			ObjectValue(nil),
			IntValue(7),
		)
		if err != nil {
			t.Fatalf("CallObjectMethod(onCreateViewHolder): %v", err)
		}
		if got == nil || got.Ref() == 0 {
			t.Fatal("onCreateViewHolder returned null Object")
		}
		defer env.DeleteLocalRef(got)
		if s := stringFromObject(t, env, got); s != want {
			t.Errorf("onCreateViewHolder() = %q, want %q", s, want)
		}
	})
}

// TestAbstractAdapterDispatch_VoidMethod verifies that void-returning
// methods correctly drop the Go handler's nil return and that the
// arguments arrive intact at the Go side.
func TestAbstractAdapterDispatch_VoidMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		type call struct {
			method   string
			argCount int
			position int32
		}
		var calls []call
		handler := ProxyHandler(func(e *Env, method string, args []*Object) (*Object, error) {
			c := call{method: method, argCount: len(args), position: -1}
			if len(args) == 2 && args[1] != nil {
				c.position = unboxInt(t, e, args[1])
			}
			calls = append(calls, c)
			return nil, nil
		})

		proxy, cls, cleanup := newAbstractAdapterProxy(t, env, handler)
		defer cleanup()

		mid, err := env.GetMethodID(
			cls, "onBindViewHolder",
			"(Ljava/lang/Object;I)V",
		)
		if err != nil {
			t.Fatalf("GetMethodID(onBindViewHolder): %v", err)
		}
		if err := env.CallVoidMethod(
			proxy, mid,
			ObjectValue(nil),
			IntValue(3),
		); err != nil {
			t.Fatalf("CallVoidMethod(onBindViewHolder): %v", err)
		}

		if len(calls) != 1 {
			t.Fatalf("len(calls) = %d, want 1", len(calls))
		}
		if calls[0].method != "onBindViewHolder" {
			t.Errorf("method = %q, want onBindViewHolder", calls[0].method)
		}
		if calls[0].argCount != 2 {
			t.Errorf("argCount = %d, want 2", calls[0].argCount)
		}
		if calls[0].position != 3 {
			t.Errorf("position = %d, want 3", calls[0].position)
		}
	})
}

// TestAbstractAdapterDispatch_ParamUnboxing verifies that a primitive
// int parameter (viewType) arrives at the Go handler as a java.lang.Integer
// that round-trips through unboxInt.
func TestAbstractAdapterDispatch_ParamUnboxing(t *testing.T) {
	withEnv(t, func(env *Env) {
		var receivedViewType int32 = -1
		handler := ProxyHandler(func(e *Env, method string, args []*Object) (*Object, error) {
			if len(args) != 2 {
				t.Errorf("len(args) = %d, want 2", len(args))
				return nil, nil
			}
			if args[1] == nil {
				t.Error("args[1] (viewType) is nil; expected boxed Integer")
				return nil, nil
			}
			receivedViewType = unboxInt(t, e, args[1])
			s, err := e.NewStringUTF("vh")
			if err != nil {
				return nil, err
			}
			return &s.Object, nil
		})

		proxy, cls, cleanup := newAbstractAdapterProxy(t, env, handler)
		defer cleanup()

		mid, err := env.GetMethodID(
			cls, "onCreateViewHolder",
			"(Ljava/lang/Object;I)Ljava/lang/Object;",
		)
		if err != nil {
			t.Fatalf("GetMethodID(onCreateViewHolder): %v", err)
		}
		got, err := env.CallObjectMethod(
			proxy, mid,
			ObjectValue(nil),
			IntValue(7),
		)
		if err != nil {
			t.Fatalf("CallObjectMethod(onCreateViewHolder): %v", err)
		}
		if got != nil {
			defer env.DeleteLocalRef(got)
		}

		if receivedViewType != 7 {
			t.Errorf("receivedViewType = %d, want 7", receivedViewType)
		}
	})
}
