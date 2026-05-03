//go:build android

// Command alarm_clock_app is a Material 3 alarm clock demo built on a real
// Material widget tree: a MaterialToolbar header, a RecyclerView of
// MaterialCardView rows (each with a label TextView, a MaterialSwitch and
// MaterialButton edit/delete buttons), and a FloatingActionButton for the
// "add alarm" action. The RecyclerView.Adapter is a Go-side implementation
// dispatched through the cycle-7 generated abstract-adapter shim.
//
// Persistence: alarms live in SharedPreferences, sorted by trigger date and
// scheduled via AlarmManager.setAlarmClock — the same surface used by the
// system Clock app. Editing opens the legacy app.TimePickerDialog because
// MaterialTimePicker requires a FragmentManager that NativeActivity does
// not expose.
package main

/*
#include <android/native_activity.h>
#include <android/log.h>
#include <stdlib.h>
extern void goOnResume(ANativeActivity*);
static void _onResume(ANativeActivity* a) { a->callbacks->onResume = goOnResume; }
static void _setCallbacks(ANativeActivity* a) { _onResume(a); }
static uintptr_t _getVM(ANativeActivity* a) { return (uintptr_t)a->vm; }
static uintptr_t _getClazz(ANativeActivity* a) { return (uintptr_t)a->clazz; }
static void _logcat(const char* tag, const char* msg) {
    __android_log_print(ANDROID_LOG_INFO, tag, "%s", msg);
}
*/
import "C"
import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/AndroidGoLab/jni"
	"github.com/AndroidGoLab/jni/app"
	"github.com/AndroidGoLab/jni/app/alarm"
	app_consts "github.com/AndroidGoLab/jni/app/consts"
	appcompat_widget "github.com/AndroidGoLab/jni/androidx/appcompat/widget"
	rvwidget "github.com/AndroidGoLab/jni/androidx/recyclerview/widget"
	rv_consts "github.com/AndroidGoLab/jni/androidx/recyclerview/widget/consts"
	"github.com/AndroidGoLab/jni/com/google/android/material/appbar"
	"github.com/AndroidGoLab/jni/com/google/android/material/button"
	"github.com/AndroidGoLab/jni/com/google/android/material/card"
	"github.com/AndroidGoLab/jni/com/google/android/material/floatingactionbutton"
	"github.com/AndroidGoLab/jni/com/google/android/material/materialswitch"
	"github.com/AndroidGoLab/jni/content/preferences"
	"github.com/AndroidGoLab/jni/view/display"
	"github.com/AndroidGoLab/jni/widget"
	widget_consts "github.com/AndroidGoLab/jni/widget/consts"
)

const (
	prefsName = "alarm_clock_app"
	prefsKey  = "alarms"

	logTag = "GoJNI"

	// Pixel values. We don't bother with density conversion: the layout is
	// generous enough that 24-32px padding looks sane on the typical phone DPI.
	rootPaddingPx = int32(24)
	cardMarginPx  = int32(16)
	cardPaddingPx = int32(24)
	textRowSp     = float32(20)
	cardRadius    = float32(28)
)

var (
	matchParent  = int32(widget_consts.MatchParent)
	wrapContent  = int32(widget_consts.WrapContent)
	orientationV = int32(widget_consts.OrientationVertical)
	orientationH = int32(widget_consts.OrientationHorizontal)
	rvVertical   = int32(rv_consts.Vertical)
)

// alarmEntry is a single persisted alarm: stable ID, human label, and
// absolute trigger time in Unix milliseconds. Enabled alarms are scheduled.
type alarmEntry struct {
	ID      int32  `json:"id"`
	Label   string `json:"label"`
	At      int64  `json:"at_unix_ms"`
	Enabled bool   `json:"enabled"`
}

// alarmsPayload is the on-disk envelope. Version is bumped on schema
// changes; legacy entries (a bare JSON array) are detected by Version == 0.
type alarmsPayload struct {
	Version int          `json:"version"`
	Entries []alarmEntry `json:"entries"`
}

const alarmsPayloadVersion = 1

// rowViews is the per-card widget tree captured at onCreateViewHolder time
// so onBindViewHolder can update it cheaply for the bound entry.
type rowViews struct {
	itemView *jni.Object         // the MaterialCardView (also holder.itemView)
	label    *widget.TextView    // time + label TextView
	sw       *materialswitch.MaterialSwitch
	editBtn  *button.MaterialButton
	delBtn   *button.MaterialButton
	swCleanup    func()
	editCleanup  func()
	delCleanup   func()
}

// State held across onResume invocations and Go callbacks.
var (
	stateMu sync.Mutex

	globalVM    *jni.VM
	activityRef *jni.Object

	startedOnce sync.Once

	// Mutable model.
	entries     []alarmEntry
	nextID      int32
	rootContext *app.Context

	// Adapter cleanup (from NewProxy).
	adapter        *jni.Object
	adapterCleanup func()

	// Per-holder widget refs, keyed by Go-side holderID. The card's
	// itemView.setTag() carries an Integer with this ID so onBindViewHolder
	// can find the cached widgets.
	holders     = map[int32]*rowViews{}
	nextHolderID int32

	// Scheduling cleanup hooks.
	cleanups []func()
)

func main() {}

//export ANativeActivity_onCreate
func ANativeActivity_onCreate(activity *C.ANativeActivity, savedState unsafe.Pointer, savedStateSize C.size_t) {
	_ = savedState
	_ = savedStateSize
	vm := jni.VMFromUintptr(uintptr(C._getVM(activity)))
	actObj := jni.ObjectFromUintptr(uintptr(C._getClazz(activity)))

	stateMu.Lock()
	globalVM = vm
	activityRef = actObj
	stateMu.Unlock()

	C._setCallbacks(activity)
}

//export goOnResume
func goOnResume(_ *C.ANativeActivity) {
	startedOnce.Do(func() {
		// onResume runs on the Android UI thread. Stay on it for the
		// initial setup so setContentView is happy.
		runSetup()
	})
}

// logf appends to a transient buffer and forwards each line to logcat.
func logf(buf *bytes.Buffer, format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	buf.WriteString(line)
	if !strings.HasSuffix(line, "\n") {
		buf.WriteByte('\n')
	}
	for _, l := range strings.Split(strings.TrimRight(line, "\n"), "\n") {
		if l == "" {
			continue
		}
		cTag := C.CString(logTag)
		cMsg := C.CString(l)
		C._logcat(cTag, cMsg)
		C.free(unsafe.Pointer(cTag))
		C.free(unsafe.Pointer(cMsg))
	}
}

// runSetup is the one-shot bootstrap: install class loader, build the
// widget tree, attach it to the activity, schedule alarms, persist.
func runSetup() {
	var buf bytes.Buffer
	if err := setupOnce(&buf); err != nil {
		logf(&buf, "ERROR: %v", err)
	}
	logf(&buf, "alarm_clock_app complete")
}

func setupOnce(buf *bytes.Buffer) error {
	vm := globalVM
	act := activityRef
	if vm == nil || act == nil {
		return fmt.Errorf("vm or activity ref unset")
	}

	if err := vm.Do(func(env *jni.Env) error {
		return installProxyClassLoader(env, act)
	}); err != nil {
		return fmt.Errorf("install proxy class loader: %w", err)
	}

	// NativeActivity owns an opaque GL surface that occludes any Java
	// widgets attached via setContentView. Switching the window pixel
	// format to TRANSLUCENT and releasing the takeSurface/takeInputQueue
	// callbacks lets the Java view tree paint into the window surface and
	// receive input events. Must run before setContentView.
	if err := makeWindowTranslucent(vm, act); err != nil {
		logf(buf, "make window translucent: %v", err)
	}

	ctx := &app.Context{VM: vm, Obj: act}
	rootContext = ctx
	spObj, err := ctx.GetSharedPreferences(prefsName, 0)
	if err != nil {
		return fmt.Errorf("getSharedPreferences: %w", err)
	}
	sp := preferences.SharedPreferences{VM: vm, Obj: spObj}

	loaded, seeded, err := loadOrSeedEntries(&sp, buf)
	if err != nil {
		return fmt.Errorf("load entries: %w", err)
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].At < loaded[j].At })
	entries = loaded
	nextID = maxID(entries) + 1
	if seeded {
		logf(buf, "(seeded fresh entries this run)")
	}
	logf(buf, "%d alarm(s) loaded", len(entries))

	root, err := buildWidgetTree(vm, act)
	if err != nil {
		return fmt.Errorf("buildWidgetTree: %w", err)
	}

	activity := &app.Activity{VM: vm, Obj: act}
	if err := activity.SetContentView1(root); err != nil {
		return fmt.Errorf("setContentView: %w", err)
	}
	logf(buf, "widget tree attached")

	stateMu.Lock()
	scheduleSnapshot := append([]alarmEntry(nil), entries...)
	storeSnapshot := append([]alarmEntry(nil), entries...)
	stateMu.Unlock()
	if err := scheduleEnabled(vm, ctx, scheduleSnapshot, buf); err != nil {
		logf(buf, "scheduleEnabled: %v", err)
	}
	if err := storeEntries(&sp, storeSnapshot); err != nil {
		return fmt.Errorf("store entries: %w", err)
	}
	return nil
}

// buildWidgetTree assembles the root LinearLayout containing the
// MaterialToolbar, the RecyclerView (with a Go adapter), and the
// FloatingActionButton. Returns the root view's underlying Object so the
// caller can hand it to setContentView.
func buildWidgetTree(vm *jni.VM, act *jni.Object) (*jni.Object, error) {
	root, err := widget.NewLinearLayout(vm, act)
	if err != nil {
		return nil, fmt.Errorf("new root LinearLayout: %w", err)
	}
	if err := root.SetOrientation(orientationV); err != nil {
		return nil, fmt.Errorf("root setOrientation: %w", err)
	}
	rootView := &display.View{VM: vm, Obj: root.Obj}
	if err := rootView.SetPadding(rootPaddingPx, rootPaddingPx, rootPaddingPx, rootPaddingPx); err != nil {
		return nil, fmt.Errorf("root setPadding: %w", err)
	}
	rootGroup := &display.ViewGroup{VM: vm, Obj: root.Obj}

	toolbar, err := appbar.NewMaterialToolbar(vm, act)
	if err != nil {
		return nil, fmt.Errorf("new MaterialToolbar: %w", err)
	}
	if err := (&appcompat_widget.Toolbar{VM: vm, Obj: toolbar.Obj}).SetTitle1_1("Alarms"); err != nil {
		return nil, fmt.Errorf("toolbar setTitle: %w", err)
	}
	if err := rootGroup.AddView1(toolbar.Obj); err != nil {
		return nil, fmt.Errorf("root addView toolbar: %w", err)
	}

	rv, err := rvwidget.NewRecyclerView(vm, act)
	if err != nil {
		return nil, fmt.Errorf("new RecyclerView: %w", err)
	}
	rvLP, err := newLinearLayoutLayoutParamsWeighted(vm, matchParent, 0, 1)
	if err != nil {
		return nil, fmt.Errorf("rv layoutparams: %w", err)
	}
	rvView := &display.View{VM: vm, Obj: rv.Obj}
	if err := rvView.SetLayoutParams(rvLP); err != nil {
		return nil, fmt.Errorf("rv setLayoutParams: %w", err)
	}
	lm, err := rvwidget.NewLinearLayoutManager(vm, act)
	if err != nil {
		return nil, fmt.Errorf("new LinearLayoutManager: %w", err)
	}
	if err := lm.SetOrientation(rvVertical); err != nil {
		return nil, fmt.Errorf("lm setOrientation: %w", err)
	}
	if err := rv.SetLayoutManager(lm.Obj); err != nil {
		return nil, fmt.Errorf("rv setLayoutManager: %w", err)
	}
	adapterObj, cleanup, err := newAlarmAdapter(vm, act)
	if err != nil {
		return nil, fmt.Errorf("new alarm Adapter: %w", err)
	}
	adapter = adapterObj
	adapterCleanup = cleanup
	if err := rv.SetAdapter(adapterObj); err != nil {
		return nil, fmt.Errorf("rv setAdapter: %w", err)
	}
	if err := rootGroup.AddView1(rv.Obj); err != nil {
		return nil, fmt.Errorf("root addView rv: %w", err)
	}

	fab, err := floatingactionbutton.NewFloatingActionButton(vm, act)
	if err != nil {
		return nil, fmt.Errorf("new FAB: %w", err)
	}
	// android.R.drawable.ic_input_add — a built-in "+" glyph; saves us
	// shipping a vector drawable for what is essentially a placeholder
	// icon. Resource ID is 0x01080003 in the public android.R.drawable
	// table for ic_input_add.
	if err := fab.SetImageResource(int32(0x01080003)); err != nil {
		return nil, fmt.Errorf("fab setImageResource: %w", err)
	}
	fabLP, err := newLinearLayoutLayoutParamsGravity(vm, wrapContent, wrapContent, gravityEnd)
	if err != nil {
		return nil, fmt.Errorf("fab layoutparams: %w", err)
	}
	if err := (&display.View{VM: vm, Obj: fab.Obj}).SetLayoutParams(fabLP); err != nil {
		return nil, fmt.Errorf("fab setLayoutParams: %w", err)
	}
	if err := rootGroup.AddView1(fab.Obj); err != nil {
		return nil, fmt.Errorf("root addView fab: %w", err)
	}
	if err := attachClickListener(vm, &display.View{VM: vm, Obj: fab.Obj}, onAddClicked); err != nil {
		return nil, fmt.Errorf("fab setOnClickListener: %w", err)
	}

	return root.Obj, nil
}

// installProxyClassLoader points the proxy machinery at the activity's
// ClassLoader so it can find GoInvocationHandler in our APK's classes.dex.
func installProxyClassLoader(env *jni.Env, activity *jni.Object) error {
	cls := env.GetObjectClass(activity)
	mid, err := env.GetMethodID(cls, "getClassLoader", "()Ljava/lang/ClassLoader;")
	if err != nil {
		return fmt.Errorf("get getClassLoader: %w", err)
	}
	cl, err := env.CallObjectMethod(activity, mid)
	if err != nil {
		return fmt.Errorf("getClassLoader: %w", err)
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
// callback (takeSurface) and InputQueue callback (takeInputQueue);
// neither is consumed by our code. We release both back so the view
// tree paints the widgets and the Java input dispatcher receives
// touches. setFormat(TRANSLUCENT) additionally drops the opaque
// RGB_565 format installed by NativeActivity.
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
		// PixelFormat.TRANSLUCENT = -3
		if err := env.CallVoidMethod(window, setFormatMid, jni.IntValue(-3)); err != nil {
			return fmt.Errorf("call setFormat(TRANSLUCENT): %w", err)
		}
		return nil
	})
}

// gravityEnd matches android.view.Gravity.END, used to drop the FAB on
// the right edge inside the vertical LinearLayout.
const gravityEnd = int32(0x00800005) // Gravity.END | Gravity.CENTER_VERTICAL → 0x00800005 (END=0x800005)

// newLinearLayoutLayoutParamsWeighted constructs
// android.widget.LinearLayout$LayoutParams(width, height, weight). The
// generator does not yet emit constructors for static-nested
// LayoutParams classes, so we resolve the constructor by raw JNI.
// Returns a global ref.
func newLinearLayoutLayoutParamsWeighted(
	vm *jni.VM,
	width, height int32,
	weight float32,
) (*jni.Object, error) {
	var out *jni.Object
	err := vm.Do(func(env *jni.Env) error {
		cls, err := env.FindClass("android/widget/LinearLayout$LayoutParams")
		if err != nil {
			return fmt.Errorf("find LinearLayout.LayoutParams: %w", err)
		}
		defer env.DeleteLocalRef(&cls.Object)
		mid, err := env.GetMethodID(cls, "<init>", "(IIF)V")
		if err != nil {
			return fmt.Errorf("LinearLayout.LayoutParams.<init>(IIF): %w", err)
		}
		local, err := env.NewObject(cls, mid,
			jni.IntValue(width), jni.IntValue(height), jni.FloatValue(weight))
		if err != nil {
			return fmt.Errorf("new LinearLayout.LayoutParams: %w", err)
		}
		out = env.NewGlobalRef(local)
		env.DeleteLocalRef(local)
		return nil
	})
	return out, err
}

// newLinearLayoutLayoutParamsGravity constructs
// android.widget.LinearLayout$LayoutParams(width, height) and sets
// .gravity = gravity. Same generator gap as newLinearLayoutLayoutParamsWeighted.
func newLinearLayoutLayoutParamsGravity(
	vm *jni.VM,
	width, height, gravity int32,
) (*jni.Object, error) {
	var out *jni.Object
	err := vm.Do(func(env *jni.Env) error {
		cls, err := env.FindClass("android/widget/LinearLayout$LayoutParams")
		if err != nil {
			return fmt.Errorf("find LinearLayout.LayoutParams: %w", err)
		}
		defer env.DeleteLocalRef(&cls.Object)
		mid, err := env.GetMethodID(cls, "<init>", "(II)V")
		if err != nil {
			return fmt.Errorf("LinearLayout.LayoutParams.<init>(II): %w", err)
		}
		local, err := env.NewObject(cls, mid,
			jni.IntValue(width), jni.IntValue(height))
		if err != nil {
			return fmt.Errorf("new LinearLayout.LayoutParams: %w", err)
		}
		fid, err := env.GetFieldID(cls, "gravity", "I")
		if err != nil {
			return fmt.Errorf("get gravity fieldID: %w", err)
		}
		env.SetIntField(local, fid, gravity)
		out = env.NewGlobalRef(local)
		env.DeleteLocalRef(local)
		return nil
	})
	return out, err
}

// boxInteger creates a fresh java.lang.Integer wrapping `n`. The
// abstract-adapter dispatch path expects boxed return values for primitive
// types, and setTag/getTag stores ints as Integer.
func boxInteger(env *jni.Env, n int32) (*jni.Object, error) {
	cls, err := env.FindClass("java/lang/Integer")
	if err != nil {
		return nil, fmt.Errorf("find Integer: %w", err)
	}
	defer env.DeleteLocalRef(&cls.Object)
	mid, err := env.GetStaticMethodID(cls, "valueOf", "(I)Ljava/lang/Integer;")
	if err != nil {
		return nil, fmt.Errorf("Integer.valueOf MID: %w", err)
	}
	obj, err := env.CallStaticObjectMethod(cls, mid, jni.IntValue(n))
	if err != nil {
		return nil, fmt.Errorf("Integer.valueOf: %w", err)
	}
	return obj, nil
}

// boxLong creates a fresh java.lang.Long wrapping `n`.
func boxLong(env *jni.Env, n int64) (*jni.Object, error) {
	cls, err := env.FindClass("java/lang/Long")
	if err != nil {
		return nil, fmt.Errorf("find Long: %w", err)
	}
	defer env.DeleteLocalRef(&cls.Object)
	mid, err := env.GetStaticMethodID(cls, "valueOf", "(J)Ljava/lang/Long;")
	if err != nil {
		return nil, fmt.Errorf("Long.valueOf MID: %w", err)
	}
	obj, err := env.CallStaticObjectMethod(cls, mid, jni.LongValue(n))
	if err != nil {
		return nil, fmt.Errorf("Long.valueOf: %w", err)
	}
	return obj, nil
}

// boxBoolean creates a fresh java.lang.Boolean wrapping `b`.
func boxBoolean(env *jni.Env, b bool) (*jni.Object, error) {
	cls, err := env.FindClass("java/lang/Boolean")
	if err != nil {
		return nil, fmt.Errorf("find Boolean: %w", err)
	}
	defer env.DeleteLocalRef(&cls.Object)
	mid, err := env.GetStaticMethodID(cls, "valueOf", "(Z)Ljava/lang/Boolean;")
	if err != nil {
		return nil, fmt.Errorf("Boolean.valueOf MID: %w", err)
	}
	var v uint8
	if b {
		v = 1
	}
	obj, err := env.CallStaticObjectMethod(cls, mid, jni.BooleanValue(v))
	if err != nil {
		return nil, fmt.Errorf("Boolean.valueOf: %w", err)
	}
	return obj, nil
}

// unboxInt reads java.lang.Integer.intValue() off a boxed argument.
func unboxInt(env *jni.Env, boxed *jni.Object) (int, error) {
	if boxed == nil || boxed.Ref() == 0 {
		return 0, fmt.Errorf("nil Integer")
	}
	cls := env.GetObjectClass(boxed)
	mid, err := env.GetMethodID(cls, "intValue", "()I")
	if err != nil {
		return 0, fmt.Errorf("intValue mid: %w", err)
	}
	v, err := env.CallIntMethod(boxed, mid)
	if err != nil {
		return 0, fmt.Errorf("intValue: %w", err)
	}
	return int(v), nil
}

// newAlarmAdapter creates a Go-backed RecyclerView.Adapter via NewProxy;
// proxy.go falls through to tryAbstractAdapter which instantiates
// center.dx.jni.generated.RecyclerView$AdapterAdapter. The dispatch
// handler implements getItemCount, onCreateViewHolder, onBindViewHolder.
func newAlarmAdapter(vm *jni.VM, act *jni.Object) (*jni.Object, func(), error) {
	var (
		proxyObj *jni.Object
		cleanup  func()
	)
	err := vm.Do(func(env *jni.Env) error {
		cls, err := env.FindClass("androidx/recyclerview/widget/RecyclerView$Adapter")
		if err != nil {
			return fmt.Errorf("find RecyclerView.Adapter: %w", err)
		}
		defer env.DeleteLocalRef(&cls.Object)
		p, c, err := env.NewProxy(
			[]*jni.Class{cls},
			func(callEnv *jni.Env, method string, args []*jni.Object) (*jni.Object, error) {
				return dispatchAdapter(callEnv, vm, act, method, args)
			},
		)
		if err != nil {
			return fmt.Errorf("NewProxy(RecyclerView.Adapter): %w", err)
		}
		proxyGlobal := env.NewGlobalRef(p)
		env.DeleteLocalRef(p)
		proxyObj = proxyGlobal
		cleanup = c
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return proxyObj, cleanup, nil
}

// dispatchAdapter routes adapter method calls from the Java shim to the
// matching Go-side implementation. Boxed returns are required for
// primitive-int methods (the shim unboxes via Integer.intValue()).
func dispatchAdapter(
	env *jni.Env,
	vm *jni.VM,
	act *jni.Object,
	method string,
	args []*jni.Object,
) (*jni.Object, error) {
	switch method {
	case "getItemCount":
		stateMu.Lock()
		n := int32(len(entries))
		stateMu.Unlock()
		return boxInteger(env, n)
	case "getItemViewType":
		// Single view type for every row.
		return boxInteger(env, 0)
	case "getItemId":
		// Adapter does not opt into stable IDs; return NO_ID (-1).
		return boxLong(env, -1)
	case "hasStableIds":
		return boxBoolean(env, false)
	case "onCreateViewHolder":
		// args[0] = parent ViewGroup; we don't actually need it because
		// MaterialCardView accepts the activity Context directly. args[1]
		// = viewType (boxed Integer), unused.
		holder, err := createRowHolder(env, vm, act)
		if err != nil {
			return nil, fmt.Errorf("createRowHolder: %w", err)
		}
		return holder, nil
	case "onBindViewHolder":
		// args[0] = ViewHolder, args[1] = position (boxed Integer).
		if len(args) < 2 {
			return nil, fmt.Errorf("onBindViewHolder: too few args")
		}
		pos, err := unboxInt(env, args[1])
		if err != nil {
			return nil, fmt.Errorf("unbox position: %w", err)
		}
		if err := bindRowHolder(env, vm, args[0], pos); err != nil {
			return nil, fmt.Errorf("bindRowHolder: %w", err)
		}
		return nil, nil
	default:
		return nil, nil
	}
}

// createRowHolder builds a fresh MaterialCardView with the row widgets,
// caches them in `holders`, tags the card with the holder ID, and wraps it
// in a RecyclerView$ViewHolderAdapter (the cycle-7 generated concrete
// subclass of RecyclerView.ViewHolder that takes a single View arg).
func createRowHolder(env *jni.Env, vm *jni.VM, act *jni.Object) (*jni.Object, error) {
	cardObj, refs, err := buildCardRow(vm, act)
	if err != nil {
		return nil, fmt.Errorf("buildCardRow: %w", err)
	}

	stateMu.Lock()
	holderID := nextHolderID
	nextHolderID++
	holders[holderID] = refs
	stateMu.Unlock()

	tagBox, err := boxInteger(env, holderID)
	if err != nil {
		return nil, fmt.Errorf("box holderID: %w", err)
	}
	defer env.DeleteLocalRef(tagBox)
	tagGlobal := env.NewGlobalRef(tagBox)
	if err := (&display.View{VM: vm, Obj: cardObj}).SetTag1_1(tagGlobal); err != nil {
		return nil, fmt.Errorf("setTag: %w", err)
	}
	defer env.DeleteGlobalRef(tagGlobal)

	// Instantiate center.dx.jni.generated.RecyclerView$ViewHolderAdapter,
	// which is the generator-emitted concrete RecyclerView.ViewHolder
	// subclass: ctor takes only (View itemView).
	cls, err := env.FindClass("center/dx/jni/generated/RecyclerView$ViewHolderAdapter")
	if err != nil {
		return nil, fmt.Errorf("find ViewHolderAdapter: %w", err)
	}
	defer env.DeleteLocalRef(&cls.Object)
	mid, err := env.GetMethodID(cls, "<init>", "(Landroid/view/View;)V")
	if err != nil {
		return nil, fmt.Errorf("ViewHolderAdapter ctor mid: %w", err)
	}
	local, err := env.NewObject(cls, mid, jni.ObjectValue(cardObj))
	if err != nil {
		return nil, fmt.Errorf("new ViewHolderAdapter: %w", err)
	}
	return local, nil
}

// bindRowHolder reads the cached widget refs from the holder's itemView
// tag and updates label, switch and click handlers for the entry at
// `position`.
func bindRowHolder(env *jni.Env, vm *jni.VM, holder *jni.Object, position int) error {
	if holder == nil || holder.Ref() == 0 {
		return fmt.Errorf("nil holder")
	}
	holderCls := env.GetObjectClass(holder)
	itemFid, err := env.GetFieldID(holderCls, "itemView", "Landroid/view/View;")
	if err != nil {
		return fmt.Errorf("itemView fieldID: %w", err)
	}
	itemView := env.GetObjectField(holder, itemFid)
	if itemView == nil || itemView.Ref() == 0 {
		return fmt.Errorf("itemView field: nil")
	}
	tagObj, err := (&display.View{VM: vm, Obj: itemView}).GetTag0()
	if err != nil {
		return fmt.Errorf("getTag: %w", err)
	}
	holderID, err := unboxInt(env, tagObj)
	if err != nil {
		return fmt.Errorf("unbox holderID: %w", err)
	}
	stateMu.Lock()
	refs, ok := holders[int32(holderID)]
	if !ok || position < 0 || position >= len(entries) {
		stateMu.Unlock()
		return fmt.Errorf("holder/position out of sync (holderID=%d position=%d)", holderID, position)
	}
	entry := entries[position]
	stateMu.Unlock()

	if err := refs.label.SetText1_3(fmt.Sprintf("%s  %s", formatTime(entry.At), entry.Label)); err != nil {
		return fmt.Errorf("label setText: %w", err)
	}
	cb := &widget.CompoundButton{VM: vm, Obj: refs.sw.Obj}
	if err := cb.SetChecked(entry.Enabled); err != nil {
		return fmt.Errorf("switch setChecked: %w", err)
	}
	// Replace listeners; previous listeners are torn down via cleanups.
	if refs.swCleanup != nil {
		refs.swCleanup()
	}
	swClean, err := attachCheckedChangeListenerByID(vm, refs.sw, entry.ID)
	if err != nil {
		return fmt.Errorf("attach switch listener: %w", err)
	}
	refs.swCleanup = swClean

	if refs.editCleanup != nil {
		refs.editCleanup()
	}
	editClean, err := attachClickListenerByID(vm, &display.View{VM: vm, Obj: refs.editBtn.Obj}, entry.ID, onEditClickedByID)
	if err != nil {
		return fmt.Errorf("attach edit listener: %w", err)
	}
	refs.editCleanup = editClean

	if refs.delCleanup != nil {
		refs.delCleanup()
	}
	delClean, err := attachClickListenerByID(vm, &display.View{VM: vm, Obj: refs.delBtn.Obj}, entry.ID, onDeleteClickedByID)
	if err != nil {
		return fmt.Errorf("attach delete listener: %w", err)
	}
	refs.delCleanup = delClean
	return nil
}

// buildCardRow constructs a MaterialCardView containing a horizontal
// LinearLayout with a TextView + MaterialSwitch + two MaterialButtons
// (Edit / Delete). It returns the card's underlying Object (the
// itemView) plus a rowViews struct caching the inner widget refs.
func buildCardRow(vm *jni.VM, act *jni.Object) (*jni.Object, *rowViews, error) {
	mc, err := card.NewMaterialCardView(vm, act)
	if err != nil {
		return nil, nil, fmt.Errorf("new MaterialCardView: %w", err)
	}
	if err := mc.SetRadius(cardRadius); err != nil {
		return nil, nil, fmt.Errorf("card setRadius: %w", err)
	}
	cardLP, err := newLinearLayoutLayoutParamsMargins(vm, matchParent, wrapContent, cardMarginPx)
	if err != nil {
		return nil, nil, fmt.Errorf("card layoutparams: %w", err)
	}
	cardView := &display.View{VM: vm, Obj: mc.Obj}
	if err := cardView.SetLayoutParams(cardLP); err != nil {
		return nil, nil, fmt.Errorf("card setLayoutParams: %w", err)
	}

	row, err := widget.NewLinearLayout(vm, act)
	if err != nil {
		return nil, nil, fmt.Errorf("new row LinearLayout: %w", err)
	}
	if err := row.SetOrientation(orientationH); err != nil {
		return nil, nil, fmt.Errorf("row setOrientation: %w", err)
	}
	rowView := &display.View{VM: vm, Obj: row.Obj}
	if err := rowView.SetPadding(cardPaddingPx, cardPaddingPx, cardPaddingPx, cardPaddingPx); err != nil {
		return nil, nil, fmt.Errorf("row setPadding: %w", err)
	}
	rowGroup := &display.ViewGroup{VM: vm, Obj: row.Obj}
	cardGroup := &display.ViewGroup{VM: vm, Obj: mc.Obj}
	if err := cardGroup.AddView1(row.Obj); err != nil {
		return nil, nil, fmt.Errorf("card addView row: %w", err)
	}

	label, err := widget.NewTextView(vm, act)
	if err != nil {
		return nil, nil, fmt.Errorf("new label TextView: %w", err)
	}
	if err := label.SetTextSize1(textRowSp); err != nil {
		return nil, nil, fmt.Errorf("label setTextSize: %w", err)
	}
	labelLP, err := newLinearLayoutLayoutParamsWeighted(vm, 0, wrapContent, 1)
	if err != nil {
		return nil, nil, fmt.Errorf("label LP: %w", err)
	}
	if err := (&display.View{VM: vm, Obj: label.Obj}).SetLayoutParams(labelLP); err != nil {
		return nil, nil, fmt.Errorf("label setLayoutParams: %w", err)
	}
	if err := rowGroup.AddView1(label.Obj); err != nil {
		return nil, nil, fmt.Errorf("row addView label: %w", err)
	}

	sw, err := materialswitch.NewMaterialSwitch(vm, act)
	if err != nil {
		return nil, nil, fmt.Errorf("new MaterialSwitch: %w", err)
	}
	if err := rowGroup.AddView1(sw.Obj); err != nil {
		return nil, nil, fmt.Errorf("row addView switch: %w", err)
	}

	editBtn, err := button.NewMaterialButton(vm, act)
	if err != nil {
		return nil, nil, fmt.Errorf("new edit MaterialButton: %w", err)
	}
	if err := (&widget.TextView{VM: vm, Obj: editBtn.Obj}).SetText1_3("Edit"); err != nil {
		return nil, nil, fmt.Errorf("edit setText: %w", err)
	}
	if err := rowGroup.AddView1(editBtn.Obj); err != nil {
		return nil, nil, fmt.Errorf("row addView edit: %w", err)
	}

	delBtn, err := button.NewMaterialButton(vm, act)
	if err != nil {
		return nil, nil, fmt.Errorf("new delete MaterialButton: %w", err)
	}
	if err := (&widget.TextView{VM: vm, Obj: delBtn.Obj}).SetText1_3("Delete"); err != nil {
		return nil, nil, fmt.Errorf("delete setText: %w", err)
	}
	if err := rowGroup.AddView1(delBtn.Obj); err != nil {
		return nil, nil, fmt.Errorf("row addView delete: %w", err)
	}

	return mc.Obj, &rowViews{
		itemView: mc.Obj,
		label:    label,
		sw:       sw,
		editBtn:  editBtn,
		delBtn:   delBtn,
	}, nil
}

// newLinearLayoutLayoutParamsMargins constructs (width, height) and sets
// margins to `margin` on all four sides. Same generator gap as
// newLinearLayoutLayoutParamsWeighted.
func newLinearLayoutLayoutParamsMargins(
	vm *jni.VM,
	width, height, margin int32,
) (*jni.Object, error) {
	var out *jni.Object
	err := vm.Do(func(env *jni.Env) error {
		cls, err := env.FindClass("android/widget/LinearLayout$LayoutParams")
		if err != nil {
			return fmt.Errorf("find LinearLayout.LayoutParams: %w", err)
		}
		defer env.DeleteLocalRef(&cls.Object)
		mid, err := env.GetMethodID(cls, "<init>", "(II)V")
		if err != nil {
			return fmt.Errorf("LinearLayout.LayoutParams.<init>(II): %w", err)
		}
		local, err := env.NewObject(cls, mid,
			jni.IntValue(width), jni.IntValue(height))
		if err != nil {
			return fmt.Errorf("new LinearLayout.LayoutParams: %w", err)
		}
		setMid, err := env.GetMethodID(cls, "setMargins", "(IIII)V")
		if err != nil {
			return fmt.Errorf("setMargins MID: %w", err)
		}
		if err := env.CallVoidMethod(local, setMid,
			jni.IntValue(margin), jni.IntValue(margin), jni.IntValue(margin), jni.IntValue(margin)); err != nil {
			return fmt.Errorf("setMargins: %w", err)
		}
		out = env.NewGlobalRef(local)
		env.DeleteLocalRef(local)
		return nil
	})
	return out, err
}

// attachClickListener creates a Java Proxy of View$OnClickListener whose
// onClick() dispatches to the supplied Go callback, then assigns it via
// View.setOnClickListener.
func attachClickListener(vm *jni.VM, v *display.View, fn func()) error {
	return vm.Do(func(env *jni.Env) error {
		listenerCls, err := env.FindClass("android/view/View$OnClickListener")
		if err != nil {
			return fmt.Errorf("find OnClickListener: %w", err)
		}
		proxy, cleanup, err := env.NewProxy(
			[]*jni.Class{listenerCls},
			func(_ *jni.Env, method string, _ []*jni.Object) (*jni.Object, error) {
				if method == "onClick" {
					fn()
				}
				return nil, nil
			},
		)
		if err != nil {
			return fmt.Errorf("NewProxy(OnClickListener): %w", err)
		}
		listenerGlobal := env.NewGlobalRef(proxy)
		gref := listenerGlobal // capture for closure
		stateMu.Lock()
		cleanups = append(cleanups, cleanup, func() {
			_ = vm.Do(func(env *jni.Env) error {
				env.DeleteGlobalRef(gref)
				return nil
			})
		})
		stateMu.Unlock()
		return v.SetOnClickListener(listenerGlobal)
	})
}

// attachClickListenerByID is the per-row variant: it captures the entry's
// ID rather than its volatile position, returns its own cleanup func, and
// is reattached on every onBindViewHolder so the dispatched ID stays in
// sync with the (possibly resorted) entries slice.
func attachClickListenerByID(
	vm *jni.VM,
	v *display.View,
	entryID int32,
	fn func(id int32),
) (func(), error) {
	var (
		out     func()
		retErr  error
	)
	retErr = vm.Do(func(env *jni.Env) error {
		listenerCls, err := env.FindClass("android/view/View$OnClickListener")
		if err != nil {
			return fmt.Errorf("find OnClickListener: %w", err)
		}
		proxy, cleanup, err := env.NewProxy(
			[]*jni.Class{listenerCls},
			func(_ *jni.Env, method string, _ []*jni.Object) (*jni.Object, error) {
				if method == "onClick" {
					fn(entryID)
				}
				return nil, nil
			},
		)
		if err != nil {
			return fmt.Errorf("NewProxy(OnClickListener): %w", err)
		}
		listenerGlobal := env.NewGlobalRef(proxy)
		gref := listenerGlobal
		out = func() {
			cleanup()
			_ = vm.Do(func(env *jni.Env) error {
				env.DeleteGlobalRef(gref)
				return nil
			})
		}
		return v.SetOnClickListener(listenerGlobal)
	})
	if retErr != nil {
		return nil, retErr
	}
	return out, nil
}

// attachCheckedChangeListenerByID wires
// CompoundButton.OnCheckedChangeListener to a Go handler that dispatches
// by the entry's stable ID. Uses the bound CompoundButton.SetOnCheckedChangeListener.
func attachCheckedChangeListenerByID(
	vm *jni.VM,
	sw *materialswitch.MaterialSwitch,
	entryID int32,
) (func(), error) {
	var (
		out    func()
		retErr error
	)
	retErr = vm.Do(func(env *jni.Env) error {
		listenerCls, err := env.FindClass("android/widget/CompoundButton$OnCheckedChangeListener")
		if err != nil {
			return fmt.Errorf("find OnCheckedChangeListener: %w", err)
		}
		proxy, cleanup, err := env.NewProxy(
			[]*jni.Class{listenerCls},
			func(_ *jni.Env, method string, args []*jni.Object) (*jni.Object, error) {
				if method == "onCheckedChanged" && len(args) == 2 {
					cb := &widget.CompoundButton{VM: vm, Obj: sw.Obj}
					checked, _ := cb.IsChecked()
					onSwitchToggledByID(entryID, checked)
				}
				return nil, nil
			},
		)
		if err != nil {
			return fmt.Errorf("NewProxy(OnCheckedChangeListener): %w", err)
		}
		listenerGlobal := env.NewGlobalRef(proxy)
		gref := listenerGlobal
		out = func() {
			cleanup()
			_ = vm.Do(func(env *jni.Env) error {
				env.DeleteGlobalRef(gref)
				return nil
			})
		}
		cb := &widget.CompoundButton{VM: vm, Obj: sw.Obj}
		return cb.SetOnCheckedChangeListener(listenerGlobal)
	})
	if retErr != nil {
		return nil, retErr
	}
	return out, nil
}

// onAddClicked opens a TimePickerDialog initialised at "now" and, on
// confirm, appends a new entry, sorts, persists, reschedules, redraws.
func onAddClicked() {
	now := time.Now()
	openTimePicker(now.Hour(), now.Minute(), func(h, m int) {
		stateMu.Lock()
		id := nextID
		nextID++
		entries = append(entries, alarmEntry{
			ID:      id,
			Label:   fmt.Sprintf("Alarm %d", id),
			At:      nextOccurrence(h, m).UnixMilli(),
			Enabled: true,
		})
		stateMu.Unlock()
		applyMutation()
	})
}

// onEditClickedByID opens a TimePickerDialog seeded with the row's
// current time. On confirm, replaces the time and persists.
func onEditClickedByID(id int32) {
	stateMu.Lock()
	idx := indexByID(entries, id)
	if idx < 0 {
		stateMu.Unlock()
		return
	}
	t := time.UnixMilli(entries[idx].At)
	stateMu.Unlock()
	openTimePicker(t.Hour(), t.Minute(), func(h, m int) {
		stateMu.Lock()
		idx := indexByID(entries, id)
		if idx >= 0 {
			entries[idx].At = nextOccurrence(h, m).UnixMilli()
		}
		stateMu.Unlock()
		applyMutation()
	})
}

// onDeleteClickedByID removes the entry, persists, redraws.
func onDeleteClickedByID(id int32) {
	stateMu.Lock()
	idx := indexByID(entries, id)
	if idx < 0 {
		stateMu.Unlock()
		return
	}
	entries = append(entries[:idx], entries[idx+1:]...)
	stateMu.Unlock()
	applyMutation()
}

// onSwitchToggledByID flips the entry's Enabled flag and reschedules.
func onSwitchToggledByID(id int32, checked bool) {
	stateMu.Lock()
	idx := indexByID(entries, id)
	if idx < 0 {
		stateMu.Unlock()
		return
	}
	if entries[idx].Enabled == checked {
		stateMu.Unlock()
		return
	}
	entries[idx].Enabled = checked
	stateMu.Unlock()
	applyMutation()
}

// indexByID returns the position of the entry with the given stable ID,
// or -1 if not found. Caller must hold stateMu.
func indexByID(es []alarmEntry, id int32) int {
	for i, e := range es {
		if e.ID == id {
			return i
		}
	}
	return -1
}

// applyMutation re-sorts, persists, reschedules, and tells the adapter
// data set changed. Called from the UI thread (proxy callbacks run on it).
func applyMutation() {
	stateMu.Lock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].At < entries[j].At })
	snapshot := append([]alarmEntry(nil), entries...)
	stateMu.Unlock()

	var buf bytes.Buffer
	logf(&buf, "applyMutation: %d entries", len(snapshot))

	vm := globalVM
	act := activityRef
	ctx := &app.Context{VM: vm, Obj: act}
	spObj, err := ctx.GetSharedPreferences(prefsName, 0)
	if err == nil {
		sp := preferences.SharedPreferences{VM: vm, Obj: spObj}
		_ = storeEntries(&sp, snapshot)
	}
	if err := scheduleEnabled(vm, ctx, snapshot, &buf); err != nil {
		logf(&buf, "scheduleEnabled: %v", err)
	}
	notifyAdapterChanged(vm)
}

// notifyAdapterChanged re-runs the RecyclerView.Adapter pipeline so the
// rows pick up the post-sort entries. Uses RecyclerViewAdapter.NotifyDataSetChanged.
func notifyAdapterChanged(vm *jni.VM) {
	if adapter == nil {
		return
	}
	a := &rvwidget.RecyclerViewAdapter{VM: vm, Obj: adapter}
	if err := a.NotifyDataSetChanged(); err != nil {
		var b bytes.Buffer
		logf(&b, "notifyDataSetChanged: %v", err)
	}
}

// openTimePicker creates a new TimePickerDialog with a Go-side
// OnTimeSetListener proxy, configures it for 24h time, and shows it.
//
// MaterialTimePicker requires a FragmentManager that NativeActivity does
// not expose, so we stay on the legacy AlertDialog-based picker.
func openTimePicker(initHour, initMinute int, onSet func(hour, minute int)) {
	vm := globalVM
	act := activityRef

	var dlg *app.TimePickerDialog
	err := vm.Do(func(env *jni.Env) error {
		listenerCls, err := env.FindClass("android/app/TimePickerDialog$OnTimeSetListener")
		if err != nil {
			return fmt.Errorf("find OnTimeSetListener: %w", err)
		}
		proxy, cleanup, err := env.NewProxy(
			[]*jni.Class{listenerCls},
			func(env *jni.Env, method string, args []*jni.Object) (*jni.Object, error) {
				if method == "onTimeSet" && len(args) >= 3 {
					h, _ := unboxInt(env, args[1])
					m, _ := unboxInt(env, args[2])
					onSet(h, m)
				}
				return nil, nil
			},
		)
		if err != nil {
			return fmt.Errorf("NewProxy(OnTimeSetListener): %w", err)
		}
		listenerGlobal := env.NewGlobalRef(proxy)
		stateMu.Lock()
		cleanups = append(cleanups, cleanup)
		stateMu.Unlock()

		td, err := app.NewTimePickerDialog(vm, act, listenerGlobal,
			int32(initHour), int32(initMinute), true)
		if err != nil {
			return fmt.Errorf("NewTimePickerDialog: %w", err)
		}
		dlg = td
		return nil
	})
	if err != nil {
		var b bytes.Buffer
		logf(&b, "openTimePicker: %v", err)
		return
	}
	if err := dlg.Show(); err != nil {
		var b bytes.Buffer
		logf(&b, "TimePickerDialog.Show: %v", err)
	}
}

// nextOccurrence returns the next local time at hh:mm (today if still in
// the future, otherwise tomorrow).
func nextOccurrence(hour, minute int) time.Time {
	now := time.Now()
	candidate := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !candidate.After(now) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate
}

// scheduleEnabled (re)schedules every enabled alarm via setAlarmClock.
// AlarmManager replaces an existing alarm with the same PendingIntent
// request code — we use entry.ID — so re-running this is idempotent.
func scheduleEnabled(vm *jni.VM, ctx *app.Context, snapshot []alarmEntry, buf *bytes.Buffer) error {
	mgr, err := alarm.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("alarm.NewManager: %w", err)
	}
	defer mgr.Close()

	canSchedule, canErr := mgr.CanScheduleExactAlarms()
	switch {
	case canErr != nil:
		logf(buf, "CanScheduleExactAlarms: %v", canErr)
	case !canSchedule:
		logf(buf, "WARN: exact alarms not permitted; setAlarmClock will fail per-entry")
	}

	naClassObj, err := findNativeActivityClass(vm)
	if err != nil {
		return fmt.Errorf("find NativeActivity class: %w", err)
	}
	defer releaseGlobalRef(vm, naClassObj)

	for _, e := range snapshot {
		if !e.Enabled {
			continue
		}
		if err := scheduleOne(vm, mgr, ctx, naClassObj, e); err != nil {
			logf(buf, "  schedule [%d] %q: %v", e.ID, e.Label, err)
			continue
		}
		logf(buf, "  scheduled [%d] %q at %s", e.ID, e.Label, formatTime(e.At))
	}
	return nil
}

func scheduleOne(
	vm *jni.VM,
	mgr *alarm.Manager,
	ctx *app.Context,
	naClassObj *jni.Object,
	entry alarmEntry,
) error {
	intent, err := app.NewIntent(vm, ctx.Obj, naClassObj)
	if err != nil {
		return fmt.Errorf("NewIntent: %w", err)
	}
	defer releaseGlobalRef(vm, intent.Obj)

	flags := int32(app_consts.FlagImmutable | app_consts.FlagUpdateCurrent)
	piHelper := app.PendingIntent{VM: vm}
	piObj, err := piHelper.GetActivity4(ctx.Obj, entry.ID, intent.Obj, flags)
	if err != nil {
		return fmt.Errorf("PendingIntent.getActivity: %w", err)
	}
	if piObj == nil || piObj.Ref() == 0 {
		return fmt.Errorf("PendingIntent.getActivity returned null")
	}
	defer releaseGlobalRef(vm, piObj)

	infoObj, err := newAlarmClockInfo(vm, entry.At, piObj)
	if err != nil {
		return fmt.Errorf("AlarmClockInfo: %w", err)
	}
	defer releaseGlobalRef(vm, infoObj)

	if err := mgr.SetAlarmClock(infoObj, piObj); err != nil {
		return fmt.Errorf("setAlarmClock: %w", err)
	}
	return nil
}

// findNativeActivityClass resolves android.app.NativeActivity once so it
// can be passed to NewIntent as the Class target.
func findNativeActivityClass(vm *jni.VM) (*jni.Object, error) {
	var classObj *jni.Object
	err := vm.Do(func(env *jni.Env) error {
		cls, err := env.FindClass("android/app/NativeActivity")
		if err != nil {
			return fmt.Errorf("FindClass: %w", err)
		}
		classObj = env.NewGlobalRef(&cls.Object)
		env.DeleteLocalRef(&cls.Object)
		if classObj == nil {
			return fmt.Errorf("NewGlobalRef returned nil")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return classObj, nil
}

// newAlarmClockInfo constructs android.app.AlarmManager$AlarmClockInfo via
// raw JNI. The javagen pipeline does not currently emit constructors for
// static-nested classes (alarm.ManagerAlarmClockInfo has only accessors).
func newAlarmClockInfo(
	vm *jni.VM,
	triggerMs int64,
	showIntent *jni.Object,
) (*jni.Object, error) {
	var infoObj *jni.Object
	err := vm.Do(func(env *jni.Env) error {
		cls, err := env.FindClass("android/app/AlarmManager$AlarmClockInfo")
		if err != nil {
			return fmt.Errorf("FindClass: %w", err)
		}
		defer env.DeleteLocalRef(&cls.Object)
		mid, err := env.GetMethodID(cls, "<init>", "(JLandroid/app/PendingIntent;)V")
		if err != nil {
			return fmt.Errorf("GetMethodID: %w", err)
		}
		local, err := env.NewObject(cls, mid,
			jni.LongValue(triggerMs), jni.ObjectValue(showIntent))
		if err != nil {
			return fmt.Errorf("NewObject: %w", err)
		}
		if local == nil {
			return fmt.Errorf("NewObject returned nil")
		}
		infoObj = env.NewGlobalRef(local)
		env.DeleteLocalRef(local)
		if infoObj == nil {
			return fmt.Errorf("NewGlobalRef returned nil")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return infoObj, nil
}

// loadOrSeedEntries reads the JSON list from SharedPreferences. On empty
// storage or a parse failure it seeds entries in unsorted order, persists
// once, and returns it.
func loadOrSeedEntries(
	sp *preferences.SharedPreferences,
	buf *bytes.Buffer,
) ([]alarmEntry, bool, error) {
	raw, err := sp.GetString(prefsKey, "")
	if err != nil {
		return nil, false, fmt.Errorf("getString: %w", err)
	}
	if raw != "" {
		var payload alarmsPayload
		if jsonErr := json.Unmarshal([]byte(raw), &payload); jsonErr == nil && payload.Version >= 1 && len(payload.Entries) > 0 {
			return payload.Entries, false, nil
		}
		var legacy []alarmEntry
		if jsonErr := json.Unmarshal([]byte(raw), &legacy); jsonErr == nil && len(legacy) > 0 {
			return legacy, false, nil
		}
		logf(buf, "(stored payload unparsable, reseeding)")
	}

	now := time.Now()
	seed := []alarmEntry{
		{ID: 1, Label: "Wake up", At: now.Add(90 * time.Second).UnixMilli(), Enabled: true},
		{ID: 2, Label: "Stand-up", At: now.Add(30 * time.Second).UnixMilli(), Enabled: true},
		{ID: 3, Label: "Coffee break", At: now.Add(180 * time.Second).UnixMilli(), Enabled: true},
		{ID: 4, Label: "Lunch", At: now.Add(10 * time.Second).UnixMilli(), Enabled: true},
		{ID: 5, Label: "Tea", At: now.Add(120 * time.Second).UnixMilli(), Enabled: true},
	}
	if err := storeEntries(sp, seed); err != nil {
		return nil, false, fmt.Errorf("seed store: %w", err)
	}
	return seed, true, nil
}

func storeEntries(sp *preferences.SharedPreferences, entries []alarmEntry) error {
	payload, err := json.Marshal(alarmsPayload{
		Version: alarmsPayloadVersion,
		Entries: entries,
	})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	editorObj, err := sp.Edit()
	if err != nil {
		return fmt.Errorf("edit: %w", err)
	}
	editor := preferences.SharedPreferencesEditor{VM: sp.VM, Obj: editorObj}
	if _, err := editor.PutString(prefsKey, string(payload)); err != nil {
		return fmt.Errorf("putString: %w", err)
	}
	if _, err := editor.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func releaseGlobalRef(vm *jni.VM, ref *jni.Object) {
	if ref == nil || ref.Ref() == 0 {
		return
	}
	_ = vm.Do(func(env *jni.Env) error {
		env.DeleteGlobalRef(ref)
		return nil
	})
}

func formatTime(ms int64) string {
	return time.UnixMilli(ms).Local().Format("15:04:05")
}

func maxID(es []alarmEntry) int32 {
	var m int32
	for _, e := range es {
		if e.ID > m {
			m = e.ID
		}
	}
	return m
}
