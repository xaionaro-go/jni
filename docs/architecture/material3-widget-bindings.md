# Material 3 widget bindings — architecture (Go-only)

## Goal

Deliver Material 3 widget bindings, a Material 3 theme, and an AAR-aware build pipeline so examples like `examples/alarm_clock_app` look indistinguishable from a modern Java Android app, while keeping ALL application code in Go. The only Java in the source tree remains the existing generated proxy-dispatch infrastructure (`GoAbstractDispatch.java`, `GoInvocationHandler.java`) plus the per-class abstract-adapter shims emitted by `templates/java/abstract_adapter.java.tmpl`. **No hand-written Java per example. No AppCompatActivity.** Out of scope: any hand-written Java/Kotlin per example; AppCompatActivity (requires a Java subclass beyond the proxy-dispatch pattern); FragmentActivity-dependent widgets such as MaterialTimePicker and MaterialDatePicker; Jetpack Compose (Kotlin compiler dependency).

## Why this is hard today

- [T3: examples/apk.mk line 87, high] `apk.mk` performs zero resource compilation. Line 87 invokes `aapt2 link` with `-I $(PLATFORM)/android.jar` only — no `aapt2 compile`, no `--java`, no `R.java` emission.
- [T3: examples/apk.mk lines 87-88, high] Only `android.jar` is on the classpath. No AndroidX, no Material Components, no RecyclerView, no ConstraintLayout.
- [T3: proxy.go lines 483, 515, high] `tryAbstractAdapter` exists and is called from the proxy-creation path, but its production exposure to date has been the 2-3-method void-return Camera callback surface. Primitive-`int` return paths and generic `ViewHolder` return paths are not covered by the current `internal/testjvm` corpus and must be verified before RecyclerView work.
- [T1: material-components-android getting-started.md, medium] Material Components for Android transitively depends on `androidx.appcompat:appcompat`, which transitively depends on `kotlin-stdlib`. Even when the example writes zero Kotlin source, the Kotlin runtime bytecode must be present in `classes.dex` for class-load resolution.
- [T1: developer.android.com/tools/aapt2, high] `aapt2 link` requires every transitive AAR's compiled resources (`-R`) and every `R.txt`/Java symbol set (`--java`, `--output-text-symbols`) at link time. A realistic Material 3 closure is 40-60 AAR/JAR artifacts. `curl` and Make alone cannot resolve transitive POM closures; a Maven/Coursier-style resolver in Go is required.
- [T1: aapt2 docs, medium] `R.java` emitted by `aapt2 link --java` covers all 12 R sub-types (`layout`, `string`, `color`, `dimen`, `attr`, `style`, `drawable`, `id`, `menu`, `mipmap`, `xml`, `styleable`). The Go bridge needs constants for every sub-type, not only `R.layout`. `styleable` arrays specifically expose `int[]` constants and require special handling: an entry like `int[] styleable AppBar { 0x010100af, 0x7f040012 }` followed by per-attribute index constants `int styleable AppBar_android_layout_height 0`.
- [T3: project memory `project_minimal_java.md`, high] The standing rule is "Examples must use minimal Java; fix JNI bindings to enable Go-only implementations". Any new hand-written Java per example violates this rule. The proposed pipeline preserves it: every Java class file shipped in `classes.dex` is either downloaded as bytecode from Maven (AndroidX, Material, kotlin-stdlib) or generated mechanically from a YAML spec or a Go template — never edited by hand.
- [T3: examples/apk.mk lines 86-98, high] The current pipeline has no `javac` and no `d8` step. A `classes.dex` is conditionally zipped in only if it already exists on disk (`@if [ -f $(BUILD)/classes.dex ]; then ...`). Material 3 requires a real javac+d8 chain to compile generated `R.java` plus the abstract-adapter shims and to merge in every transitive `classes.jar` extracted from the resolved AARs.

## Non-goals (rejected explicitly)

- Hand-writing any Java per example — violates `project_minimal_java.md`.
- AppCompatActivity / FragmentActivity / ComponentActivity — would require generated Java subclasses going beyond the existing proxy-dispatch pattern; explicitly rejected by the user's pure-Go directive.
- MaterialTimePicker / MaterialDatePicker — these are `DialogFragment` subclasses; their `show()` requires a `FragmentManager`, which is only obtainable from `FragmentActivity`.
- Jetpack Compose — requires the Kotlin compiler at build time and Compose-Runtime ABI matching at runtime; contradicts the pure-Go directive.
- Vendoring AAR/JAR artifacts in the repo — bloat, no upgrade path, license problems.
- Curl-based AAR fetch in Make — cannot resolve transitive POM closures, cannot handle `<dependencyManagement>`, cannot apply nearest-wins.
- Hand-coded `R.id` integer constants — fragile across builds; aapt2 reassigns IDs whenever the resource set changes.
- Staying on raw `Canvas` rendering — does not deliver Material 3 widgets and fails the indistinguishability goal.

## Architecture overview (ASCII layered diagram)

```
Build pipeline:
  tools/cmd/aar-resolve/   resolve POM closure of (appcompat 1.7.0
                            + material 1.12.0 + recyclerview 1.3.2
                            + constraintlayout 2.1.4 + transitive
                            kotlin-stdlib)
  .aar-cache/              downloaded AARs/JARs with SHA-256 lock
  aapt2 compile --dir <res>/ -o build/compiled/<lib>/  per-AAR
                            resource compilation
  aapt2 link --java build/gen/ \
             --output-text-symbols build/R.txt \
             -I <each-classes.jar> \
             -R build/compiled/*/      base.apk + R.java + R.txt
  tools/cmd/r-consts/      reads R.txt, emits Go constants for ALL
                            12 R.* sub-types
  javac (with all classes.jar on classpath) -> d8 -> classes.dex
                            including kotlin-stdlib bytecode

Runtime (Go-only example code):
  android.app.NativeActivity (manifest entry, unchanged)
    -> libexample.so loads via NativeActivity onCreate
    -> Go init() runs -> ui.Register(run)
    -> run(vm, ctx) calls JNI:
         ctx.SetTheme(R.style.Theme_App)
         ctx.SetContentView(R.layout.activity_alarm)
    -> NativeActivity extends Activity; MaterialToolbar, FAB,
       RecyclerView all render fine on bare Activity
    -> legacy time picker: android.app.TimePickerDialog (no
       fragment manager required)
    -> repeat-day chips: ChipGroup + Chip (no fragment manager)
    -> RecyclerView.Adapter implementation: existing
       abstract_adapter.java.tmpl emits a generated subclass
       that delegates to GoAbstractDispatch -> Go callback

Bindings layer:
  spec/java/{view,recyclerview,material,timepicker,resources}.yaml
  + overlays/java/*.yaml
    -> javagen -> widget/* + view/* + content/resources/* packages
```

## Component list

| Artifact | Type | Path | Purpose |
| --- | --- | --- | --- |
| `tools/cmd/aar-resolve/main.go` | NEW Go tool | `tools/cmd/aar-resolve/main.go` | Read top-level POM coordinates, fetch from Google Maven, follow `<dependency>` graphs with nearest-wins, output JSON closure plus SHA-256 lock |
| `tools/cmd/r-consts/main.go` | NEW Go tool | `tools/cmd/r-consts/main.go` | Read `aapt2`'s `R.txt`, emit Go constants for all 12 R.* sub-types including `styleable` `int[]` arrays |
| `examples/material.mk` | NEW Make include | `examples/material.mk` | Per-example AAR fetch, `aapt2 compile`, and R.java pipeline; included by examples that opt in via `EXAMPLE_USES_MATERIAL := true` |
| `spec/java/view.yaml` plus overlay | NEW YAML | `spec/{java,overlays/java}/view.yaml` | `View`, `ViewGroup`, `LayoutInflater`, `TextView`, `ImageView`, `ViewStub`, `ViewTreeObserver` |
| `spec/java/recyclerview.yaml` plus overlay | NEW YAML | `spec/{java,overlays/java}/recyclerview.yaml` | `RecyclerView`, `RecyclerView.Adapter` (abstract-adapter generated), `RecyclerView.ViewHolder`, `LinearLayoutManager`, `GridLayoutManager`, `ItemDecoration` |
| `spec/java/material.yaml` plus overlay | NEW YAML | `spec/{java,overlays/java}/material.yaml` | `MaterialToolbar` (as plain `View`), FAB, `MaterialCardView`, `MaterialSwitch`, `Chip`, `ChipGroup`, `TextInputLayout`, `TextInputEditText`, `MaterialAlertDialogBuilder`, `Snackbar`, `BottomNavigationView` |
| `spec/java/timepicker.yaml` plus overlay | NEW YAML | `spec/{java,overlays/java}/timepicker.yaml` | `android.app.TimePickerDialog` (legacy, no fragment requirement), `android.widget.TimePicker`, `android.app.DatePickerDialog` |
| `spec/java/resources.yaml` plus overlay | NEW YAML | `spec/{java,overlays/java}/resources.yaml` | `android.content.res.Resources`, `ResourcesCompat` (color/drawable/dimen lookups by R.* int) |
| `examples/alarm_clock_app/res/` | NEW resources | `layout/`, `values/`, `drawable/` | Material 3 theme, activity layout, item layout (XML, not code) |
| `examples/alarm_clock_app/main.go` | REWRITE | `examples/alarm_clock_app/main.go` | Replace Canvas-text drawing with widget-tree calls; alarm CRUD UI |

## Cycle plan with falsifiable gate predicates

**Cycle 0 (oracle).** Build a small Java reference app under `examples/_reference_alarm_java/` (pure Java, NOT Kotlin, since the directive forbids Kotlin) via `gradle assembleDebug`. Capture a Pixel-resolution screenshot.
Gate: `adb shell screencap` PNG exists; visual inspection identifies it as a Material 3 alarm UI; SHA-256 of the PNG recorded in this doc's appendix.

**Cycle 1.** This design doc plus plan freeze.
Gate: `ls docs/architecture/material3-widget-bindings.md` succeeds; word count between 2000 and 4000; every cycle below has a runnable predicate.

**Cycle 2.** Full implementation of `tools/cmd/aar-resolve`.
Gate: `go run ./tools/cmd/aar-resolve --top androidx.appcompat:appcompat:1.7.0 --top com.google.android.material:material:1.12.0 --top androidx.recyclerview:recyclerview:1.3.2 --top androidx.constraintlayout:constraintlayout:2.1.4 --out .aar-cache/lock.json` produces a closure of 40-60 artifacts each with a SHA-256; rerun is byte-identical.

**Cycle 3.** `examples/material.mk` plus `apk.mk` extension.
Gate: a smoke example `examples/_material_smoke/` builds an APK whose `aapt2 dump resources base.apk` lists at least 1000 resource entries originating from Material; APK installs and starts on emulator; logcat shows `libexample.so` loaded.

**Cycle 4.** Full implementation of `tools/cmd/r-consts`.
Gate: `go run ./tools/cmd/r-consts --in build/R.txt --out gen/r_consts.go --pkg main` emits Go constants for all 12 R.* sub-types; `styleable` arrays surface as `[]int`; `go vet ./...` clean.

**Cycle 5.** Bindings for `View`, `ViewGroup`, `LayoutInflater`, `TextView`.
Gate: per-binding `internal/testjvm` test passes; from Go, `LayoutInflater.from(ctx).inflate(layoutID, null)` returns a non-null `View`.

**Cycle 5.5 (precursor to RecyclerView).** Failing-test-first against `tryAbstractAdapter` (proxy.go line 515) using a 3-method abstract class with primitive `int` return, `Object` return, and a generic `ViewHolder` parameter. Iterate on `templates/java/abstract_adapter.java.tmpl` (currently 24 lines) until the test passes.
Gate: `internal/testjvm/proxy_recyclerview_dispatch_test.go` passes; both primitive-int and generic-`Object` return paths exercised.

**Cycle 6.** RecyclerView, LinearLayoutManager, RecyclerView.Adapter (using verified template), RecyclerView.ViewHolder.
Gate: a smoke example builds a RecyclerView with 5 items from a Go-side adapter; `adb shell uiautomator dump` shows 5 list items.

**Cycle 7.** Material widget bindings for MaterialToolbar (as plain View), FAB, MaterialCardView, MaterialSwitch, Chip, ChipGroup, TextInputLayout, TextInputEditText, MaterialAlertDialogBuilder, Snackbar, BottomNavigationView.
Gate: every class has a passing `internal/testjvm` test; smoke example renders MaterialToolbar plus FAB on-device.

**Cycle 8.** Legacy `TimePickerDialog` and `DatePickerDialog` bindings.
Gate: smoke example shows a TimePickerDialog on FAB tap from Go; user can pick a time; the picked time arrives via `OnTimeSetListener` (interface, proxy-handled).

**Cycle 9.** Rewrite `examples/alarm_clock_app/main.go` (currently 349 lines of Canvas drawing) using the widget tree.
Gate: SSIM at least 0.95 versus the Cycle-0 Java reference screenshot for the alarm-list layout; TalkBack walks all interactive elements; day-night flip changes the theme without restarting; predictive back gesture works.

## Resolver semantics (POM closure)

`tools/cmd/aar-resolve` implements a small subset of Maven's resolver, sufficient for Google Maven AndroidX/Material artifacts. Behaviour:

- Inputs: one or more `groupId:artifactId:version` coordinates via repeated `--top` flags.
- Repository order: `https://maven.google.com`, then `https://repo1.maven.org/maven2`. Each artifact's `.pom` is fetched, parsed, and cached by coordinate.
- Dependency walk: BFS over `<dependency>` entries with `<scope>` in {`compile`, `runtime`, default}. `<scope>test</scope>` and `<optional>true</optional>` are skipped. `<dependencyManagement>` is honoured for version pinning.
- Conflict resolution: nearest-wins (the version reached at the shallowest BFS depth survives; ties broken by `--top` declaration order).
- Output: JSON file listing every resolved coordinate with download URL, SHA-256, packaging (`aar`/`jar`), and the path of every interesting member (`classes.jar`, `res/`, `AndroidManifest.xml`, `R.txt`).
- Determinism: a re-run with the same `--top` set must produce a byte-identical lock file. CI gate: diff the regenerated lock against the committed copy.

## Indistinguishability checklist (Material 3 in 2026, Go-only path)

- Edge-to-edge: `WindowCompat.setDecorFitsSystemWindows(false)` — bind from Go via `WindowCompat`.
- Predictive back: `OnBackPressedCallback` plus manifest `android:enableOnBackInvokedCallback="true"` — `OnBackInvokedDispatcher` binding from Go.
- `Theme.Material3.DayNight` plus `DynamicColors.applyToActivitiesIfAvailable` — theme is XML; color application is a one-line static call from Go.
- TalkBack: `View.setContentDescription` from Go on every interactive element.
- Material 3 ripple drawables — automatic with theme, no Go code.
- Lifecycle-aware adapters — defer; not needed for the Go-side adapter.
- Configuration changes (rotation): handle via NativeActivity's `onConfigurationChanged` Go callback; restate UI from persisted state.
- WorkManager-style resilience for AlarmManager — already handled (alarm_clock_app persists alarms in `SharedPreferences` across reinstalls).
- Min/max width breakpoints — handled at theme XML level via `-sw600dp` resource qualifiers.

## Trade-offs explicitly accepted (because Go-only)

- No MaterialTimePicker (Material 3 fragment-based picker) — falling back to `android.app.TimePickerDialog`. Visually similar but slightly older bottom-sheet versus the Material 3 spec dialog.
- No `setSupportActionBar` — MaterialToolbar renders as a regular view; menu items appear via FAB or icon-button row instead of an action-bar overflow menu.
- No fragments at all (no `FragmentManager` available on `NativeActivity`) — bottom sheets and navigation drawer fragments rejected; substitute with custom views or non-fragment dialog alternatives.
- No data-binding library — Go-side state is pushed to widgets via direct setter calls.
- Inflation overhead: every layout inflation crosses JNI; for an alarm list with under 100 items this is fine.

## References

Files cited in this document (line numbers verified in this session):

- `examples/apk.mk` line 87 — current `aapt2 link` invocation, no resource compilation [T3, high].
- `examples/alarm_clock_app/main.go` (349 lines) — current Canvas demo, target of Cycle 9 rewrite [T3, high].
- `proxy.go` line 483 (call site), line 515 (definition) — `tryAbstractAdapter` [T3, high].
- `templates/java/abstract_adapter.java.tmpl` (24 lines) — abstract-adapter shim emitter [T3, high].
- `tools/cmd/specgen/main.go` and `tools/cmd/javagen/main.go` — generators for spec-driven bindings [T3, high].
- `internal/testjvm/testdata/center/dx/jni/internal/GoAbstractDispatch.java` (12 lines) — accepted Java glue [T3, high].
- `internal/testjvm/testdata/center/dx/jni/internal/GoInvocationHandler.java` (27 lines) — accepted Java glue [T3, high].
- Project memory `project_minimal_java.md` — "Examples must use minimal Java; fix JNI bindings to enable Go-only implementations" [T3, high].

External documentation (T1, fetched in cycle-1 exploration; re-cite when consumed in implementation cycles):

- developer.android.com/tools/aapt2 — `aapt2 link --java`, `-I`, `-R`, `--output-text-symbols` semantics.
- github.com/material-components/material-components-android `getting-started.md` — `Theme.Material3.*` style names and AndroidX/Material transitive dependency on `kotlin-stdlib`.
- docs.oracle.com `java.lang.reflect.Proxy` — interfaces only; abstract-class fallback handled by `tryAbstractAdapter`.

## Next steps (numbered)

1. Build a small Java reference alarm app under `examples/_reference_alarm_java/` (one-off Gradle project, NOT a checked-in build artifact, only the source plus a captured screenshot for SSIM oracle in cycles 7 and 9). Capture a Pixel screenshot.
2. Implement `tools/cmd/aar-resolve`. Mode: read POMs, follow `<dependency>` edges, apply nearest-wins, output JSON closure plus SHA-256 lock file.
3. Implement `examples/material.mk` extending the `apk.mk` pipeline behind an `EXAMPLE_USES_MATERIAL := true` switch — preserve backwards compatibility for the existing 53 examples.
4. Implement `tools/cmd/r-consts` emitting Go constants for all 12 R.* sub-types including `styleable` arrays.
5. Run the cycle-5.5 abstract-adapter dispatch verification BEFORE cycle 6 RecyclerView work — primitive-`int` return and generic `Object` return paths must pass first.
6. Generate widget bindings cycle by cycle; each cycle gated by `internal/testjvm` coverage.
7. Rewrite `examples/alarm_clock_app/main.go` against the new widget surface; verify SSIM at least 0.95 versus the Cycle-0 Java reference screenshot.
