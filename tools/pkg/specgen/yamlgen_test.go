package specgen

import (
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
			FullName:   "android.bluetooth.le.ScanCallback",
			IsAbstract: true,
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
			FullName:   "android.app.AbstractThing",
			IsAbstract: true,
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
