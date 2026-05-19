package specgen

import (
	"strings"
	"testing"
)

func TestInferGoType(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fullClass string
		goPkg     string
		want      string
	}{
		{
			name:      "simple class",
			fullClass: "android.app.Activity",
			goPkg:     "app",
			want:      "Activity",
		},
		{
			name:      "strip package prefix",
			fullClass: "android.app.alarm.AlarmManager",
			goPkg:     "alarm",
			want:      "Manager",
		},
		{
			name:      "no strip when no prefix match",
			fullClass: "android.app.SearchManager",
			goPkg:     "app",
			want:      "SearchManager",
		},
		{
			name:      "inner class",
			fullClass: "android.app.SearchManager$OnCancelListener",
			goPkg:     "app",
			want:      "SearchManagerOnCancelListener",
		},
		{
			name:      "case-sensitive prefix no strip",
			fullClass: "android.app.appsearch.AppSearchManager",
			goPkg:     "appsearch",
			want:      "AppSearchManager",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := inferGoType(tc.fullClass, tc.goPkg)
			if got != tc.want {
				t.Errorf("inferGoType(%q, %q) = %q, want %q", tc.fullClass, tc.goPkg, got, tc.want)
			}
		})
	}
}

func TestInferPackageMapping(t *testing.T) {
	module := "github.com/AndroidGoLab/jni"
	for _, tc := range []struct {
		name      string
		className string
		wantPkg   string
		wantPath  string
	}{
		{
			name:      "android.app direct",
			className: "android.app.Activity",
			wantPkg:   "app",
			wantPath:  module + "/app",
		},
		{
			name:      "android.app.appsearch subpackage",
			className: "android.app.appsearch.AppSearchManager",
			wantPkg:   "appsearch",
			wantPath:  module + "/app/appsearch",
		},
		{
			name:      "android.credentials",
			className: "android.credentials.CredentialManager",
			wantPkg:   "credentials",
			wantPath:  module + "/credentials",
		},
		{
			name:      "android.service.credentials separate from android.credentials",
			className: "android.service.credentials.ClearCredentialStateRequest",
			wantPkg:   "credentials",
			wantPath:  module + "/service/credentials",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := inferPackageMapping(tc.className, module)
			if got.Package != tc.wantPkg {
				t.Errorf("inferPackageMapping(%q).Package = %q, want %q", tc.className, got.Package, tc.wantPkg)
			}
			if got.GoImport != tc.wantPath {
				t.Errorf("inferPackageMapping(%q).GoImport = %q, want %q", tc.className, got.GoImport, tc.wantPath)
			}
		})
	}
}

func TestChooseBestConstructor(t *testing.T) {
	t.Run("prefers Context param", func(t *testing.T) {
		ctors := []JavapConstructor{
			{Params: nil},
			{Params: []JavapParam{{JavaType: "android.content.Context"}}},
			{Params: []JavapParam{{JavaType: "int"}, {JavaType: "int"}}},
		}
		best := chooseBestConstructor(ctors)
		if len(best.Params) != 1 || best.Params[0].JavaType != "android.content.Context" {
			t.Errorf("expected Context constructor, got %+v", best)
		}
	})

	t.Run("falls back to no-arg", func(t *testing.T) {
		ctors := []JavapConstructor{
			{Params: []JavapParam{{JavaType: "int"}, {JavaType: "int"}}},
			{Params: nil},
		}
		best := chooseBestConstructor(ctors)
		if len(best.Params) != 0 {
			t.Errorf("expected no-arg constructor, got %+v", best)
		}
	})

	t.Run("falls back to first", func(t *testing.T) {
		ctors := []JavapConstructor{
			{Params: []JavapParam{{JavaType: "int"}}},
			{Params: []JavapParam{{JavaType: "int"}, {JavaType: "int"}}},
		}
		best := chooseBestConstructor(ctors)
		if len(best.Params) != 1 {
			t.Errorf("expected first constructor (1 param), got %+v", best)
		}
	})
}

func TestClassFromJavap_ConstructorObtain(t *testing.T) {
	// Reset AndroidServiceName so the test doesn't depend on runtime state.
	origSvc := AndroidServiceName
	AndroidServiceName = nil
	defer func() { AndroidServiceName = origSvc }()

	t.Run("concrete class with constructors gets obtain=constructor", func(t *testing.T) {
		jc := &JavapClass{
			FullName: "android.media.MediaRecorder",
			Constructors: []JavapConstructor{
				{Params: nil},
				{Params: []JavapParam{{JavaType: "android.content.Context"}}},
			},
			Methods: []JavapMethod{
				{Name: "start", ReturnType: "void"},
			},
		}
		cls := classFromJavap(jc, "media")
		if cls.Obtain != "constructor" {
			t.Errorf("Obtain = %q, want %q", cls.Obtain, "constructor")
		}
		// Should pick the Context constructor.
		if len(cls.ConstructorParams) != 1 {
			t.Fatalf("len(ConstructorParams) = %d, want 1", len(cls.ConstructorParams))
		}
		if cls.ConstructorParams[0].JavaType != "android.content.Context" {
			t.Errorf("ConstructorParams[0].JavaType = %q, want %q",
				cls.ConstructorParams[0].JavaType, "android.content.Context")
		}
	})

	t.Run("abstract class does not get obtain=constructor", func(t *testing.T) {
		jc := &JavapClass{
			FullName:   "android.app.AbstractThing",
			IsAbstract: true,
			Constructors: []JavapConstructor{
				{Params: nil},
			},
		}
		cls := classFromJavap(jc, "app")
		if cls.Obtain != "" {
			t.Errorf("Obtain = %q, want empty for abstract class", cls.Obtain)
		}
	})

	t.Run("interface does not get obtain=constructor", func(t *testing.T) {
		jc := &JavapClass{
			FullName:    "android.app.SomeInterface",
			IsInterface: true,
		}
		cls := classFromJavap(jc, "app")
		if cls.Obtain != "" {
			t.Errorf("Obtain = %q, want empty for interface", cls.Obtain)
		}
	})

	t.Run("system service class keeps obtain=system_service", func(t *testing.T) {
		AndroidServiceName = map[string]string{
			"android.app.AlarmManager": "alarm",
		}
		jc := &JavapClass{
			FullName: "android.app.AlarmManager",
			Constructors: []JavapConstructor{
				{Params: nil},
			},
		}
		cls := classFromJavap(jc, "alarm")
		if cls.Obtain != "system_service" {
			t.Errorf("Obtain = %q, want %q", cls.Obtain, "system_service")
		}
		if cls.ServiceName != "alarm" {
			t.Errorf("ServiceName = %q, want %q", cls.ServiceName, "alarm")
		}
	})
}

func TestAbstractCallbackFromJavap(t *testing.T) {
	t.Run("abstract class with methods generates callback", func(t *testing.T) {
		jc := &JavapClass{
			FullName:     "android.bluetooth.le.ScanCallback",
			IsAbstract:   true,
			Constructors: []JavapConstructor{{Params: nil}},
			Methods: []JavapMethod{
				{Name: "onScanFailed", ReturnType: "void", Params: []JavapParam{{JavaType: "int"}}},
				{Name: "onScanResult", ReturnType: "void", Params: []JavapParam{{JavaType: "int"}, {JavaType: "android.bluetooth.le.ScanResult"}}},
			},
		}
		acb := abstractCallbackFromJavap(jc, "le")
		if acb == nil {
			t.Fatal("expected non-nil abstract callback")
		}
		if acb.JavaClass != "android.bluetooth.le.ScanCallback" {
			t.Errorf("JavaClass = %q, want %q", acb.JavaClass, "android.bluetooth.le.ScanCallback")
		}
		if acb.GoType != "ScanCallbackCallback" {
			t.Errorf("GoType = %q, want %q", acb.GoType, "ScanCallbackCallback")
		}
		if len(acb.Methods) != 2 {
			t.Fatalf("len(Methods) = %d, want 2", len(acb.Methods))
		}
		if acb.Methods[0].JavaMethod != "onScanFailed" {
			t.Errorf("Methods[0].JavaMethod = %q, want %q", acb.Methods[0].JavaMethod, "onScanFailed")
		}
		if len(acb.Methods[1].Params) != 2 {
			t.Errorf("Methods[1] params count = %d, want 2", len(acb.Methods[1].Params))
		}
	})

	t.Run("concrete class returns nil", func(t *testing.T) {
		jc := &JavapClass{
			FullName: "android.app.Activity",
			Methods:  []JavapMethod{{Name: "onCreate", ReturnType: "void"}},
		}
		if acb := abstractCallbackFromJavap(jc, "app"); acb != nil {
			t.Errorf("expected nil for concrete class, got %+v", acb)
		}
	})

	t.Run("interface returns nil", func(t *testing.T) {
		jc := &JavapClass{
			FullName:    "android.location.LocationListener",
			IsInterface: true,
			Methods:     []JavapMethod{{Name: "onLocationChanged", ReturnType: "void"}},
		}
		if acb := abstractCallbackFromJavap(jc, "location"); acb != nil {
			t.Errorf("expected nil for interface, got %+v", acb)
		}
	})

	t.Run("static methods are excluded", func(t *testing.T) {
		jc := &JavapClass{
			FullName:     "android.app.AbstractThing",
			IsAbstract:   true,
			Constructors: []JavapConstructor{{Params: nil}},
			Methods: []JavapMethod{
				{Name: "doWork", ReturnType: "void"},
				{Name: "getInstance", ReturnType: "android.app.AbstractThing", IsStatic: true},
			},
		}
		acb := abstractCallbackFromJavap(jc, "app")
		if acb == nil {
			t.Fatal("expected non-nil abstract callback")
		}
		if len(acb.Methods) != 1 {
			t.Fatalf("expected 1 method (static excluded), got %d", len(acb.Methods))
		}
		if acb.Methods[0].JavaMethod != "doWork" {
			t.Errorf("Methods[0].JavaMethod = %q, want %q", acb.Methods[0].JavaMethod, "doWork")
		}
	})

	t.Run("abstract class with no methods returns nil", func(t *testing.T) {
		jc := &JavapClass{
			FullName:   "android.app.EmptyAbstract",
			IsAbstract: true,
		}
		if acb := abstractCallbackFromJavap(jc, "app"); acb != nil {
			t.Errorf("expected nil for abstract class with no methods, got %+v", acb)
		}
	})

	t.Run("final methods are excluded", func(t *testing.T) {
		// Final methods cannot be overridden, so the adapter shim must
		// not emit @Override for them. Dual-sided check: abstract methods
		// remain, final ones disappear.
		jc := &JavapClass{
			FullName:     "androidx.recyclerview.widget.RecyclerView$Adapter",
			IsAbstract:   true,
			Constructors: []JavapConstructor{{Params: nil}},
			TypeParams: map[string]string{
				"VH": "androidx.recyclerview.widget.RecyclerView$ViewHolder",
			},
			Methods: []JavapMethod{
				{
					Name:       "createViewHolder",
					ReturnType: "VH",
					IsFinal:    true,
					Params:     []JavapParam{{JavaType: "android.view.ViewGroup"}, {JavaType: "int"}},
				},
				{
					Name:       "bindViewHolder",
					ReturnType: "void",
					IsFinal:    true,
					Params:     []JavapParam{{JavaType: "VH"}, {JavaType: "int"}},
				},
				{
					Name:       "onCreateViewHolder",
					ReturnType: "VH",
					IsAbstract: true,
					Params:     []JavapParam{{JavaType: "android.view.ViewGroup"}, {JavaType: "int"}},
				},
				{
					Name:       "onBindViewHolder",
					ReturnType: "void",
					IsAbstract: true,
					Params:     []JavapParam{{JavaType: "VH"}, {JavaType: "int"}},
				},
			},
		}
		acb := abstractCallbackFromJavap(jc, "widget")
		if acb == nil {
			t.Fatal("expected non-nil acb")
		}
		gotNames := make(map[string]struct{}, len(acb.Methods))
		for _, m := range acb.Methods {
			gotNames[m.JavaMethod] = struct{}{}
		}
		if _, ok := gotNames["createViewHolder"]; ok {
			t.Error("final method createViewHolder should be excluded")
		}
		if _, ok := gotNames["bindViewHolder"]; ok {
			t.Error("final method bindViewHolder should be excluded")
		}
		if _, ok := gotNames["onCreateViewHolder"]; !ok {
			t.Error("abstract onCreateViewHolder must remain")
		}
		if _, ok := gotNames["onBindViewHolder"]; !ok {
			t.Error("abstract onBindViewHolder must remain")
		}
	})

	t.Run("abstract class without supported constructor is skipped", func(t *testing.T) {
		jc := &JavapClass{
			FullName:   "androidx.dynamicanimation.animation.DynamicAnimation",
			IsAbstract: true,
			Constructors: []JavapConstructor{
				{Params: []JavapParam{{JavaType: "androidx.dynamicanimation.animation.FloatValueHolder"}}},
			},
			Methods: []JavapMethod{
				{Name: "setStartValue", ReturnType: "void", Params: []JavapParam{{JavaType: "float"}}},
			},
		}
		if acb := abstractCallbackFromJavap(jc, "animation"); acb != nil {
			t.Errorf("expected unsupported abstract class to be skipped, got %+v", acb)
		}
		eligibility := abstractCallbackEligibilityFromJavap(jc)
		if eligibility.JavaClass != jc.FullName {
			t.Errorf("eligibility.JavaClass = %q, want %q", eligibility.JavaClass, jc.FullName)
		}
		if eligibility.Generated {
			t.Fatalf("eligibility.Generated = true, want false")
		}
		if eligibility.Reason != "unsupported_constructor" {
			t.Errorf("eligibility.Reason = %q, want unsupported_constructor", eligibility.Reason)
		}
	})

	t.Run("recyclerview viewholder special constructor remains supported", func(t *testing.T) {
		jc := &JavapClass{
			FullName:   "androidx.recyclerview.widget.RecyclerView$ViewHolder",
			IsAbstract: true,
			Constructors: []JavapConstructor{
				{Params: []JavapParam{{JavaType: "android.view.View"}}},
			},
			Methods: []JavapMethod{
				{Name: "toString", ReturnType: "java.lang.String"},
			},
		}
		acb := abstractCallbackFromJavap(jc, "widget")
		if acb == nil {
			t.Fatal("expected RecyclerView ViewHolder shim to remain supported")
		}
		if acb.JavaClass != "androidx.recyclerview.widget.RecyclerView$ViewHolder" {
			t.Errorf("JavaClass = %q, want RecyclerView ViewHolder", acb.JavaClass)
		}
	})

	t.Run("legacy deny-list class with supported structure is generated", func(t *testing.T) {
		jc := &JavapClass{
			FullName:     "android.icu.util.TimeZone",
			IsAbstract:   true,
			Constructors: []JavapConstructor{{Params: nil}},
			Methods: []JavapMethod{
				{
					Name:       "getOffset",
					ReturnType: "int",
					Params:     []JavapParam{{JavaType: "long"}},
				},
			},
		}
		acb := abstractCallbackFromJavap(jc, "util")
		if acb == nil {
			t.Fatal("expected structurally supported abstract callback to be generated")
		}
		if acb.JavaClass != jc.FullName {
			t.Errorf("JavaClass = %q, want %q", acb.JavaClass, jc.FullName)
		}
		eligibility := abstractCallbackEligibilityFromJavap(jc)
		if !eligibility.Generated {
			t.Fatalf("eligibility.Generated = false, want true: %#v", eligibility)
		}
		if eligibility.Reason != "supported_no_arg_constructor" {
			t.Errorf("eligibility.Reason = %q, want supported_no_arg_constructor", eligibility.Reason)
		}
	})

	t.Run("bridge and synthetic methods are excluded", func(t *testing.T) {
		jc := &JavapClass{
			FullName:     "android.animation.Animator",
			IsAbstract:   true,
			Constructors: []JavapConstructor{{Params: nil}},
			Methods: []JavapMethod{
				{Name: "clone", ReturnType: "java.lang.Object", IsBridge: true, IsSynthetic: true},
				{Name: "start", ReturnType: "void"},
			},
		}
		acb := abstractCallbackFromJavap(jc, "animation")
		if acb == nil {
			t.Fatal("expected callback with non-synthetic method")
		}
		if len(acb.Methods) != 1 {
			t.Fatalf("len(Methods) = %d, want 1", len(acb.Methods))
		}
		if acb.Methods[0].JavaMethod != "start" {
			t.Errorf("method = %q, want start", acb.Methods[0].JavaMethod)
		}
	})

	t.Run("no renderable methods are reason-coded", func(t *testing.T) {
		jc := &JavapClass{
			FullName:     "com.example.OnlyBridge",
			IsAbstract:   true,
			Constructors: []JavapConstructor{{Params: nil}},
			Methods: []JavapMethod{
				{Name: "compareTo", ReturnType: "int", IsBridge: true, IsSynthetic: true, Params: []JavapParam{{JavaType: "java.lang.Object"}}},
			},
		}
		if acb := abstractCallbackFromJavap(jc, "example"); acb != nil {
			t.Fatalf("expected no callback when all methods are bridge/synthetic, got %#v", acb)
		}
		eligibility := abstractCallbackEligibilityFromJavap(jc)
		if eligibility.Generated {
			t.Fatalf("eligibility.Generated = true, want false")
		}
		if eligibility.Reason != "no_renderable_methods" {
			t.Errorf("eligibility.Reason = %q, want no_renderable_methods", eligibility.Reason)
		}
	})

	t.Run("implemented interfaces are reason-coded", func(t *testing.T) {
		jc := &JavapClass{
			FullName:     "com.example.ParcelableBase",
			IsAbstract:   true,
			Constructors: []JavapConstructor{{Params: nil, IsPublic: true}},
			Implements:   []string{"android.os.Parcelable"},
			Methods: []JavapMethod{
				{Name: "describeContents", ReturnType: "int", IsPublic: true},
			},
		}
		eligibility := abstractCallbackEligibilityFromJavap(jc)
		if eligibility.Generated {
			t.Fatalf("eligibility.Generated = true, want false")
		}
		if eligibility.Reason != "implements_interface" {
			t.Errorf("eligibility.Reason = %q, want implements_interface", eligibility.Reason)
		}
	})

	t.Run("unsupported abstract method signatures are reason-coded", func(t *testing.T) {
		jc := &JavapClass{
			FullName:     "com.example.GenericAbstract",
			IsAbstract:   true,
			Constructors: []JavapConstructor{{Params: nil, IsPublic: true}},
			Methods: []JavapMethod{
				{
					Name:       "apply",
					ReturnType: "void",
					IsAbstract: true,
					IsPublic:   true,
					Params:     []JavapParam{{JavaType: "java.util.List<java.lang.String>"}},
				},
			},
		}
		eligibility := abstractCallbackEligibilityFromJavap(jc)
		if eligibility.Generated {
			t.Fatalf("eligibility.Generated = true, want false")
		}
		if eligibility.Reason != "unsupported_abstract_method_signature" {
			t.Errorf("eligibility.Reason = %q, want unsupported_abstract_method_signature", eligibility.Reason)
		}
	})

	t.Run("non object superclass is reason-coded", func(t *testing.T) {
		jc := &JavapClass{
			FullName:     "com.example.AbstractChild",
			SuperClass:   "com.example.AbstractParent",
			IsAbstract:   true,
			Constructors: []JavapConstructor{{Params: nil, IsPublic: true}},
			Methods:      []JavapMethod{{Name: "onEvent", ReturnType: "void", IsPublic: true}},
		}
		eligibility := abstractCallbackEligibilityFromJavap(jc)
		if eligibility.Generated {
			t.Fatalf("eligibility.Generated = true, want false")
		}
		if eligibility.Reason != "non_object_superclass" {
			t.Errorf("eligibility.Reason = %q, want non_object_superclass", eligibility.Reason)
		}
	})
}

// TestClassFromJavap_OverloadDisambiguator_NoCollision verifies that when
// two Java methods differ only by case (e.g. Dimension.Suggested static and
// Dimension.suggested instance — both PascalCase to "Suggested"), the
// generated Go names are unique. Regression test for cycle 7: previously
// the disambiguator counted by Java method name, so case-only collisions
// produced two identical Go names (e.g. Suggested1 and Suggested1).
func TestClassFromJavap_OverloadDisambiguator_NoCollision(t *testing.T) {
	origSvc := AndroidServiceName
	AndroidServiceName = nil
	defer func() { AndroidServiceName = origSvc }()

	jc := &JavapClass{
		FullName: "androidx.constraintlayout.solver.state.Dimension",
		Methods: []JavapMethod{
			// Static capital-S methods (case differs from instance methods).
			{Name: "Suggested", IsStatic: true, ReturnType: "androidx.constraintlayout.solver.state.Dimension",
				Params: []JavapParam{{JavaType: "int"}}},
			{Name: "Suggested", IsStatic: true, ReturnType: "androidx.constraintlayout.solver.state.Dimension",
				Params: []JavapParam{{JavaType: "java.lang.Object"}}},
			{Name: "Fixed", IsStatic: true, ReturnType: "androidx.constraintlayout.solver.state.Dimension",
				Params: []JavapParam{{JavaType: "int"}}},
			{Name: "Fixed", IsStatic: true, ReturnType: "androidx.constraintlayout.solver.state.Dimension",
				Params: []JavapParam{{JavaType: "java.lang.Object"}}},
			// Instance lowercase methods.
			{Name: "suggested", ReturnType: "androidx.constraintlayout.solver.state.Dimension",
				Params: []JavapParam{{JavaType: "int"}}},
			{Name: "suggested", ReturnType: "androidx.constraintlayout.solver.state.Dimension",
				Params: []JavapParam{{JavaType: "java.lang.Object"}}},
			{Name: "fixed", ReturnType: "androidx.constraintlayout.solver.state.Dimension",
				Params: []JavapParam{{JavaType: "int"}}},
			{Name: "fixed", ReturnType: "androidx.constraintlayout.solver.state.Dimension",
				Params: []JavapParam{{JavaType: "java.lang.Object"}}},
		},
	}
	cls := classFromJavap(jc, "state")

	seen := make(map[string]int)
	for _, m := range cls.Methods {
		seen[m.GoName]++
	}
	for _, m := range cls.StaticMethods {
		seen[m.GoName]++
	}
	for name, count := range seen {
		if count > 1 {
			t.Errorf("Go name %q appears %d times across methods+static_methods (want 1)", name, count)
		}
	}
	// Sanity-check totals: 8 input methods → 8 unique Go names.
	if total := len(seen); total != 8 {
		t.Errorf("got %d unique Go names, want 8 (Methods=%d, StaticMethods=%d)",
			total, len(cls.Methods), len(cls.StaticMethods))
	}
}

// TestClassFromJavap_OverloadDifferentParamCount verifies that overloads
// distinguished by parameter count still produce distinct Go names. The
// disambiguator uses paramCount as the primary suffix and an occurrence
// index as the secondary suffix; both must combine to a unique identifier.
func TestClassFromJavap_OverloadDifferentParamCount(t *testing.T) {
	origSvc := AndroidServiceName
	AndroidServiceName = nil
	defer func() { AndroidServiceName = origSvc }()

	jc := &JavapClass{
		FullName: "com.example.Foo",
		Methods: []JavapMethod{
			{Name: "doIt", ReturnType: "void"},
			{Name: "doIt", ReturnType: "void", Params: []JavapParam{{JavaType: "int"}}},
			{Name: "doIt", ReturnType: "void", Params: []JavapParam{{JavaType: "int"}, {JavaType: "int"}}},
		},
	}
	cls := classFromJavap(jc, "foo")
	seen := make(map[string]int)
	for _, m := range cls.Methods {
		seen[m.GoName]++
	}
	if len(cls.Methods) != 3 {
		t.Fatalf("expected 3 methods, got %d", len(cls.Methods))
	}
	for name, count := range seen {
		if count > 1 {
			t.Errorf("Go name %q appeared %d times, want 1", name, count)
		}
	}
}

func TestClassFromJavap_ExcludesBridgeSyntheticMethods(t *testing.T) {
	jc := &JavapClass{
		FullName: "androidx.activity.OnBackPressedDispatcher",
		Methods: []JavapMethod{
			{Name: "addCallback", ReturnType: "void"},
			{Name: "addCallback$default", ReturnType: "void", IsSynthetic: true},
			{Name: "compareTo", ReturnType: "int", IsBridge: true, IsSynthetic: true, Params: []JavapParam{{JavaType: "java.lang.Object"}}},
		},
	}
	cls := classFromJavap(jc, "activity")
	if len(cls.Methods) != 1 {
		t.Fatalf("len(Methods) = %d, want 1: %#v", len(cls.Methods), cls.Methods)
	}
	if cls.Methods[0].JavaMethod != "addCallback" {
		t.Errorf("JavaMethod = %q, want addCallback", cls.Methods[0].JavaMethod)
	}
	if strings.Contains(cls.Methods[0].GoName, "$") {
		t.Errorf("GoName contains $: %q", cls.Methods[0].GoName)
	}
}

func TestJavaMethodToGoName_SanitizesDollarSegments(t *testing.T) {
	for _, tc := range []struct {
		javaMethod string
		want       string
	}{
		{
			javaMethod: "updateBackInvokedCallbackState$activity_release",
			want:       "UpdateBackInvokedCallbackStateActivityRelease",
		},
		{
			javaMethod: "access$getOnBackPressedCallbacks$p",
			want:       "AccessGetOnBackPressedCallbacksP",
		},
	} {
		t.Run(tc.javaMethod, func(t *testing.T) {
			got := javaMethodToGoName(tc.javaMethod)
			if got != tc.want {
				t.Errorf("javaMethodToGoName(%q) = %q, want %q", tc.javaMethod, got, tc.want)
			}
			if strings.Contains(got, "$") {
				t.Errorf("GoName contains $: %q", got)
			}
		})
	}
}

// TestAbstractCallbackFromJavap_ErasesTypeVariables verifies that a
// type-variable reference in a method signature (e.g. `VH` from
// `class Foo<VH extends Bar>`) is replaced with its declared upper bound
// when materialised into the spec. This closes the RecyclerView$Adapter
// generator gap where VH was previously emitted literally and javac
// rejected it.
func TestAbstractCallbackFromJavap_ErasesTypeVariables(t *testing.T) {
	t.Run("substitutes single VH bound", func(t *testing.T) {
		jc := &JavapClass{
			FullName:     "androidx.recyclerview.widget.RecyclerView$Adapter",
			IsAbstract:   true,
			Constructors: []JavapConstructor{{Params: nil}},
			TypeParams: map[string]string{
				"VH": "androidx.recyclerview.widget.RecyclerView$ViewHolder",
			},
			Methods: []JavapMethod{
				{
					Name:       "onCreateViewHolder",
					ReturnType: "VH",
					Params:     []JavapParam{{JavaType: "android.view.ViewGroup"}, {JavaType: "int"}},
				},
				{
					Name:       "onBindViewHolder",
					ReturnType: "void",
					Params:     []JavapParam{{JavaType: "VH"}, {JavaType: "int"}},
				},
			},
		}
		acb := abstractCallbackFromJavap(jc, "widget")
		if acb == nil {
			t.Fatal("abstractCallbackFromJavap returned nil")
		}
		if len(acb.Methods) != 2 {
			t.Fatalf("len(Methods) = %d, want 2", len(acb.Methods))
		}
		if acb.Methods[0].Returns != "androidx.recyclerview.widget.RecyclerView$ViewHolder" {
			t.Errorf("onCreateViewHolder Returns = %q, want erased VH bound", acb.Methods[0].Returns)
		}
		if acb.Methods[1].Params[0] != "androidx.recyclerview.widget.RecyclerView$ViewHolder" {
			t.Errorf("onBindViewHolder Params[0] = %q, want erased VH bound", acb.Methods[1].Params[0])
		}
	})

	t.Run("dual: literal VH never leaks when typeparams known", func(t *testing.T) {
		// Negative side of the dual-sided check: the spec must NOT contain
		// the bare type variable name once erasure runs.
		jc := &JavapClass{
			FullName:     "com.example.Foo",
			IsAbstract:   true,
			Constructors: []JavapConstructor{{Params: nil}},
			TypeParams:   map[string]string{"VH": "com.example.Foo$Bar"},
			Methods:      []JavapMethod{{Name: "f", ReturnType: "VH", Params: []JavapParam{{JavaType: "VH"}}}},
		}
		acb := abstractCallbackFromJavap(jc, "example")
		if acb == nil || len(acb.Methods) == 0 {
			t.Fatal("expected method to survive (VH is known)")
		}
		if acb.Methods[0].Returns == "VH" {
			t.Error("Returns leaked literal VH")
		}
		for _, p := range acb.Methods[0].Params {
			if p == "VH" {
				t.Error("Param leaked literal VH")
			}
		}
	})

	t.Run("single-letter type variable not declared: method dropped", func(t *testing.T) {
		// When the existing `isTypeVariable` heuristic detects a bare
		// single-letter type (e.g. `T`) but it is not in TypeParams, the
		// type variable is unresolved and the method is correctly
		// excluded — same behaviour as before this fix.
		jc := &JavapClass{
			FullName:     "com.example.Foo",
			IsAbstract:   true,
			Constructors: []JavapConstructor{{Params: nil}},
			TypeParams:   nil,
			Methods:      []JavapMethod{{Name: "f", ReturnType: "T"}},
		}
		acb := abstractCallbackFromJavap(jc, "example")
		if acb != nil {
			t.Errorf("expected nil acb (method dropped), got %+v", acb)
		}
	})

	t.Run("array of type variable substituted", func(t *testing.T) {
		jc := &JavapClass{
			FullName:     "com.example.Foo",
			IsAbstract:   true,
			Constructors: []JavapConstructor{{Params: nil}},
			TypeParams:   map[string]string{"T": "java.lang.Object"},
			Methods:      []JavapMethod{{Name: "g", ReturnType: "void", Params: []JavapParam{{JavaType: "T[]"}}}},
		}
		acb := abstractCallbackFromJavap(jc, "example")
		if acb == nil || len(acb.Methods) != 1 {
			t.Fatalf("acb=%+v", acb)
		}
		if acb.Methods[0].Params[0] != "java.lang.Object[]" {
			t.Errorf("Params[0] = %q, want erased array", acb.Methods[0].Params[0])
		}
	})

	t.Run("recursive parameterized bounds are erased to raw source types", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			className  string
			typeParam  string
			bound      string
			returnType string
			wantReturn string
		}{
			{
				name:       "dynamic animation",
				className:  "androidx.dynamicanimation.animation.DynamicAnimation",
				typeParam:  "T",
				bound:      "androidx.dynamicanimation.animation.DynamicAnimation<T>",
				returnType: "androidx.dynamicanimation.animation.DynamicAnimation<T>",
				wantReturn: "androidx.dynamicanimation.animation.DynamicAnimation",
			},
			{
				name:       "snackbar base transient bottom bar",
				className:  "com.google.android.material.snackbar.BaseTransientBottomBar",
				typeParam:  "B",
				bound:      "com.google.android.material.snackbar.BaseTransientBottomBar<B>",
				returnType: "B",
				wantReturn: "com.google.android.material.snackbar.BaseTransientBottomBar",
			},
			{
				name:       "text classifier event builder",
				className:  "android.view.textclassifier.TextClassifierEvent$Builder",
				typeParam:  "T",
				bound:      "android.view.textclassifier.TextClassifierEvent$Builder<T>",
				returnType: "android.view.textclassifier.TextClassifierEvent$Builder<T>",
				wantReturn: "android.view.textclassifier.TextClassifierEvent$Builder",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				jc := &JavapClass{
					FullName:       tc.className,
					IsAbstract:     true,
					Constructors:   []JavapConstructor{{Params: nil}},
					TypeParams:     map[string]string{tc.typeParam: tc.bound},
					TypeParamOrder: []string{tc.typeParam},
					Methods:        []JavapMethod{{Name: "self", ReturnType: tc.returnType}},
				}
				acb := abstractCallbackFromJavap(jc, "example")
				if acb == nil || len(acb.Methods) != 1 {
					t.Fatalf("acb=%+v, want one surviving erased method", acb)
				}
				if len(acb.TypeParamBounds) != 0 {
					t.Fatalf("TypeParamBounds = %#v, want no unsafe recursive bounds", acb.TypeParamBounds)
				}
				if acb.Methods[0].Returns != tc.wantReturn {
					t.Errorf("Returns = %q, want %q", acb.Methods[0].Returns, tc.wantReturn)
				}
				if acb.Methods[0].Returns == tc.typeParam {
					t.Errorf("Returns leaked bare type variable %q", tc.typeParam)
				}
				if strings.Contains(acb.Methods[0].Returns, "<"+tc.typeParam+">") {
					t.Errorf("Returns leaked nested type variable in %q", acb.Methods[0].Returns)
				}
			})
		}
	})
}

// TestParseTypeParams verifies the `<...>` block parser used by the
// class-header regex.
func TestParseTypeParams(t *testing.T) {
	for _, tc := range []struct {
		name      string
		in        string
		want      map[string]string
		wantOrder []string
	}{
		{"empty", "", nil, nil},
		{
			name:      "single bounded",
			in:        "VH extends androidx.recyclerview.widget.RecyclerView$ViewHolder",
			want:      map[string]string{"VH": "androidx.recyclerview.widget.RecyclerView$ViewHolder"},
			wantOrder: []string{"VH"},
		},
		{
			name:      "unbounded defaults to Object",
			in:        "T",
			want:      map[string]string{"T": "java.lang.Object"},
			wantOrder: []string{"T"},
		},
		{
			name: "multiple params preserves order",
			in:   "K extends java.lang.Comparable, V",
			want: map[string]string{
				"K": "java.lang.Comparable",
				"V": "java.lang.Object",
			},
			wantOrder: []string{"K", "V"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, gotOrder := parseTypeParams(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len(got)=%d want=%d (got=%v)", len(got), len(tc.want), got)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("[%q] = %q, want %q", k, got[k], v)
				}
			}
			if len(gotOrder) != len(tc.wantOrder) {
				t.Fatalf("order len=%d want=%d (got=%v)", len(gotOrder), len(tc.wantOrder), gotOrder)
			}
			for i, k := range tc.wantOrder {
				if gotOrder[i] != k {
					t.Errorf("order[%d] = %q, want %q", i, gotOrder[i], k)
				}
			}
		})
	}
}

// TestParseJavap_ClassWithTypeParams verifies that a class header with a
// generic-parameter block is parsed into JavapClass.TypeParams.
func TestParseJavap_ClassWithTypeParams(t *testing.T) {
	output := `Compiled from "RecyclerView.java"
public abstract class androidx.recyclerview.widget.RecyclerView$Adapter<VH extends androidx.recyclerview.widget.RecyclerView$ViewHolder> extends java.lang.Object
  minor version: 0
{
  public VH onCreateViewHolder(android.view.ViewGroup, int);
}
`
	jc, err := parseJavap(output)
	if err != nil {
		t.Fatalf("parseJavap: %v", err)
	}
	if jc.FullName != "androidx.recyclerview.widget.RecyclerView$Adapter" {
		t.Errorf("FullName = %q", jc.FullName)
	}
	if got := jc.TypeParams["VH"]; got != "androidx.recyclerview.widget.RecyclerView$ViewHolder" {
		t.Errorf("TypeParams[VH] = %q, want bound FQN", got)
	}
}

// TestInferConstantDefault_BooleanReturnsFalse verifies that boolean
// constants with no value get the Go zero value "false" rather than "0",
// which was an invalid bool literal.
func TestInferConstantDefault_BooleanReturnsFalse(t *testing.T) {
	for _, tc := range []struct {
		jt   string
		want string
	}{
		{"boolean", "false"},
		{"java.lang.String", `""`},
		{"int", "0"},
		{"long", "0"},
		{"float", "0"},
	} {
		got := inferConstantDefault(tc.jt)
		if got != tc.want {
			t.Errorf("inferConstantDefault(%q) = %q, want %q", tc.jt, got, tc.want)
		}
	}
}

func TestDeduplicateGoTypes(t *testing.T) {
	t.Run("no collision", func(t *testing.T) {
		classes := []SpecClass{
			{JavaClass: "android.app.Activity", GoType: "Activity"},
			{JavaClass: "android.app.SearchManager", GoType: "SearchManager"},
		}
		result := deduplicateGoTypes(classes)
		if result[0].GoType != "Activity" || result[1].GoType != "SearchManager" {
			t.Errorf("unexpected rename: %v", result)
		}
	})

	t.Run("collision resolved by restoring full name", func(t *testing.T) {
		classes := []SpecClass{
			{JavaClass: "android.net.ipsec.ike.IkeSaProposal", GoType: "SaProposal"},
			{JavaClass: "android.net.ipsec.ike.SaProposal", GoType: "SaProposal"},
		}
		result := deduplicateGoTypes(classes)
		if result[0].GoType != "IkeSaProposal" {
			t.Errorf("expected IkeSaProposal, got %q", result[0].GoType)
		}
		if result[1].GoType != "SaProposal" {
			t.Errorf("expected SaProposal, got %q", result[1].GoType)
		}
	})

	t.Run("inner class collision", func(t *testing.T) {
		classes := []SpecClass{
			{JavaClass: "com.example.Foo$Bar", GoType: "Bar"},
			{JavaClass: "com.example.Bar", GoType: "Bar"},
		}
		result := deduplicateGoTypes(classes)
		if result[0].GoType != "FooBar" {
			t.Errorf("expected FooBar, got %q", result[0].GoType)
		}
		if result[1].GoType != "Bar" {
			t.Errorf("expected Bar, got %q", result[1].GoType)
		}
	})
}
