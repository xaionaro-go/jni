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
//
// All Activity-lifecycle and proxy-classloader plumbing is shared with
// other widget-mode examples via examples/common/widgetui; the alarmApp
// struct below owns every piece of mutable state previously held in
// package-level vars.
package main

/*
#include <android/native_activity.h>
extern void goOnResume(ANativeActivity*);
static void _onResume(ANativeActivity* a) { goOnResume(a); }
static void _setCallbacks(ANativeActivity* a) { a->callbacks->onResume = _onResume; }
static uintptr_t _getVM(ANativeActivity* a) { return (uintptr_t)a->vm; }
static uintptr_t _getClazz(ANativeActivity* a) { return (uintptr_t)a->clazz; }
*/
import "C"
import (
	"encoding/json"
	"fmt"
	"sort"
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
	"github.com/AndroidGoLab/jni/examples/common/widgetui"
	"github.com/AndroidGoLab/jni/view/display"
	"github.com/AndroidGoLab/jni/widget"
	widget_consts "github.com/AndroidGoLab/jni/widget/consts"
)

const (
	prefsName = "alarm_clock_app"
	prefsKey  = "alarms"

	// Pixel values. We don't bother with density conversion: the layout is
	// generous enough that 24-32px padding looks sane on the typical phone DPI.
	rootPaddingPx = int32(24)
	cardMarginPx  = int32(16)
	cardPaddingPx = int32(24)
	textRowSp     = float32(20)
	cardRadius    = float32(28)

	// Layout-param magic values lifted from android.widget so we don't
	// repeat the int32 conversion at every call site.
	matchParent  = int32(widget_consts.MatchParent)
	wrapContent  = int32(widget_consts.WrapContent)
	orientationV = int32(widget_consts.OrientationVertical)
	orientationH = int32(widget_consts.OrientationHorizontal)
	rvVertical   = int32(rv_consts.Vertical)

	// gravityEnd matches android.view.Gravity.END | CENTER_VERTICAL,
	// used to drop the FAB on the right edge inside the vertical
	// LinearLayout.
	gravityEnd = int32(0x00800005)

	// android.R.drawable.ic_input_add — a built-in "+" glyph; saves
	// shipping a vector drawable for what is essentially a placeholder
	// icon.
	resIDInputAdd = int32(0x01080003)

	alarmsPayloadVersion = 1
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

// rowViews is the per-card widget tree captured at onCreateViewHolder time
// so onBindViewHolder can update it cheaply for the bound entry.
type rowViews struct {
	itemView    *jni.Object
	label       *widget.TextView
	sw          *materialswitch.MaterialSwitch
	editBtn     *button.MaterialButton
	delBtn      *button.MaterialButton
	swCleanup   func()
	editCleanup func()
	delCleanup  func()
}

// alarmApp owns every piece of state previously held in package-level
// vars: model + adapter + per-holder caches + scheduling resources.
// Constructed once in setup() and threaded through methods.
type alarmApp struct {
	vm  *jni.VM
	act *jni.Object
	ctx *app.Context

	mu sync.Mutex
	// Mutable model.
	entries []alarmEntry
	nextID  int32
	// Adapter cleanup (from NewProxy).
	adapter        *jni.Object
	adapterCleanup func()
	// Per-holder widget refs, keyed by Go-side holderID. The card's
	// itemView.setTag() carries an Integer with this ID so
	// onBindViewHolder can find the cached widgets.
	holders      map[int32]*rowViews
	nextHolderID int32
	// Listener cleanup hooks for global (FAB / TimePicker) listeners.
	cleanups []func()
}

func main() {}

func init() { widgetui.Register(setup) }

//export ANativeActivity_onCreate
func ANativeActivity_onCreate(activity *C.ANativeActivity, savedState unsafe.Pointer, savedStateSize C.size_t) {
	_ = savedState
	_ = savedStateSize
	widgetui.OnCreate(
		jni.VMFromUintptr(uintptr(C._getVM(activity))),
		jni.ObjectFromUintptr(uintptr(C._getClazz(activity))),
	)
	C._setCallbacks(activity)
}

//export goOnResume
func goOnResume(_ *C.ANativeActivity) {
	widgetui.OnResume()
}

// setup is the widgetui Setup callback: the proxy class loader is
// already installed and the window is already translucent. We
// build the widget tree, attach it to the activity, schedule alarms
// and persist.
func setup(vm *jni.VM, activity *jni.Object) error {
	a := &alarmApp{
		vm:      vm,
		act:     activity,
		ctx:     &app.Context{VM: vm, Obj: activity},
		holders: map[int32]*rowViews{},
	}

	spObj, err := a.ctx.GetSharedPreferences(prefsName, 0)
	if err != nil {
		return fmt.Errorf("getSharedPreferences: %w", err)
	}
	sp := preferences.SharedPreferences{VM: vm, Obj: spObj}

	loaded, seeded, err := loadOrSeedEntries(&sp)
	if err != nil {
		return fmt.Errorf("load entries: %w", err)
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].At < loaded[j].At })
	a.entries = loaded
	a.nextID = maxID(a.entries) + 1
	if seeded {
		widgetui.Logf("(seeded fresh entries this run)")
	}
	widgetui.Logf("%d alarm(s) loaded", len(a.entries))

	root, err := a.buildWidgetTree()
	if err != nil {
		return fmt.Errorf("buildWidgetTree: %w", err)
	}

	activityHelper := &app.Activity{VM: vm, Obj: activity}
	if err := activityHelper.SetContentView1(root); err != nil {
		return fmt.Errorf("setContentView: %w", err)
	}
	widgetui.Logf("widget tree attached")

	a.mu.Lock()
	scheduleSnapshot := append([]alarmEntry(nil), a.entries...)
	storeSnapshot := append([]alarmEntry(nil), a.entries...)
	a.mu.Unlock()
	if err := a.scheduleEnabled(scheduleSnapshot); err != nil {
		widgetui.Logf("scheduleEnabled: %v", err)
	}
	if err := storeEntries(&sp, storeSnapshot); err != nil {
		return fmt.Errorf("store entries: %w", err)
	}
	widgetui.Logf("alarm_clock_app complete")
	return nil
}

// buildWidgetTree assembles the root LinearLayout containing the
// MaterialToolbar, the RecyclerView (with a Go adapter), and the
// FloatingActionButton. Returns the root view's underlying Object so the
// caller can hand it to setContentView.
func (a *alarmApp) buildWidgetTree() (*jni.Object, error) {
	root, err := widget.NewLinearLayout(a.vm, a.act)
	if err != nil {
		return nil, fmt.Errorf("new root LinearLayout: %w", err)
	}
	if err := root.SetOrientation(orientationV); err != nil {
		return nil, fmt.Errorf("root setOrientation: %w", err)
	}
	rootView := &display.View{VM: a.vm, Obj: root.Obj}
	if err := rootView.SetPadding(rootPaddingPx, rootPaddingPx, rootPaddingPx, rootPaddingPx); err != nil {
		return nil, fmt.Errorf("root setPadding: %w", err)
	}
	rootGroup := &display.ViewGroup{VM: a.vm, Obj: root.Obj}

	toolbar, err := appbar.NewMaterialToolbar(a.vm, a.act)
	if err != nil {
		return nil, fmt.Errorf("new MaterialToolbar: %w", err)
	}
	if err := (&appcompat_widget.Toolbar{VM: a.vm, Obj: toolbar.Obj}).SetTitle1_1("Alarms"); err != nil {
		return nil, fmt.Errorf("toolbar setTitle: %w", err)
	}
	if err := rootGroup.AddView1(toolbar.Obj); err != nil {
		return nil, fmt.Errorf("root addView toolbar: %w", err)
	}

	rv, err := rvwidget.NewRecyclerView(a.vm, a.act)
	if err != nil {
		return nil, fmt.Errorf("new RecyclerView: %w", err)
	}
	rvLP, err := newLinearLayoutLayoutParamsWeighted(a.vm, matchParent, 0, 1)
	if err != nil {
		return nil, fmt.Errorf("rv layoutparams: %w", err)
	}
	rvView := &display.View{VM: a.vm, Obj: rv.Obj}
	if err := rvView.SetLayoutParams(rvLP); err != nil {
		return nil, fmt.Errorf("rv setLayoutParams: %w", err)
	}
	lm, err := rvwidget.NewLinearLayoutManager(a.vm, a.act)
	if err != nil {
		return nil, fmt.Errorf("new LinearLayoutManager: %w", err)
	}
	if err := lm.SetOrientation(rvVertical); err != nil {
		return nil, fmt.Errorf("lm setOrientation: %w", err)
	}
	if err := rv.SetLayoutManager(lm.Obj); err != nil {
		return nil, fmt.Errorf("rv setLayoutManager: %w", err)
	}
	adapterObj, cleanup, err := a.newAlarmAdapter()
	if err != nil {
		return nil, fmt.Errorf("new alarm Adapter: %w", err)
	}
	a.adapter = adapterObj
	a.adapterCleanup = cleanup
	if err := rv.SetAdapter(adapterObj); err != nil {
		return nil, fmt.Errorf("rv setAdapter: %w", err)
	}
	if err := rootGroup.AddView1(rv.Obj); err != nil {
		return nil, fmt.Errorf("root addView rv: %w", err)
	}

	fab, err := floatingactionbutton.NewFloatingActionButton(a.vm, a.act)
	if err != nil {
		return nil, fmt.Errorf("new FAB: %w", err)
	}
	if err := fab.SetImageResource(resIDInputAdd); err != nil {
		return nil, fmt.Errorf("fab setImageResource: %w", err)
	}
	fabLP, err := newLinearLayoutLayoutParamsGravity(a.vm, wrapContent, wrapContent, gravityEnd)
	if err != nil {
		return nil, fmt.Errorf("fab layoutparams: %w", err)
	}
	if err := (&display.View{VM: a.vm, Obj: fab.Obj}).SetLayoutParams(fabLP); err != nil {
		return nil, fmt.Errorf("fab setLayoutParams: %w", err)
	}
	if err := rootGroup.AddView1(fab.Obj); err != nil {
		return nil, fmt.Errorf("root addView fab: %w", err)
	}
	if err := a.attachClickListener(&display.View{VM: a.vm, Obj: fab.Obj}, a.onAddClicked); err != nil {
		return nil, fmt.Errorf("fab setOnClickListener: %w", err)
	}

	return root.Obj, nil
}

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
func (a *alarmApp) newAlarmAdapter() (*jni.Object, func(), error) {
	var (
		proxyObj *jni.Object
		cleanup  func()
	)
	err := a.vm.Do(func(env *jni.Env) error {
		cls, err := env.FindClass("androidx/recyclerview/widget/RecyclerView$Adapter")
		if err != nil {
			return fmt.Errorf("find RecyclerView.Adapter: %w", err)
		}
		defer env.DeleteLocalRef(&cls.Object)
		p, c, err := env.NewProxy(
			[]*jni.Class{cls},
			func(callEnv *jni.Env, method string, args []*jni.Object) (*jni.Object, error) {
				return a.dispatchAdapter(callEnv, method, args)
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
func (a *alarmApp) dispatchAdapter(
	env *jni.Env,
	method string,
	args []*jni.Object,
) (*jni.Object, error) {
	switch method {
	case "getItemCount":
		a.mu.Lock()
		n := int32(len(a.entries))
		a.mu.Unlock()
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
		// MaterialCardView accepts the activity Context directly.
		holder, err := a.createRowHolder(env)
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
		if err := a.bindRowHolder(env, args[0], pos); err != nil {
			return nil, fmt.Errorf("bindRowHolder: %w", err)
		}
		return nil, nil
	default:
		return nil, nil
	}
}

// createRowHolder builds a fresh MaterialCardView with the row widgets,
// caches them in a.holders, tags the card with the holder ID, and wraps it
// in a RecyclerView$ViewHolderAdapter (the cycle-7 generated concrete
// subclass of RecyclerView.ViewHolder that takes a single View arg).
func (a *alarmApp) createRowHolder(env *jni.Env) (*jni.Object, error) {
	cardObj, refs, err := a.buildCardRow()
	if err != nil {
		return nil, fmt.Errorf("buildCardRow: %w", err)
	}

	a.mu.Lock()
	holderID := a.nextHolderID
	a.nextHolderID++
	a.holders[holderID] = refs
	a.mu.Unlock()

	tagBox, err := boxInteger(env, holderID)
	if err != nil {
		return nil, fmt.Errorf("box holderID: %w", err)
	}
	defer env.DeleteLocalRef(tagBox)
	tagGlobal := env.NewGlobalRef(tagBox)
	if err := (&display.View{VM: a.vm, Obj: cardObj}).SetTag1_1(tagGlobal); err != nil {
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
func (a *alarmApp) bindRowHolder(env *jni.Env, holder *jni.Object, position int) error {
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
	tagObj, err := (&display.View{VM: a.vm, Obj: itemView}).GetTag0()
	if err != nil {
		return fmt.Errorf("getTag: %w", err)
	}
	holderID, err := unboxInt(env, tagObj)
	if err != nil {
		return fmt.Errorf("unbox holderID: %w", err)
	}
	a.mu.Lock()
	refs, ok := a.holders[int32(holderID)]
	if !ok || position < 0 || position >= len(a.entries) {
		a.mu.Unlock()
		return fmt.Errorf("holder/position out of sync (holderID=%d position=%d)", holderID, position)
	}
	entry := a.entries[position]
	a.mu.Unlock()

	if err := refs.label.SetText1_3(fmt.Sprintf("%s  %s", formatTime(entry.At), entry.Label)); err != nil {
		return fmt.Errorf("label setText: %w", err)
	}
	cb := &widget.CompoundButton{VM: a.vm, Obj: refs.sw.Obj}
	if err := cb.SetChecked(entry.Enabled); err != nil {
		return fmt.Errorf("switch setChecked: %w", err)
	}
	// Replace listeners; previous listeners are torn down via cleanups.
	if refs.swCleanup != nil {
		refs.swCleanup()
	}
	swClean, err := a.attachCheckedChangeListenerByID(refs.sw, entry.ID)
	if err != nil {
		return fmt.Errorf("attach switch listener: %w", err)
	}
	refs.swCleanup = swClean

	if refs.editCleanup != nil {
		refs.editCleanup()
	}
	editClean, err := a.attachClickListenerByID(&display.View{VM: a.vm, Obj: refs.editBtn.Obj}, entry.ID, a.onEditClickedByID)
	if err != nil {
		return fmt.Errorf("attach edit listener: %w", err)
	}
	refs.editCleanup = editClean

	if refs.delCleanup != nil {
		refs.delCleanup()
	}
	delClean, err := a.attachClickListenerByID(&display.View{VM: a.vm, Obj: refs.delBtn.Obj}, entry.ID, a.onDeleteClickedByID)
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
func (a *alarmApp) buildCardRow() (*jni.Object, *rowViews, error) {
	mc, err := card.NewMaterialCardView(a.vm, a.act)
	if err != nil {
		return nil, nil, fmt.Errorf("new MaterialCardView: %w", err)
	}
	if err := mc.SetRadius(cardRadius); err != nil {
		return nil, nil, fmt.Errorf("card setRadius: %w", err)
	}
	cardLP, err := newLinearLayoutLayoutParamsMargins(a.vm, matchParent, wrapContent, cardMarginPx)
	if err != nil {
		return nil, nil, fmt.Errorf("card layoutparams: %w", err)
	}
	cardView := &display.View{VM: a.vm, Obj: mc.Obj}
	if err := cardView.SetLayoutParams(cardLP); err != nil {
		return nil, nil, fmt.Errorf("card setLayoutParams: %w", err)
	}

	row, err := widget.NewLinearLayout(a.vm, a.act)
	if err != nil {
		return nil, nil, fmt.Errorf("new row LinearLayout: %w", err)
	}
	if err := row.SetOrientation(orientationH); err != nil {
		return nil, nil, fmt.Errorf("row setOrientation: %w", err)
	}
	rowView := &display.View{VM: a.vm, Obj: row.Obj}
	if err := rowView.SetPadding(cardPaddingPx, cardPaddingPx, cardPaddingPx, cardPaddingPx); err != nil {
		return nil, nil, fmt.Errorf("row setPadding: %w", err)
	}
	rowGroup := &display.ViewGroup{VM: a.vm, Obj: row.Obj}
	cardGroup := &display.ViewGroup{VM: a.vm, Obj: mc.Obj}
	if err := cardGroup.AddView1(row.Obj); err != nil {
		return nil, nil, fmt.Errorf("card addView row: %w", err)
	}

	label, err := widget.NewTextView(a.vm, a.act)
	if err != nil {
		return nil, nil, fmt.Errorf("new label TextView: %w", err)
	}
	if err := label.SetTextSize1(textRowSp); err != nil {
		return nil, nil, fmt.Errorf("label setTextSize: %w", err)
	}
	labelLP, err := newLinearLayoutLayoutParamsWeighted(a.vm, 0, wrapContent, 1)
	if err != nil {
		return nil, nil, fmt.Errorf("label LP: %w", err)
	}
	if err := (&display.View{VM: a.vm, Obj: label.Obj}).SetLayoutParams(labelLP); err != nil {
		return nil, nil, fmt.Errorf("label setLayoutParams: %w", err)
	}
	if err := rowGroup.AddView1(label.Obj); err != nil {
		return nil, nil, fmt.Errorf("row addView label: %w", err)
	}

	sw, err := materialswitch.NewMaterialSwitch(a.vm, a.act)
	if err != nil {
		return nil, nil, fmt.Errorf("new MaterialSwitch: %w", err)
	}
	if err := rowGroup.AddView1(sw.Obj); err != nil {
		return nil, nil, fmt.Errorf("row addView switch: %w", err)
	}

	editBtn, err := button.NewMaterialButton(a.vm, a.act)
	if err != nil {
		return nil, nil, fmt.Errorf("new edit MaterialButton: %w", err)
	}
	if err := (&widget.TextView{VM: a.vm, Obj: editBtn.Obj}).SetText1_3("Edit"); err != nil {
		return nil, nil, fmt.Errorf("edit setText: %w", err)
	}
	if err := rowGroup.AddView1(editBtn.Obj); err != nil {
		return nil, nil, fmt.Errorf("row addView edit: %w", err)
	}

	delBtn, err := button.NewMaterialButton(a.vm, a.act)
	if err != nil {
		return nil, nil, fmt.Errorf("new delete MaterialButton: %w", err)
	}
	if err := (&widget.TextView{VM: a.vm, Obj: delBtn.Obj}).SetText1_3("Delete"); err != nil {
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
// onClick() dispatches to fn, then assigns it via View.setOnClickListener.
// The proxy's cleanup hook is appended to a.cleanups for shutdown — the
// FAB and TimePicker listeners outlive any individual row.
func (a *alarmApp) attachClickListener(v *display.View, fn func()) error {
	return a.vm.Do(func(env *jni.Env) error {
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
		gref := listenerGlobal
		vm := a.vm
		a.mu.Lock()
		a.cleanups = append(a.cleanups, cleanup, func() {
			_ = vm.Do(func(env *jni.Env) error {
				env.DeleteGlobalRef(gref)
				return nil
			})
		})
		a.mu.Unlock()
		return v.SetOnClickListener(listenerGlobal)
	})
}

// attachClickListenerByID is the per-row variant: it captures the entry's
// ID rather than its volatile position, returns its own cleanup func, and
// is reattached on every onBindViewHolder so the dispatched ID stays in
// sync with the (possibly resorted) entries slice.
func (a *alarmApp) attachClickListenerByID(
	v *display.View,
	entryID int32,
	fn func(id int32),
) (func(), error) {
	var (
		out    func()
		retErr error
	)
	retErr = a.vm.Do(func(env *jni.Env) error {
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
		vm := a.vm
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
func (a *alarmApp) attachCheckedChangeListenerByID(
	sw *materialswitch.MaterialSwitch,
	entryID int32,
) (func(), error) {
	var (
		out    func()
		retErr error
	)
	retErr = a.vm.Do(func(env *jni.Env) error {
		listenerCls, err := env.FindClass("android/widget/CompoundButton$OnCheckedChangeListener")
		if err != nil {
			return fmt.Errorf("find OnCheckedChangeListener: %w", err)
		}
		vm := a.vm
		proxy, cleanup, err := env.NewProxy(
			[]*jni.Class{listenerCls},
			func(_ *jni.Env, method string, args []*jni.Object) (*jni.Object, error) {
				if method == "onCheckedChanged" && len(args) == 2 {
					cb := &widget.CompoundButton{VM: vm, Obj: sw.Obj}
					checked, _ := cb.IsChecked()
					a.onSwitchToggledByID(entryID, checked)
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
func (a *alarmApp) onAddClicked() {
	now := time.Now()
	a.openTimePicker(now.Hour(), now.Minute(), func(h, m int) {
		a.mu.Lock()
		id := a.nextID
		a.nextID++
		a.entries = append(a.entries, alarmEntry{
			ID:      id,
			Label:   fmt.Sprintf("Alarm %d", id),
			At:      nextOccurrence(h, m).UnixMilli(),
			Enabled: true,
		})
		a.mu.Unlock()
		a.applyMutation()
	})
}

// onEditClickedByID opens a TimePickerDialog seeded with the row's
// current time. On confirm, replaces the time and persists.
func (a *alarmApp) onEditClickedByID(id int32) {
	a.mu.Lock()
	idx := indexByID(a.entries, id)
	if idx < 0 {
		a.mu.Unlock()
		return
	}
	t := time.UnixMilli(a.entries[idx].At)
	a.mu.Unlock()
	a.openTimePicker(t.Hour(), t.Minute(), func(h, m int) {
		a.mu.Lock()
		idx := indexByID(a.entries, id)
		if idx >= 0 {
			a.entries[idx].At = nextOccurrence(h, m).UnixMilli()
		}
		a.mu.Unlock()
		a.applyMutation()
	})
}

// onDeleteClickedByID removes the entry, persists, redraws.
func (a *alarmApp) onDeleteClickedByID(id int32) {
	a.mu.Lock()
	idx := indexByID(a.entries, id)
	if idx < 0 {
		a.mu.Unlock()
		return
	}
	a.entries = append(a.entries[:idx], a.entries[idx+1:]...)
	a.mu.Unlock()
	a.applyMutation()
}

// onSwitchToggledByID flips the entry's Enabled flag and reschedules.
func (a *alarmApp) onSwitchToggledByID(id int32, checked bool) {
	a.mu.Lock()
	idx := indexByID(a.entries, id)
	if idx < 0 {
		a.mu.Unlock()
		return
	}
	if a.entries[idx].Enabled == checked {
		a.mu.Unlock()
		return
	}
	a.entries[idx].Enabled = checked
	a.mu.Unlock()
	a.applyMutation()
}

// indexByID returns the position of the entry with the given stable ID,
// or -1 if not found. Caller must hold a.mu.
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
func (a *alarmApp) applyMutation() {
	a.mu.Lock()
	sort.Slice(a.entries, func(i, j int) bool { return a.entries[i].At < a.entries[j].At })
	snapshot := append([]alarmEntry(nil), a.entries...)
	a.mu.Unlock()

	widgetui.Logf("applyMutation: %d entries", len(snapshot))

	spObj, err := a.ctx.GetSharedPreferences(prefsName, 0)
	if err == nil {
		sp := preferences.SharedPreferences{VM: a.vm, Obj: spObj}
		_ = storeEntries(&sp, snapshot)
	}
	if err := a.scheduleEnabled(snapshot); err != nil {
		widgetui.Logf("scheduleEnabled: %v", err)
	}
	a.notifyAdapterChanged()
}

// notifyAdapterChanged re-runs the RecyclerView.Adapter pipeline so the
// rows pick up the post-sort entries. Uses RecyclerViewAdapter.NotifyDataSetChanged.
func (a *alarmApp) notifyAdapterChanged() {
	if a.adapter == nil {
		return
	}
	rva := &rvwidget.RecyclerViewAdapter{VM: a.vm, Obj: a.adapter}
	if err := rva.NotifyDataSetChanged(); err != nil {
		widgetui.Logf("notifyDataSetChanged: %v", err)
	}
}

// openTimePicker creates a new TimePickerDialog with a Go-side
// OnTimeSetListener proxy, configures it for 24h time, and shows it.
//
// MaterialTimePicker requires a FragmentManager that NativeActivity does
// not expose, so we stay on the legacy AlertDialog-based picker.
func (a *alarmApp) openTimePicker(initHour, initMinute int, onSet func(hour, minute int)) {
	var dlg *app.TimePickerDialog
	err := a.vm.Do(func(env *jni.Env) error {
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
		a.mu.Lock()
		a.cleanups = append(a.cleanups, cleanup)
		a.mu.Unlock()

		td, err := app.NewTimePickerDialog(a.vm, a.act, listenerGlobal,
			int32(initHour), int32(initMinute), true)
		if err != nil {
			return fmt.Errorf("NewTimePickerDialog: %w", err)
		}
		dlg = td
		return nil
	})
	if err != nil {
		widgetui.Logf("openTimePicker: %v", err)
		return
	}
	if err := dlg.Show(); err != nil {
		widgetui.Logf("TimePickerDialog.Show: %v", err)
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
func (a *alarmApp) scheduleEnabled(snapshot []alarmEntry) error {
	mgr, err := alarm.NewManager(a.ctx)
	if err != nil {
		return fmt.Errorf("alarm.NewManager: %w", err)
	}
	defer mgr.Close()

	canSchedule, canErr := mgr.CanScheduleExactAlarms()
	switch {
	case canErr != nil:
		widgetui.Logf("CanScheduleExactAlarms: %v", canErr)
	case !canSchedule:
		widgetui.Logf("WARN: exact alarms not permitted; setAlarmClock will fail per-entry")
	}

	naClassObj, err := findNativeActivityClass(a.vm)
	if err != nil {
		return fmt.Errorf("find NativeActivity class: %w", err)
	}
	defer releaseGlobalRef(a.vm, naClassObj)

	for _, e := range snapshot {
		if !e.Enabled {
			continue
		}
		if err := a.scheduleOne(mgr, naClassObj, e); err != nil {
			widgetui.Logf("  schedule [%d] %q: %v", e.ID, e.Label, err)
			continue
		}
		widgetui.Logf("  scheduled [%d] %q at %s", e.ID, e.Label, formatTime(e.At))
	}
	return nil
}

func (a *alarmApp) scheduleOne(
	mgr *alarm.Manager,
	naClassObj *jni.Object,
	entry alarmEntry,
) error {
	intent, err := app.NewIntent(a.vm, a.ctx.Obj, naClassObj)
	if err != nil {
		return fmt.Errorf("NewIntent: %w", err)
	}
	defer releaseGlobalRef(a.vm, intent.Obj)

	flags := int32(app_consts.FlagImmutable | app_consts.FlagUpdateCurrent)
	piHelper := app.PendingIntent{VM: a.vm}
	piObj, err := piHelper.GetActivity4(a.ctx.Obj, entry.ID, intent.Obj, flags)
	if err != nil {
		return fmt.Errorf("PendingIntent.getActivity: %w", err)
	}
	if piObj == nil || piObj.Ref() == 0 {
		return fmt.Errorf("PendingIntent.getActivity returned null")
	}
	defer releaseGlobalRef(a.vm, piObj)

	infoObj, err := newAlarmClockInfo(a.vm, entry.At, piObj)
	if err != nil {
		return fmt.Errorf("AlarmClockInfo: %w", err)
	}
	defer releaseGlobalRef(a.vm, infoObj)

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
func loadOrSeedEntries(sp *preferences.SharedPreferences) ([]alarmEntry, bool, error) {
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
		widgetui.Logf("(stored payload unparsable, reseeding)")
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

