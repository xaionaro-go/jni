//go:build android

// Command alarm_clock_app is a Material 3 alarm clock app rendered as a real
// Material 3 widget tree. The list of alarms is persisted in
// SharedPreferences, sorted by trigger date, and each entry is scheduled as
// an exact alarm clock via AlarmManager.setAlarmClock — the same surface
// used by the system Clock app.
//
// The whole UI is constructed from Go via the existing JNI bindings: a
// vertical LinearLayout root with a title TextView, a ScrollView wrapping
// per-alarm rows (time + label + Switch + Edit + Delete), and a "+ Add
// alarm" Button at the bottom. Edit and Add open a Material 3
// TimePickerDialog whose OnTimeSetListener is a Go closure registered via
// env.NewProxy.
package main

/*
#include <android/native_activity.h>
#include <android/log.h>
#include <stdlib.h>
extern void goOnResume(ANativeActivity*);
static void _onResume(ANativeActivity* a) { goOnResume(a); }
static void _setCallbacks(ANativeActivity* a) { a->callbacks->onResume = _onResume; }
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
	"github.com/AndroidGoLab/jni/content/preferences"
	"github.com/AndroidGoLab/jni/view/display"
	"github.com/AndroidGoLab/jni/widget"
)

const (
	prefsName = "alarm_clock_app"
	prefsKey  = "alarms"

	logTag = "GoJNI"

	// LinearLayout.LayoutParams sentinels.
	matchParent  = int32(-1)
	wrapContent  = int32(-2)
	orientationV = int32(1) // LinearLayout.VERTICAL
	orientationH = int32(0) // LinearLayout.HORIZONTAL

	// Pixel values. We don't bother with density conversion: the layout is
	// generous enough that 24px padding looks sane on the typical phone DPI.
	rootPaddingPx = int32(48)
	rowPaddingPx  = int32(16)
	textTitleSp   = float32(28)
	textRowSp     = float32(20)
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

// State held across onResume invocations and Go callbacks.
var (
	stateMu sync.Mutex

	globalVM    *jni.VM
	activityRef *jni.Object

	startedOnce sync.Once

	// Mutable model. Mutations happen on the UI thread (inside the
	// proxy invocation handlers, dispatched by the Java framework).
	entries     []alarmEntry
	nextID      int32
	listLayout  *widget.LinearLayout // the inner row container; cleared and rebuilt on mutation
	rootContext *app.Context         // for alarm.NewManager (long-lived global ref)

	// Cleanup hooks for proxy handlers; deferred shutdown is best-effort.
	cleanups []func()
)

func main() {}

//export ANativeActivity_onCreate
func ANativeActivity_onCreate(activity *C.ANativeActivity, savedState unsafe.Pointer, savedStateSize C.size_t) {
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
		// initial setup so setContentView and the first dialog show()
		// (if any) are happy. Heavy alarm scheduling can run after the
		// tree is up.
		runSetup()
	})
}

// logf appends to a transient buffer and forwards each line to logcat.
// Used for narrative status messages so test_all_apks.sh and operators
// can read progress without a Canvas surface.
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

	// 1) classloader + proxy init for env.NewProxy.
	if err := vm.Do(func(env *jni.Env) error {
		return installProxyClassLoader(env, act)
	}); err != nil {
		return fmt.Errorf("install proxy class loader: %w", err)
	}

	// NativeActivity owns an opaque GL surface that occludes any Java
	// widgets attached via setContentView. Switching the window pixel
	// format to TRANSLUCENT makes the surface transparent so the widget
	// tree below is actually rendered. Must run before setContentView.
	if err := makeWindowTranslucent(vm, act); err != nil {
		logf(buf, "make window translucent: %v", err)
		// Continue: widgets may still partially render or hide is acceptable in test
	}

	// 2) Open SharedPreferences and load/seed entries.
	ctx := &app.Context{VM: vm, Obj: act}
	rootContext = &app.Context{VM: vm, Obj: act}
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

	// 3) Build the root LinearLayout with padding, title, scroll list, add button.
	root, err := widget.NewLinearLayout(vm, act)
	if err != nil {
		return fmt.Errorf("new root LinearLayout: %w", err)
	}
	if err := root.SetOrientation(orientationV); err != nil {
		return fmt.Errorf("root setOrientation: %w", err)
	}
	rootView := &display.View{VM: vm, Obj: root.Obj}
	if err := rootView.SetPadding(rootPaddingPx, rootPaddingPx, rootPaddingPx, rootPaddingPx); err != nil {
		return fmt.Errorf("root setPadding: %w", err)
	}

	title, err := widget.NewTextView(vm, act)
	if err != nil {
		return fmt.Errorf("new title TextView: %w", err)
	}
	if err := title.SetText1_3("Alarms"); err != nil {
		return fmt.Errorf("title setText: %w", err)
	}
	if err := title.SetTextSize1(textTitleSp); err != nil {
		return fmt.Errorf("title setTextSize: %w", err)
	}
	rootGroup := &display.ViewGroup{VM: vm, Obj: root.Obj}
	if err := rootGroup.AddView1(title.Obj); err != nil {
		return fmt.Errorf("root addView title: %w", err)
	}

	scroll, err := widget.NewScrollView(vm, act)
	if err != nil {
		return fmt.Errorf("new ScrollView: %w", err)
	}
	scrollLP, err := newLinearLayoutLayoutParamsWeighted(vm, matchParent, 0, 1)
	if err != nil {
		return fmt.Errorf("scroll layoutparams: %w", err)
	}
	scrollView := &display.View{VM: vm, Obj: scroll.Obj}
	if err := scrollView.SetLayoutParams(scrollLP); err != nil {
		return fmt.Errorf("scroll setLayoutParams: %w", err)
	}
	if err := rootGroup.AddView1(scroll.Obj); err != nil {
		return fmt.Errorf("root addView scroll: %w", err)
	}

	list, err := widget.NewLinearLayout(vm, act)
	if err != nil {
		return fmt.Errorf("new list LinearLayout: %w", err)
	}
	if err := list.SetOrientation(orientationV); err != nil {
		return fmt.Errorf("list setOrientation: %w", err)
	}
	scrollGroup := &display.ViewGroup{VM: vm, Obj: scroll.Obj}
	if err := scrollGroup.AddView1(list.Obj); err != nil {
		return fmt.Errorf("scroll addView list: %w", err)
	}
	listLayout = list

	addBtn, err := widget.NewButton(vm, act)
	if err != nil {
		return fmt.Errorf("new add Button: %w", err)
	}
	if err := (&widget.TextView{VM: vm, Obj: addBtn.Obj}).SetText1_3("+ Add alarm"); err != nil {
		return fmt.Errorf("addBtn setText: %w", err)
	}
	if err := rootGroup.AddView1(addBtn.Obj); err != nil {
		return fmt.Errorf("root addView addBtn: %w", err)
	}
	if err := attachClickListener(vm, &display.View{VM: vm, Obj: addBtn.Obj}, onAddClicked); err != nil {
		return fmt.Errorf("addBtn setOnClickListener: %w", err)
	}

	// Populate rows.
	if err := rebuildList(); err != nil {
		return fmt.Errorf("rebuild list: %w", err)
	}

	// 4) Attach root to the activity.
	activity := &app.Activity{VM: vm, Obj: act}
	if err := activity.SetContentView1(root.Obj); err != nil {
		return fmt.Errorf("setContentView: %w", err)
	}
	logf(buf, "widget tree attached")

	// 5) Schedule alarms (best-effort; Permission denials are logged).
	stateMu.Lock()
	scheduleSnapshot := make([]alarmEntry, len(entries))
	copy(scheduleSnapshot, entries)
	storeSnapshot := make([]alarmEntry, len(entries))
	copy(storeSnapshot, entries)
	stateMu.Unlock()
	if err := scheduleEnabled(vm, ctx, scheduleSnapshot, buf); err != nil {
		logf(buf, "scheduleEnabled: %v", err)
	}

	// 6) Persist sorted form.
	if err := storeEntries(&sp, storeSnapshot); err != nil {
		return fmt.Errorf("store entries: %w", err)
	}
	return nil
}

// installProxyClassLoader points the proxy machinery at the activity's
// ClassLoader so it can find GoInvocationHandler in our APK's classes.dex.
// FindClass() from native threads consults the BootClassLoader, which
// can't see APK classes — proxy.go falls back to this loader on miss.
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
// RGB_565 format installed by NativeActivity. Must run on the UI
// thread before setContentView so the first traversal paints the
// widget tree.
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
		// Release the native surface callback installed by NativeActivity
		// so the view hierarchy paints into the window surface.
		takeSurfaceMid, err := env.GetMethodID(winCls, "takeSurface",
			"(Landroid/view/SurfaceHolder$Callback2;)V")
		if err != nil {
			return fmt.Errorf("getMethodID takeSurface: %w", err)
		}
		if err := env.CallVoidMethod(window, takeSurfaceMid, jni.ObjectValue(nil)); err != nil {
			return fmt.Errorf("call takeSurface(null): %w", err)
		}
		// Release the native input queue callback too. NativeActivity also
		// owns the input queue; without releasing it, touch events go to
		// the unread native queue and the Java view dispatcher starves,
		// producing ANRs on the first user tap.
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

// newLinearLayoutLayoutParamsWeighted constructs
// android.widget.LinearLayout$LayoutParams(width, height, weight) since
// the constructor isn't bound. Returns a global ref.
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

// rebuildList wipes the inner LinearLayout and re-adds one row per
// entry. Called from the UI thread.
func rebuildList() error {
	if listLayout == nil {
		return fmt.Errorf("listLayout not initialised")
	}
	vm := globalVM
	act := activityRef

	listGroup := &display.ViewGroup{VM: vm, Obj: listLayout.Obj}
	if err := listGroup.RemoveAllViews(); err != nil {
		return fmt.Errorf("removeAllViews: %w", err)
	}

	for i := range entries {
		row, err := buildRow(vm, act, i)
		if err != nil {
			return fmt.Errorf("build row %d: %w", i, err)
		}
		if err := listGroup.AddView1(row); err != nil {
			return fmt.Errorf("add row %d: %w", i, err)
		}
	}
	return nil
}

// buildRow constructs one alarm row: horizontal LinearLayout containing a
// time+label TextView, a Switch, an Edit Button, and a Delete Button. The
// row index is captured by each click handler so user interaction looks
// up the current entry by position even after sorts.
func buildRow(vm *jni.VM, act *jni.Object, idx int) (*jni.Object, error) {
	row, err := widget.NewLinearLayout(vm, act)
	if err != nil {
		return nil, fmt.Errorf("new row LinearLayout: %w", err)
	}
	if err := row.SetOrientation(orientationH); err != nil {
		return nil, err
	}
	rowView := &display.View{VM: vm, Obj: row.Obj}
	if err := rowView.SetPadding(0, rowPaddingPx, 0, rowPaddingPx); err != nil {
		return nil, err
	}
	rowGroup := &display.ViewGroup{VM: vm, Obj: row.Obj}

	entry := entries[idx]

	label, err := widget.NewTextView(vm, act)
	if err != nil {
		return nil, fmt.Errorf("new label TextView: %w", err)
	}
	if err := label.SetText1_3(fmt.Sprintf("%s  %s", formatTime(entry.At), entry.Label)); err != nil {
		return nil, err
	}
	if err := label.SetTextSize1(textRowSp); err != nil {
		return nil, err
	}
	labelLP, err := newLinearLayoutLayoutParamsWeighted(vm, 0, wrapContent, 1)
	if err != nil {
		return nil, fmt.Errorf("label LP: %w", err)
	}
	if err := (&display.View{VM: vm, Obj: label.Obj}).SetLayoutParams(labelLP); err != nil {
		return nil, err
	}
	if err := rowGroup.AddView1(label.Obj); err != nil {
		return nil, err
	}

	sw, err := widget.NewSwitch(vm, act)
	if err != nil {
		return nil, fmt.Errorf("new Switch: %w", err)
	}
	if err := sw.SetChecked(entry.Enabled); err != nil {
		return nil, err
	}
	if err := rowGroup.AddView1(sw.Obj); err != nil {
		return nil, err
	}
	if err := attachSwitchListener(vm, sw, idx); err != nil {
		return nil, fmt.Errorf("attach switch listener: %w", err)
	}

	editBtn, err := widget.NewButton(vm, act)
	if err != nil {
		return nil, fmt.Errorf("new Edit Button: %w", err)
	}
	if err := (&widget.TextView{VM: vm, Obj: editBtn.Obj}).SetText1_3("Edit"); err != nil {
		return nil, err
	}
	if err := rowGroup.AddView1(editBtn.Obj); err != nil {
		return nil, err
	}
	rowIdx := idx
	if err := attachClickListener(vm, &display.View{VM: vm, Obj: editBtn.Obj}, func() {
		onEditClicked(rowIdx)
	}); err != nil {
		return nil, err
	}

	delBtn, err := widget.NewButton(vm, act)
	if err != nil {
		return nil, fmt.Errorf("new Delete Button: %w", err)
	}
	if err := (&widget.TextView{VM: vm, Obj: delBtn.Obj}).SetText1_3("Delete"); err != nil {
		return nil, err
	}
	if err := rowGroup.AddView1(delBtn.Obj); err != nil {
		return nil, err
	}
	if err := attachClickListener(vm, &display.View{VM: vm, Obj: delBtn.Obj}, func() {
		onDeleteClicked(rowIdx)
	}); err != nil {
		return nil, err
	}

	return row.Obj, nil
}

// attachSwitchListener wires android.widget.CompoundButton$OnCheckedChangeListener
// onto the Switch so toggling the row's "enabled" flag round-trips through
// rescheduling.
func attachSwitchListener(vm *jni.VM, sw *widget.Switch, idx int) error {
	return vm.Do(func(env *jni.Env) error {
		listenerCls, err := env.FindClass("android/widget/CompoundButton$OnCheckedChangeListener")
		if err != nil {
			return fmt.Errorf("find OnCheckedChangeListener: %w", err)
		}
		proxy, cleanup, err := env.NewProxy(
			[]*jni.Class{listenerCls},
			func(_ *jni.Env, method string, args []*jni.Object) (*jni.Object, error) {
				if method == "onCheckedChanged" && len(args) == 2 {
					// args[1] is a boxed Boolean; rather than unbox by
					// hand we re-read via CompoundButton.isChecked() —
					// Switch inherits the method from CompoundButton.
					cb := &widget.CompoundButton{VM: vm, Obj: sw.Obj}
					checked, _ := cb.IsChecked()
					onSwitchToggled(idx, checked)
				}
				return nil, nil
			},
		)
		if err != nil {
			return fmt.Errorf("NewProxy(OnCheckedChangeListener): %w", err)
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

		setMID, err := env.GetMethodID(env.GetObjectClass(sw.Obj),
			"setOnCheckedChangeListener",
			"(Landroid/widget/CompoundButton$OnCheckedChangeListener;)V")
		if err != nil {
			return fmt.Errorf("get setOnCheckedChangeListener: %w", err)
		}
		return env.CallVoidMethod(sw.Obj, setMID, jni.ObjectValue(listenerGlobal))
	})
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

// onEditClicked opens a TimePickerDialog seeded with the row's current
// time. On confirm, replaces the time and persists.
func onEditClicked(idx int) {
	if idx < 0 || idx >= len(entries) {
		return
	}
	t := time.UnixMilli(entries[idx].At)
	openTimePicker(t.Hour(), t.Minute(), func(h, m int) {
		stateMu.Lock()
		if idx < len(entries) {
			entries[idx].At = nextOccurrence(h, m).UnixMilli()
		}
		stateMu.Unlock()
		applyMutation()
	})
}

// onDeleteClicked removes the row, persists, redraws.
func onDeleteClicked(idx int) {
	stateMu.Lock()
	if idx < 0 || idx >= len(entries) {
		stateMu.Unlock()
		return
	}
	entries = append(entries[:idx], entries[idx+1:]...)
	stateMu.Unlock()
	applyMutation()
}

// onSwitchToggled flips the entry's Enabled flag and reschedules.
func onSwitchToggled(idx int, checked bool) {
	stateMu.Lock()
	if idx < 0 || idx >= len(entries) {
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

// applyMutation re-sorts, persists, reschedules, and rebuilds the row
// list. Called from the UI thread (proxy callbacks run on it).
func applyMutation() {
	stateMu.Lock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].At < entries[j].At })
	snapshot := make([]alarmEntry, len(entries))
	copy(snapshot, entries)
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
	if err := rebuildList(); err != nil {
		logf(&buf, "rebuildList: %v", err)
	}
}

// openTimePicker creates a new TimePickerDialog with a Go-side
// OnTimeSetListener proxy, configures it for 24h time, and shows it.
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
// snapshot is a caller-owned copy of the entries slice taken under
// stateMu, so this function can iterate without re-acquiring the lock.
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
// static-nested classes.
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
		// Try the current schema (versioned envelope) first.
		var payload alarmsPayload
		if jsonErr := json.Unmarshal([]byte(raw), &payload); jsonErr == nil && payload.Version >= 1 && len(payload.Entries) > 0 {
			return payload.Entries, false, nil
		}
		// Legacy payload (bare JSON array, no version field). Use Enabled
		// as-stored: the JSON spec gives missing booleans the zero value
		// (false), and we accept that legacy users opt back in via the
		// Switch UI rather than silently flipping their disabled alarms on.
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
