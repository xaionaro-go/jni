package jni

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"github.com/AndroidGoLab/jni/capi"
	"github.com/AndroidGoLab/jni/internal/testjvm"
)

var testVM *VM

func TestMain(m *testing.M) {
	runtime.LockOSThread()

	// Find testdata directory relative to this source file for the GoInvocationHandler class.
	classpath := "internal/testjvm/testdata"

	vmPtr, _, err := testjvm.Create(classpath)
	if err != nil {
		panic(err)
	}
	testVM = VMFromPtr(vmPtr)

	os.Exit(m.Run())
}

// withEnv runs fn inside testVM.Do to ensure a valid JNI env on the current thread.
func withEnv(t *testing.T, fn func(env *Env)) {
	t.Helper()
	err := testVM.Do(func(env *Env) error {
		fn(env)
		return nil
	})
	if err != nil {
		t.Fatalf("VM.Do: %v", err)
	}
}

func requireJavac(t *testing.T) string {
	t.Helper()
	javac, err := exec.LookPath("javac")
	if err != nil {
		t.Fatalf("javac not available: %v", err)
	}
	return javac
}

func compileJavaFixture(
	t *testing.T,
	sourceRelPath string,
	source string,
) string {
	t.Helper()
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src", filepath.FromSlash(sourceRelPath))
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	classesDir := filepath.Join(tmp, "classes")
	cmd := exec.Command(requireJavac(t), "-d", classesDir, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("javac fixture: %v\n%s", err, out)
	}
	return classesDir
}

// --- Version ---

func TestGetVersion(t *testing.T) {
	withEnv(t, func(env *Env) {
		v := env.GetVersion()
		if v < int32(JNI_VERSION_1_8) {
			t.Errorf("GetVersion() = %d, want >= JNI_VERSION_1_8", v)
		}
	})
}

// --- Class operations ---

func TestFindClass(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/String")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		if cls == nil {
			t.Fatal("FindClass returned nil")
		}
		env.DeleteLocalRef(&cls.Object)
	})
}

func TestFindClassNotFound(t *testing.T) {
	withEnv(t, func(env *Env) {
		_, err := env.FindClass("no/such/Class")
		if err == nil {
			t.Fatal("expected error for non-existent class")
		}
	})
}

func TestFindClassUsesProxyClassLoaderFallbackAfterException(t *testing.T) {
	classesDir := compileJavaFixture(
		t,
		filepath.Join("test", "fallback", "FallbackOnly.java"),
		"package test.fallback; public class FallbackOnly {}\n",
	)

	withEnv(t, func(env *Env) {
		loader := newURLClassLoaderForDir(t, env, classesDir)
		loaderGlobal := env.NewGlobalRef(loader)
		env.DeleteLocalRef(loader)
		defer env.DeleteGlobalRef(loaderGlobal)

		proxyMu.Lock()
		oldLoader := proxyClassLoader
		proxyClassLoader = loaderGlobal.Ref()
		proxyMu.Unlock()
		defer func() {
			proxyMu.Lock()
			proxyClassLoader = oldLoader
			proxyMu.Unlock()
		}()

		cls, err := env.FindClass("test/fallback/FallbackOnly")
		if err != nil {
			t.Fatalf("FindClass via loader fallback: %v", err)
		}
		if cls == nil {
			t.Fatal("FindClass returned nil for fallback-loaded class")
		}
		env.DeleteLocalRef(&cls.Object)
	})
}

func newURLClassLoaderForDir(t *testing.T, env *Env, classesDir string) *Object {
	t.Helper()

	fileCls, err := env.FindClass("java/io/File")
	if err != nil {
		t.Fatalf("FindClass(File): %v", err)
	}
	defer env.DeleteLocalRef(&fileCls.Object)
	fileCtor, err := env.GetMethodID(fileCls, "<init>", "(Ljava/lang/String;)V")
	if err != nil {
		t.Fatalf("File.<init>: %v", err)
	}
	path, err := env.NewStringUTF(classesDir)
	if err != nil {
		t.Fatalf("NewStringUTF(classesDir): %v", err)
	}
	defer env.DeleteLocalRef(&path.Object)
	fileObj, err := env.NewObject(fileCls, fileCtor, ObjectValue(&path.Object))
	if err != nil {
		t.Fatalf("new File: %v", err)
	}
	defer env.DeleteLocalRef(fileObj)

	toURIMid, err := env.GetMethodID(fileCls, "toURI", "()Ljava/net/URI;")
	if err != nil {
		t.Fatalf("File.toURI: %v", err)
	}
	uriObj, err := env.CallObjectMethod(fileObj, toURIMid)
	if err != nil {
		t.Fatalf("File.toURI(): %v", err)
	}
	defer env.DeleteLocalRef(uriObj)
	uriCls := env.GetObjectClass(uriObj)
	defer env.DeleteLocalRef(&uriCls.Object)
	toURLMid, err := env.GetMethodID(uriCls, "toURL", "()Ljava/net/URL;")
	if err != nil {
		t.Fatalf("URI.toURL: %v", err)
	}
	urlObj, err := env.CallObjectMethod(uriObj, toURLMid)
	if err != nil {
		t.Fatalf("URI.toURL(): %v", err)
	}
	defer env.DeleteLocalRef(urlObj)

	urlCls, err := env.FindClass("java/net/URL")
	if err != nil {
		t.Fatalf("FindClass(URL): %v", err)
	}
	defer env.DeleteLocalRef(&urlCls.Object)
	urls, err := env.NewObjectArray(1, urlCls, nil)
	if err != nil {
		t.Fatalf("NewObjectArray(URL): %v", err)
	}
	if urls == nil {
		t.Fatal("NewObjectArray(URL) returned nil")
	}
	defer env.DeleteLocalRef(&urls.Object)
	if err := env.SetObjectArrayElement(urls, 0, urlObj); err != nil {
		t.Fatalf("SetObjectArrayElement(URL): %v", err)
	}

	loaderCls, err := env.FindClass("java/net/URLClassLoader")
	if err != nil {
		t.Fatalf("FindClass(URLClassLoader): %v", err)
	}
	defer env.DeleteLocalRef(&loaderCls.Object)
	loaderCtor, err := env.GetMethodID(loaderCls, "<init>", "([Ljava/net/URL;)V")
	if err != nil {
		t.Fatalf("URLClassLoader.<init>: %v", err)
	}
	loader, err := env.NewObject(loaderCls, loaderCtor, ObjectValue(&urls.Object))
	if err != nil {
		t.Fatalf("new URLClassLoader: %v", err)
	}
	return loader
}

func TestGetObjectClass(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, err := env.NewStringUTF("hello")
		if err != nil {
			t.Fatalf("NewStringUTF: %v", err)
		}
		cls := env.GetObjectClass(&str.Object)
		if cls == nil {
			t.Fatal("GetObjectClass returned nil")
		}
		env.DeleteLocalRef(&cls.Object)
		env.DeleteLocalRef(&str.Object)
	})
}

func TestGetSuperclass(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/String")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		super := env.GetSuperclass(cls)
		if super == nil {
			t.Fatal("GetSuperclass returned nil")
		}
		env.DeleteLocalRef(&super.Object)
		env.DeleteLocalRef(&cls.Object)
	})
}

func TestIsInstanceOf(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, err := env.NewStringUTF("test")
		if err != nil {
			t.Fatalf("NewStringUTF: %v", err)
		}
		cls, err := env.FindClass("java/lang/String")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		if !env.IsInstanceOf(&str.Object, cls) {
			t.Error("expected String to be instance of String")
		}
		env.DeleteLocalRef(&cls.Object)
		env.DeleteLocalRef(&str.Object)
	})
}

func TestIsAssignableFrom(t *testing.T) {
	withEnv(t, func(env *Env) {
		strCls, err := env.FindClass("java/lang/String")
		if err != nil {
			t.Fatalf("FindClass String: %v", err)
		}
		objCls, err := env.FindClass("java/lang/Object")
		if err != nil {
			t.Fatalf("FindClass Object: %v", err)
		}
		if !env.IsAssignableFrom(strCls, objCls) {
			t.Error("String should be assignable to Object")
		}
		if env.IsAssignableFrom(objCls, strCls) {
			t.Error("Object should NOT be assignable to String")
		}
		env.DeleteLocalRef(&objCls.Object)
		env.DeleteLocalRef(&strCls.Object)
	})
}

// --- String operations ---

func TestNewStringUTF(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, err := env.NewStringUTF("hello world")
		if err != nil {
			t.Fatalf("NewStringUTF: %v", err)
		}
		l := env.GetStringUTFLength(str)
		if l != 11 {
			t.Errorf("GetStringUTFLength = %d, want 11", l)
		}
		env.DeleteLocalRef(&str.Object)
	})
}

func TestGetStringLength(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, err := env.NewStringUTF("abc")
		if err != nil {
			t.Fatalf("NewStringUTF: %v", err)
		}
		l := env.GetStringLength(str)
		if l != 3 {
			t.Errorf("GetStringLength = %d, want 3", l)
		}
		env.DeleteLocalRef(&str.Object)
	})
}

func TestNewString(t *testing.T) {
	withEnv(t, func(env *Env) {
		chars := []uint16{'H', 'i'}
		str, err := env.NewString(chars)
		if err != nil {
			t.Fatalf("NewString: %v", err)
		}
		l := env.GetStringLength(str)
		if l != 2 {
			t.Errorf("GetStringLength = %d, want 2", l)
		}
		env.DeleteLocalRef(&str.Object)
	})
}

func TestGetStringUTFCharsAndRelease(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, err := env.NewStringUTF("test")
		if err != nil {
			t.Fatalf("NewStringUTF: %v", err)
		}
		chars, err := env.GetStringUTFChars(str, nil)
		if err != nil {
			t.Fatalf("GetStringUTFChars: %v", err)
		}
		got := unsafe.String((*byte)(chars), 4)
		if got != "test" {
			t.Errorf("got %q, want %q", got, "test")
		}
		env.ReleaseStringUTFChars(str, chars)
		env.DeleteLocalRef(&str.Object)
	})
}

func TestGetStringCharsAndRelease(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, err := env.NewStringUTF("AB")
		if err != nil {
			t.Fatalf("NewStringUTF: %v", err)
		}
		chars, err := env.GetStringChars(str, nil)
		if err != nil {
			t.Fatalf("GetStringChars: %v", err)
		}
		arr := (*[2]uint16)(chars)
		if arr[0] != 'A' || arr[1] != 'B' {
			t.Errorf("GetStringChars = %v, want [A, B]", arr)
		}
		env.ReleaseStringChars(str, chars)
		env.DeleteLocalRef(&str.Object)
	})
}

func TestGetStringRegion(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, err := env.NewStringUTF("abcdef")
		if err != nil {
			t.Fatalf("NewStringUTF: %v", err)
		}
		buf := make([]uint16, 3)
		err = env.GetStringRegion(str, 1, 3, buf)
		if err != nil {
			t.Fatalf("GetStringRegion: %v", err)
		}
		if buf[0] != 'b' || buf[1] != 'c' || buf[2] != 'd' {
			t.Errorf("GetStringRegion = %v, want [b, c, d]", buf)
		}
		env.DeleteLocalRef(&str.Object)
	})
}

func TestGetStringUTFRegion(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, err := env.NewStringUTF("hello")
		if err != nil {
			t.Fatalf("NewStringUTF: %v", err)
		}
		buf := make([]byte, 3)
		err = env.GetStringUTFRegion(str, 1, 3, buf)
		if err != nil {
			t.Fatalf("GetStringUTFRegion: %v", err)
		}
		if string(buf) != "ell" {
			t.Errorf("GetStringUTFRegion = %q, want %q", buf, "ell")
		}
		env.DeleteLocalRef(&str.Object)
	})
}

func TestGetStringCritical(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, err := env.NewStringUTF("XY")
		if err != nil {
			t.Fatalf("NewStringUTF: %v", err)
		}
		ptr := env.GetStringCritical(str, nil)
		if ptr == nil {
			t.Fatal("GetStringCritical returned nil")
		}
		env.ReleaseStringCritical(str, ptr)
		env.DeleteLocalRef(&str.Object)
	})
}

// --- Method invocation ---

func TestCallObjectMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, err := env.NewStringUTF("Hello World")
		if err != nil {
			t.Fatalf("NewStringUTF: %v", err)
		}
		cls := env.GetObjectClass(&str.Object)
		mid, err := env.GetMethodID(cls, "toLowerCase", "()Ljava/lang/String;")
		if err != nil {
			t.Fatalf("GetMethodID: %v", err)
		}
		result, err := env.CallObjectMethod(&str.Object, mid)
		if err != nil {
			t.Fatalf("CallObjectMethod: %v", err)
		}
		if result == nil {
			t.Fatal("CallObjectMethod returned nil")
		}
		env.DeleteLocalRef(result)
		env.DeleteLocalRef(&cls.Object)
		env.DeleteLocalRef(&str.Object)
	})
}

func TestCallIntMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, err := env.NewStringUTF("abc")
		if err != nil {
			t.Fatalf("NewStringUTF: %v", err)
		}
		cls := env.GetObjectClass(&str.Object)
		mid, err := env.GetMethodID(cls, "length", "()I")
		if err != nil {
			t.Fatalf("GetMethodID: %v", err)
		}
		length, err := env.CallIntMethod(&str.Object, mid)
		if err != nil {
			t.Fatalf("CallIntMethod: %v", err)
		}
		if length != 3 {
			t.Errorf("length = %d, want 3", length)
		}
		env.DeleteLocalRef(&cls.Object)
		env.DeleteLocalRef(&str.Object)
	})
}

func TestCallBooleanMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, err := env.NewStringUTF("")
		if err != nil {
			t.Fatalf("NewStringUTF: %v", err)
		}
		cls := env.GetObjectClass(&str.Object)
		mid, err := env.GetMethodID(cls, "isEmpty", "()Z")
		if err != nil {
			t.Fatalf("GetMethodID: %v", err)
		}
		result, err := env.CallBooleanMethod(&str.Object, mid)
		if err != nil {
			t.Fatalf("CallBooleanMethod: %v", err)
		}
		if result == 0 {
			t.Error("expected true for empty string isEmpty()")
		}
		env.DeleteLocalRef(&cls.Object)
		env.DeleteLocalRef(&str.Object)
	})
}

func TestCallCharMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, err := env.NewStringUTF("abc")
		if err != nil {
			t.Fatalf("NewStringUTF: %v", err)
		}
		cls := env.GetObjectClass(&str.Object)
		mid, err := env.GetMethodID(cls, "charAt", "(I)C")
		if err != nil {
			t.Fatalf("GetMethodID: %v", err)
		}
		ch, err := env.CallCharMethod(&str.Object, mid, IntValue(1))
		if err != nil {
			t.Fatalf("CallCharMethod: %v", err)
		}
		if ch != 'b' {
			t.Errorf("charAt(1) = %c, want 'b'", ch)
		}
		env.DeleteLocalRef(&cls.Object)
		env.DeleteLocalRef(&str.Object)
	})
}

func TestCallVoidMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		sbCls, err := env.FindClass("java/lang/StringBuilder")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		ctor, err := env.GetMethodID(sbCls, "<init>", "()V")
		if err != nil {
			t.Fatalf("GetMethodID <init>: %v", err)
		}
		sb, err := env.NewObject(sbCls, ctor)
		if err != nil {
			t.Fatalf("NewObject: %v", err)
		}
		mid, err := env.GetMethodID(sbCls, "setLength", "(I)V")
		if err != nil {
			t.Fatalf("GetMethodID setLength: %v", err)
		}
		err = env.CallVoidMethod(sb, mid, IntValue(0))
		if err != nil {
			t.Fatalf("CallVoidMethod: %v", err)
		}
		env.DeleteLocalRef(sb)
		env.DeleteLocalRef(&sbCls.Object)
	})
}

// --- Static method invocation ---

func TestCallStaticIntMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/Integer")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		mid, err := env.GetStaticMethodID(cls, "parseInt", "(Ljava/lang/String;)I")
		if err != nil {
			t.Fatalf("GetStaticMethodID: %v", err)
		}
		str, err := env.NewStringUTF("42")
		if err != nil {
			t.Fatalf("NewStringUTF: %v", err)
		}
		result, err := env.CallStaticIntMethod(cls, mid, ObjectValue(&str.Object))
		if err != nil {
			t.Fatalf("CallStaticIntMethod: %v", err)
		}
		if result != 42 {
			t.Errorf("parseInt(\"42\") = %d, want 42", result)
		}
		env.DeleteLocalRef(&str.Object)
		env.DeleteLocalRef(&cls.Object)
	})
}

func TestCallStaticObjectMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/String")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		mid, err := env.GetStaticMethodID(cls, "valueOf", "(I)Ljava/lang/String;")
		if err != nil {
			t.Fatalf("GetStaticMethodID: %v", err)
		}
		result, err := env.CallStaticObjectMethod(cls, mid, IntValue(123))
		if err != nil {
			t.Fatalf("CallStaticObjectMethod: %v", err)
		}
		if result == nil {
			t.Fatal("expected non-nil result")
		}
		env.DeleteLocalRef(result)
		env.DeleteLocalRef(&cls.Object)
	})
}

func TestCallStaticBooleanMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/Boolean")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		mid, err := env.GetStaticMethodID(cls, "parseBoolean", "(Ljava/lang/String;)Z")
		if err != nil {
			t.Fatalf("GetStaticMethodID: %v", err)
		}
		str, _ := env.NewStringUTF("true")
		result, err := env.CallStaticBooleanMethod(cls, mid, ObjectValue(&str.Object))
		if err != nil {
			t.Fatalf("CallStaticBooleanMethod: %v", err)
		}
		if result == 0 {
			t.Error("expected true")
		}
		env.DeleteLocalRef(&str.Object)
		env.DeleteLocalRef(&cls.Object)
	})
}

func TestCallStaticLongMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/Long")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		mid, err := env.GetStaticMethodID(cls, "parseLong", "(Ljava/lang/String;)J")
		if err != nil {
			t.Fatalf("GetStaticMethodID: %v", err)
		}
		str, _ := env.NewStringUTF("1099511627776")
		result, err := env.CallStaticLongMethod(cls, mid, ObjectValue(&str.Object))
		if err != nil {
			t.Fatalf("CallStaticLongMethod: %v", err)
		}
		if result != 1099511627776 {
			t.Errorf("got %d, want 1099511627776", result)
		}
		env.DeleteLocalRef(&str.Object)
		env.DeleteLocalRef(&cls.Object)
	})
}

func TestCallStaticDoubleMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/Math")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		mid, err := env.GetStaticMethodID(cls, "sqrt", "(D)D")
		if err != nil {
			t.Fatalf("GetStaticMethodID: %v", err)
		}
		result, err := env.CallStaticDoubleMethod(cls, mid, DoubleValue(4.0))
		if err != nil {
			t.Fatalf("CallStaticDoubleMethod: %v", err)
		}
		if result != 2.0 {
			t.Errorf("sqrt(4) = %f, want 2.0", result)
		}
		env.DeleteLocalRef(&cls.Object)
	})
}

func TestCallStaticFloatMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/Float")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		mid, err := env.GetStaticMethodID(cls, "parseFloat", "(Ljava/lang/String;)F")
		if err != nil {
			t.Fatalf("GetStaticMethodID: %v", err)
		}
		str, _ := env.NewStringUTF("3.14")
		result, err := env.CallStaticFloatMethod(cls, mid, ObjectValue(&str.Object))
		if err != nil {
			t.Fatalf("CallStaticFloatMethod: %v", err)
		}
		if result < 3.13 || result > 3.15 {
			t.Errorf("parseFloat(\"3.14\") = %f", result)
		}
		env.DeleteLocalRef(&str.Object)
		env.DeleteLocalRef(&cls.Object)
	})
}

func TestCallStaticVoidMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/System")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		mid, err := env.GetStaticMethodID(cls, "gc", "()V")
		if err != nil {
			t.Fatalf("GetStaticMethodID: %v", err)
		}
		err = env.CallStaticVoidMethod(cls, mid)
		if err != nil {
			t.Fatalf("CallStaticVoidMethod: %v", err)
		}
		env.DeleteLocalRef(&cls.Object)
	})
}

func TestCallShortByteMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/Integer")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		ctor, err := env.GetStaticMethodID(cls, "valueOf", "(I)Ljava/lang/Integer;")
		if err != nil {
			t.Fatalf("GetStaticMethodID: %v", err)
		}
		intObj, err := env.CallStaticObjectMethod(cls, ctor, IntValue(42))
		if err != nil {
			t.Fatalf("CallStaticObjectMethod: %v", err)
		}
		shortMid, _ := env.GetMethodID(cls, "shortValue", "()S")
		sv, err := env.CallShortMethod(intObj, shortMid)
		if err != nil {
			t.Fatalf("CallShortMethod: %v", err)
		}
		if sv != 42 {
			t.Errorf("shortValue() = %d, want 42", sv)
		}
		byteMid, _ := env.GetMethodID(cls, "byteValue", "()B")
		bv, err := env.CallByteMethod(intObj, byteMid)
		if err != nil {
			t.Fatalf("CallByteMethod: %v", err)
		}
		if bv != 42 {
			t.Errorf("byteValue() = %d, want 42", bv)
		}
		env.DeleteLocalRef(intObj)
		env.DeleteLocalRef(&cls.Object)
	})
}

// --- Field operations ---

func TestGetStaticIntField(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/Integer")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		fid, err := env.GetStaticFieldID(cls, "MAX_VALUE", "I")
		if err != nil {
			t.Fatalf("GetStaticFieldID: %v", err)
		}
		val := env.GetStaticIntField(cls, fid)
		if val != 2147483647 {
			t.Errorf("Integer.MAX_VALUE = %d, want 2147483647", val)
		}
		env.DeleteLocalRef(&cls.Object)
	})
}

func TestGetStaticObjectField(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/Boolean")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		fid, err := env.GetStaticFieldID(cls, "TRUE", "Ljava/lang/Boolean;")
		if err != nil {
			t.Fatalf("GetStaticFieldID: %v", err)
		}
		obj := env.GetStaticObjectField(cls, fid)
		if obj == nil {
			t.Fatal("GetStaticObjectField returned nil")
		}
		env.DeleteLocalRef(obj)
		env.DeleteLocalRef(&cls.Object)
	})
}

func TestGetFieldIDNotFound(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/StringBuilder")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		_, err = env.GetFieldID(cls, "nonexistent", "I")
		if err == nil {
			t.Error("expected error for non-existent field")
		}
		env.DeleteLocalRef(&cls.Object)
	})
}

// --- Object creation ---

func TestNewObject(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/StringBuilder")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		ctor, err := env.GetMethodID(cls, "<init>", "()V")
		if err != nil {
			t.Fatalf("GetMethodID: %v", err)
		}
		obj, err := env.NewObject(cls, ctor)
		if err != nil {
			t.Fatalf("NewObject: %v", err)
		}
		if obj == nil {
			t.Fatal("NewObject returned nil")
		}
		env.DeleteLocalRef(obj)
		env.DeleteLocalRef(&cls.Object)
	})
}

func TestAllocObject(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, err := env.FindClass("java/lang/Object")
		if err != nil {
			t.Fatalf("FindClass: %v", err)
		}
		obj, err := env.AllocObject(cls)
		if err != nil {
			t.Fatalf("AllocObject: %v", err)
		}
		if obj == nil {
			t.Fatal("AllocObject returned nil")
		}
		env.DeleteLocalRef(obj)
		env.DeleteLocalRef(&cls.Object)
	})
}

// --- Reference management ---

func TestGlobalRef(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, _ := env.NewStringUTF("global")
		global := env.NewGlobalRef(&str.Object)
		if global == nil {
			t.Fatal("NewGlobalRef returned nil")
		}
		refType := env.GetObjectRefType(global)
		if refType != JNIGlobalRefType {
			t.Errorf("ref type = %d, want %d", refType, JNIGlobalRefType)
		}
		env.DeleteGlobalRef(global)
		env.DeleteLocalRef(&str.Object)
	})
}

func TestLocalRef(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, _ := env.NewStringUTF("local")
		local := env.NewLocalRef(&str.Object)
		if local == nil {
			t.Fatal("NewLocalRef returned nil")
		}
		refType := env.GetObjectRefType(local)
		if refType != JNILocalRefType {
			t.Errorf("ref type = %d, want %d", refType, JNILocalRefType)
		}
		env.DeleteLocalRef(local)
		env.DeleteLocalRef(&str.Object)
	})
}

func TestWeakGlobalRef(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, _ := env.NewStringUTF("weak")
		weak := env.NewWeakGlobalRef(&str.Object)
		if weak == nil {
			t.Fatal("NewWeakGlobalRef returned nil")
		}
		refType := env.GetObjectRefType(&weak.Object)
		if refType != JNIWeakGlobalRefType {
			t.Errorf("ref type = %d, want %d", refType, JNIWeakGlobalRefType)
		}
		env.DeleteWeakGlobalRef(weak)
		env.DeleteLocalRef(&str.Object)
	})
}

func TestIsSameObject(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, _ := env.NewStringUTF("same")
		if !env.IsSameObject(&str.Object, &str.Object) {
			t.Error("expected IsSameObject(x, x) = true")
		}
		str2, _ := env.NewStringUTF("different")
		if env.IsSameObject(&str.Object, &str2.Object) {
			t.Error("expected IsSameObject(x, y) = false")
		}
		env.DeleteLocalRef(&str2.Object)
		env.DeleteLocalRef(&str.Object)
	})
}

// --- Local frame management ---

func TestPushPopLocalFrame(t *testing.T) {
	withEnv(t, func(env *Env) {
		err := env.PushLocalFrame(16)
		if err != nil {
			t.Fatalf("PushLocalFrame: %v", err)
		}
		str, _ := env.NewStringUTF("in frame")
		result := env.PopLocalFrame(&str.Object)
		if result == nil {
			t.Fatal("PopLocalFrame returned nil")
		}
		env.DeleteLocalRef(result)
	})
}

func TestEnsureLocalCapacity(t *testing.T) {
	withEnv(t, func(env *Env) {
		err := env.EnsureLocalCapacity(32)
		if err != nil {
			t.Fatalf("EnsureLocalCapacity: %v", err)
		}
	})
}

// --- Exception handling ---

func TestExceptionCheckAndClear(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, _ := env.FindClass("java/lang/Integer")
		mid, _ := env.GetStaticMethodID(cls, "parseInt", "(Ljava/lang/String;)I")
		badStr, _ := env.NewStringUTF("not_a_number")
		_, err := env.CallStaticIntMethod(cls, mid, ObjectValue(&badStr.Object))
		if err == nil {
			t.Fatal("expected exception from parseInt(\"not_a_number\")")
		}
		env.DeleteLocalRef(&badStr.Object)
		env.DeleteLocalRef(&cls.Object)
	})
}

func TestThrowNew(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, _ := env.FindClass("java/lang/RuntimeException")
		err := env.ThrowNew(cls, "test exception")
		if err != nil {
			t.Fatalf("ThrowNew: %v", err)
		}
		if !env.ExceptionCheck() {
			t.Fatal("expected pending exception after ThrowNew")
		}
		exc := env.ExceptionOccurred()
		if exc == nil {
			t.Fatal("ExceptionOccurred returned nil")
		}
		env.ExceptionClear()
		env.DeleteLocalRef(&exc.Object)
		env.DeleteLocalRef(&cls.Object)
	})
}

// --- Array operations ---

func TestIntArrayRegion(t *testing.T) {
	withEnv(t, func(env *Env) {
		arr := env.NewIntArray(5)
		if arr == nil {
			t.Fatal("NewIntArray returned nil")
		}
		length := env.GetArrayLength(&arr.Array)
		if length != 5 {
			t.Errorf("GetArrayLength = %d, want 5", length)
		}
		data := [5]int32{10, 20, 30, 40, 50}
		env.SetIntArrayRegion(arr, 0, 5, unsafe.Pointer(&data[0]))
		var buf [5]int32
		env.GetIntArrayRegion(arr, 0, 5, unsafe.Pointer(&buf[0]))
		if buf != data {
			t.Errorf("got %v, want %v", buf, data)
		}
		env.DeleteLocalRef(&arr.Object)
	})
}

func TestByteArrayRegion(t *testing.T) {
	withEnv(t, func(env *Env) {
		arr := env.NewByteArray(3)
		data := [3]int8{1, 2, 3}
		env.SetByteArrayRegion(arr, 0, 3, unsafe.Pointer(&data[0]))
		var buf [3]int8
		env.GetByteArrayRegion(arr, 0, 3, unsafe.Pointer(&buf[0]))
		if buf != data {
			t.Errorf("got %v, want %v", buf, data)
		}
		env.DeleteLocalRef(&arr.Object)
	})
}

func TestBooleanArrayRegion(t *testing.T) {
	withEnv(t, func(env *Env) {
		arr := env.NewBooleanArray(2)
		data := [2]uint8{1, 0}
		env.SetBooleanArrayRegion(arr, 0, 2, unsafe.Pointer(&data[0]))
		var buf [2]uint8
		env.GetBooleanArrayRegion(arr, 0, 2, unsafe.Pointer(&buf[0]))
		if buf != data {
			t.Errorf("got %v, want %v", buf, data)
		}
		env.DeleteLocalRef(&arr.Object)
	})
}

func TestCharArrayRegion(t *testing.T) {
	withEnv(t, func(env *Env) {
		arr := env.NewCharArray(2)
		data := [2]uint16{'A', 'B'}
		env.SetCharArrayRegion(arr, 0, 2, unsafe.Pointer(&data[0]))
		var buf [2]uint16
		env.GetCharArrayRegion(arr, 0, 2, unsafe.Pointer(&buf[0]))
		if buf != data {
			t.Errorf("got %v, want %v", buf, data)
		}
		env.DeleteLocalRef(&arr.Object)
	})
}

func TestShortArrayRegion(t *testing.T) {
	withEnv(t, func(env *Env) {
		arr := env.NewShortArray(2)
		data := [2]int16{100, 200}
		env.SetShortArrayRegion(arr, 0, 2, unsafe.Pointer(&data[0]))
		var buf [2]int16
		env.GetShortArrayRegion(arr, 0, 2, unsafe.Pointer(&buf[0]))
		if buf != data {
			t.Errorf("got %v, want %v", buf, data)
		}
		env.DeleteLocalRef(&arr.Object)
	})
}

func TestLongArrayRegion(t *testing.T) {
	withEnv(t, func(env *Env) {
		arr := env.NewLongArray(2)
		data := [2]int64{1<<40 + 1, 1<<40 + 2}
		env.SetLongArrayRegion(arr, 0, 2, unsafe.Pointer(&data[0]))
		var buf [2]int64
		env.GetLongArrayRegion(arr, 0, 2, unsafe.Pointer(&buf[0]))
		if buf != data {
			t.Errorf("got %v, want %v", buf, data)
		}
		env.DeleteLocalRef(&arr.Object)
	})
}

func TestFloatArrayRegion(t *testing.T) {
	withEnv(t, func(env *Env) {
		arr := env.NewFloatArray(2)
		data := [2]float32{1.5, 2.5}
		env.SetFloatArrayRegion(arr, 0, 2, unsafe.Pointer(&data[0]))
		var buf [2]float32
		env.GetFloatArrayRegion(arr, 0, 2, unsafe.Pointer(&buf[0]))
		if buf != data {
			t.Errorf("got %v, want %v", buf, data)
		}
		env.DeleteLocalRef(&arr.Object)
	})
}

func TestDoubleArrayRegion(t *testing.T) {
	withEnv(t, func(env *Env) {
		arr := env.NewDoubleArray(2)
		data := [2]float64{3.14, 2.72}
		env.SetDoubleArrayRegion(arr, 0, 2, unsafe.Pointer(&data[0]))
		var buf [2]float64
		env.GetDoubleArrayRegion(arr, 0, 2, unsafe.Pointer(&buf[0]))
		if buf != data {
			t.Errorf("got %v, want %v", buf, data)
		}
		env.DeleteLocalRef(&arr.Object)
	})
}

func TestGetIntArrayElements(t *testing.T) {
	withEnv(t, func(env *Env) {
		arr := env.NewIntArray(3)
		data := [3]int32{7, 8, 9}
		env.SetIntArrayRegion(arr, 0, 3, unsafe.Pointer(&data[0]))
		elems := env.GetIntArrayElements(arr, nil)
		if elems == nil {
			t.Fatal("GetIntArrayElements returned nil")
		}
		got := (*[3]int32)(elems)
		if *got != data {
			t.Errorf("got %v, want %v", got, data)
		}
		env.ReleaseIntArrayElements(arr, elems, 0)
		env.DeleteLocalRef(&arr.Object)
	})
}

func TestGetPrimitiveArrayCritical(t *testing.T) {
	withEnv(t, func(env *Env) {
		arr := env.NewByteArray(4)
		data := [4]int8{10, 20, 30, 40}
		env.SetByteArrayRegion(arr, 0, 4, unsafe.Pointer(&data[0]))
		ptr := env.GetPrimitiveArrayCritical(&arr.Array, nil)
		if ptr == nil {
			t.Fatal("GetPrimitiveArrayCritical returned nil")
		}
		got := (*[4]int8)(ptr)
		if *got != data {
			t.Errorf("got %v, want %v", got, data)
		}
		env.ReleasePrimitiveArrayCritical(&arr.Array, ptr, 0)
		env.DeleteLocalRef(&arr.Object)
	})
}

func TestObjectArray(t *testing.T) {
	withEnv(t, func(env *Env) {
		strCls, _ := env.FindClass("java/lang/String")
		arr, err := env.NewObjectArray(3, strCls, nil)
		if err != nil {
			t.Fatalf("NewObjectArray: %v", err)
		}
		s1, _ := env.NewStringUTF("a")
		s2, _ := env.NewStringUTF("b")
		_ = env.SetObjectArrayElement(arr, 0, &s1.Object)
		_ = env.SetObjectArrayElement(arr, 1, &s2.Object)
		got, err := env.GetObjectArrayElement(arr, 0)
		if err != nil {
			t.Fatalf("GetObjectArrayElement: %v", err)
		}
		if !env.IsSameObject(got, &s1.Object) {
			t.Error("element 0 should be same as s1")
		}
		env.DeleteLocalRef(got)
		env.DeleteLocalRef(&s2.Object)
		env.DeleteLocalRef(&s1.Object)
		env.DeleteLocalRef(&arr.Object)
		env.DeleteLocalRef(&strCls.Object)
	})
}

// --- Monitor ---

func TestMonitorEnterExit(t *testing.T) {
	withEnv(t, func(env *Env) {
		obj, _ := env.NewStringUTF("monitor")
		err := env.MonitorEnter(&obj.Object)
		if err != nil {
			t.Fatalf("MonitorEnter: %v", err)
		}
		err = env.MonitorExit(&obj.Object)
		if err != nil {
			t.Fatalf("MonitorExit: %v", err)
		}
		env.DeleteLocalRef(&obj.Object)
	})
}

// --- Direct byte buffer ---

func TestDirectByteBuffer(t *testing.T) {
	withEnv(t, func(env *Env) {
		data := make([]byte, 64)
		data[0] = 42
		buf := env.NewDirectByteBuffer(unsafe.Pointer(&data[0]), int64(len(data)))
		if buf == nil {
			t.Fatal("NewDirectByteBuffer returned nil")
		}
		addr := env.GetDirectBufferAddress(buf)
		if addr == nil {
			t.Fatal("GetDirectBufferAddress returned nil")
		}
		cap := env.GetDirectBufferCapacity(buf)
		if cap != 64 {
			t.Errorf("GetDirectBufferCapacity = %d, want 64", cap)
		}
		if *(*byte)(addr) != 42 {
			t.Error("buffer content mismatch")
		}
		env.DeleteLocalRef(buf)
	})
}

// --- Reflection ---

func TestFromToReflectedMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, _ := env.FindClass("java/lang/String")
		mid, _ := env.GetMethodID(cls, "length", "()I")
		reflected := env.ToReflectedMethod(cls, mid, false)
		if reflected == nil {
			t.Fatal("ToReflectedMethod returned nil")
		}
		mid2 := env.FromReflectedMethod(reflected)
		if mid2 != mid {
			t.Error("round-trip method ID mismatch")
		}
		env.DeleteLocalRef(reflected)
		env.DeleteLocalRef(&cls.Object)
	})
}

func TestFromToReflectedField(t *testing.T) {
	withEnv(t, func(env *Env) {
		cls, _ := env.FindClass("java/lang/Integer")
		fid, _ := env.GetStaticFieldID(cls, "MAX_VALUE", "I")
		reflected := env.ToReflectedField(cls, fid, true)
		if reflected == nil {
			t.Fatal("ToReflectedField returned nil")
		}
		fid2 := env.FromReflectedField(reflected)
		if fid2 != fid {
			t.Error("round-trip field ID mismatch")
		}
		env.DeleteLocalRef(reflected)
		env.DeleteLocalRef(&cls.Object)
	})
}

// --- Value constructors ---

func TestValueConstructors(t *testing.T) {
	_ = BooleanValue(1)
	_ = ByteValue(42)
	_ = CharValue('A')
	_ = ShortValue(100)
	_ = IntValue(999)
	_ = LongValue(1 << 40)
	_ = FloatValue(3.14)
	_ = DoubleValue(2.718)
	_ = ObjectValue(nil)
	v := IntValue(42)
	_ = v.Raw()
}

// --- Nonvirtual method call ---

func TestCallNonvirtualObjectMethod(t *testing.T) {
	withEnv(t, func(env *Env) {
		str, _ := env.NewStringUTF("test")
		objCls, _ := env.FindClass("java/lang/Object")
		mid, _ := env.GetMethodID(objCls, "toString", "()Ljava/lang/String;")
		result, err := env.CallNonvirtualObjectMethod(&str.Object, objCls, mid)
		if err != nil {
			t.Fatalf("CallNonvirtualObjectMethod: %v", err)
		}
		if result == nil {
			t.Fatal("expected non-nil result")
		}
		env.DeleteLocalRef(result)
		env.DeleteLocalRef(&objCls.Object)
		env.DeleteLocalRef(&str.Object)
	})
}

// --- VM thread operations ---

func TestVMDo(t *testing.T) {
	err := testVM.Do(func(env *Env) error {
		v := env.GetVersion()
		if v < int32(JNI_VERSION_1_8) {
			t.Errorf("version in Do = %d", v)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("VM.Do: %v", err)
	}
}

func TestVMPtrRoundTrip(t *testing.T) {
	ptr := testVM.Ptr()
	vm2 := VMFromPtr(ptr)
	if vm2.Ptr() != ptr {
		t.Error("VM pointer round-trip failed")
	}
}

func TestEnvPtrRoundTrip(t *testing.T) {
	withEnv(t, func(env *Env) {
		ptr := env.Ptr()
		env2 := EnvFromPtr(ptr)
		if env2.Ptr() != ptr {
			t.Error("Env pointer round-trip failed")
		}
	})
}

// Ensure unused import suppression.
var _ = capi.Object(0)
