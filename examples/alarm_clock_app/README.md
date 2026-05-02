# alarm_clock_app

A minimal Material 3 alarm clock app demo: pure Go, no hand-written Java, real Android widgets.

## What it does

- Persists a list of named alarms in `SharedPreferences` (`alarm_clock_app` / `alarms`) as a versioned JSON envelope, surviving relaunches.
- Sorts the list by trigger date on every launch and on every mutation.
- Renders a real Java widget tree built entirely from Go via existing JNI
  bindings: a vertical `LinearLayout` root with title `TextView`, a
  `ScrollView` containing per-alarm rows (time + label + `Switch` + EDIT +
  DELETE), and a "+ ADD ALARM" button at the bottom. EDIT and ADD open a
  `TimePickerDialog` whose `OnTimeSetListener` is a Go closure registered
  via `env.NewProxy`.
- Schedules each enabled alarm via `AlarmManager.setAlarmClock` (the same
  surface used by the system Clock app), built around a
  `PendingIntent.getActivity` aimed back at the demo's own `NativeActivity`.
- Reports the next system-wide alarm clock returned by
  `AlarmManager.getNextAlarmClock` to logcat so operators can confirm the
  OS accepted the schedule.

On first launch, five entries are seeded in deliberately unsorted order
(`Wake up`, `Stand-up`, `Coffee break`, `Lunch`, `Tea` at +90s, +30s, +180s,
+10s, +120s respectively, IDs 1..5) so the sort step is visibly meaningful.
Subsequent launches load the sorted form from storage.

## Build, install, run

```
make
make install
make run
```

`make run` resolves to:

```
adb shell am start -n center.dx.jni.examples.alarm_clock_app/android.app.NativeActivity
```

For an emulator vs real device with both attached, scope adb explicitly:
`adb -s emulator-5554 ...` or `adb -s <serial> ...`. Logcat output uses
the `GoJNI` tag: `adb logcat -s GoJNI:* '*:S'`.

## NativeActivity widget integration

Cycle-critical lifecycle finding for any future Go-only NativeActivity
widget app: `NativeActivity` calls `getWindow().takeSurface(this)` and
`getWindow().takeInputQueue(this)` in `onCreate`, claiming both the
window's GL surface and its input dispatch. Without releasing both,
Java widgets are invisible behind an opaque surface AND tap input is
starved producing ANRs.

This demo's setup runs the following sequence (via raw JNI, in
`makeWindowTranslucent`) before `setContentView`, restoring the
standard Java view + input flow:

```
window.takeSurface(null);
window.takeInputQueue(null);
window.setFormat(PixelFormat.TRANSLUCENT);  // -3
```

## Permissions

The demo declares only `android.permission.USE_EXACT_ALARM`. That permission
is install-time and auto-granted at install on Android 13+ when the app
targets SDK 33+, so no runtime prompt or Special Access toggle is involved.

`USE_EXACT_ALARM` is the alarm-clock-app permission: it was added to remove
the user-facing toggle for apps whose core function is to surface alarm
clocks (this demo schedules via `AlarmManager.setAlarmClock`, exactly that
use case). Apps that need exact alarms but are **not** alarm clocks must
declare `SCHEDULE_EXACT_ALARM` instead and direct the user to enable
*Settings -> Apps -> <app> -> Alarms & reminders* by hand, since
`SCHEDULE_EXACT_ALARM` is an AppOps "special access" permission and cannot
be granted via the standard runtime permission dialog.

If `mgr.CanScheduleExactAlarms()` returns false on an older device or in a
configuration where the permission is not effective, each `setAlarmClock`
call raises `SecurityException`; the demo catches it per entry and prints
the error so the rest of the run still produces output.

## Signing

The shared `examples/apk.mk` auto-generates a debug keystore on first build
and signs with `apksigner`. The exact lines from `apk.mk`:

```
$(BUILD)/debug.keystore:
        @mkdir -p $(BUILD)
        keytool -genkeypair -keystore $@ -storepass android -alias debug \
                -keyalg RSA -keysize 2048 -validity 10000 \
                -dname "CN=Debug" -noprompt 2>/dev/null
...
        $(APKSIGNER) sign --ks $(BUILD)/debug.keystore --ks-pass pass:android \
                --out $@ $(BUILD)/aligned.apk
```

For release signing, swap the keystore + alias: point `--ks` at your release
keystore and `--ks-pass`/`--key-pass` at the corresponding secrets, and use
the matching `--ks-key-alias`. Nothing in the example needs to change.

## gRPC-Go versions

This repository does **not** depend on gRPC-Go. The gRPC-related code lives
in the separate companion repo `jni-proxy`. Upgrading `google.golang.org/grpc`
upstream cannot break this demo or the JNI bindings in this module.

## Roadmap notes

Honest list of binding gaps the demo had to work around with raw JNI calls
(each is a candidate overlay TODO):

- `AlarmManager$AlarmClockInfo(long, PendingIntent)` constructor — `javagen`
  does not currently emit constructors for static-nested classes.
- `LinearLayout$LayoutParams(int, int, float)` constructor — needed for
  weight-based row layouts.
- `CompoundButton.setOnCheckedChangeListener(OnCheckedChangeListener)` —
  the `widget.Switch` / `widget.CompoundButton` Go bindings expose no setter
  for the listener interface yet.

`RecyclerView`, `MaterialToolbar`, `FloatingActionButton`, `MaterialCardView`,
and `MaterialTimePicker` are also missing as Go bindings — this demo gets a
modern Material 3 look on the existing `widget/*` (`LinearLayout`, `Switch`,
`Button`) plus the Material 3 theme XML applied at the manifest level. A
follow-up cycle should generate the AndroidX/Material binding set from the
`.aar-cache/` closure produced by `tools/cmd/aar-resolve`.
