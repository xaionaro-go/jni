package specgen

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitRespectingGenerics(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty",
			input:    "",
			expected: []string{""},
		},
		{
			name:     "single_primitive",
			input:    "int",
			expected: []string{"int"},
		},
		{
			name:     "two_primitives",
			input:    "int, boolean",
			expected: []string{"int", " boolean"},
		},
		{
			name:     "generic_with_comma",
			input:    "java.util.Map<java.lang.String, android.hardware.camera2.params.SessionConfiguration>",
			expected: []string{"java.util.Map<java.lang.String, android.hardware.camera2.params.SessionConfiguration>"},
		},
		{
			name:     "generic_followed_by_plain",
			input:    "java.util.Map<java.lang.String, java.lang.String>, int",
			expected: []string{"java.util.Map<java.lang.String, java.lang.String>", " int"},
		},
		{
			name:     "nested_generics",
			input:    "java.util.Map<java.lang.String, java.util.List<java.util.Set<java.lang.Integer>>>",
			expected: []string{"java.util.Map<java.lang.String, java.util.List<java.util.Set<java.lang.Integer>>>"},
		},
		{
			name:     "multiple_generics_with_commas",
			input:    "java.util.Map<A, B>, java.util.List<C, D>",
			expected: []string{"java.util.Map<A, B>", " java.util.List<C, D>"},
		},
		{
			name:     "mixed_params_with_generic_in_middle",
			input:    "int, java.util.Map<K, V>, boolean",
			expected: []string{"int", " java.util.Map<K, V>", " boolean"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := splitRespectingGenerics(tc.input)
			if !reflect.DeepEqual(result, tc.expected) {
				t.Errorf("got %v, want %v", result, tc.expected)
			}
		})
	}
}

func TestParseParams(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    string
		expected []JavapParam
	}{
		{
			name:     "empty",
			input:    "",
			expected: nil,
		},
		{
			name:  "single_primitive",
			input: "int",
			expected: []JavapParam{
				{JavaType: "int"},
			},
		},
		{
			name:  "generic_map_not_split",
			input: "java.util.Map<java.lang.String, android.hardware.camera2.params.SessionConfiguration>",
			expected: []JavapParam{
				{JavaType: "java.util.Map<java.lang.String, android.hardware.camera2.params.SessionConfiguration>"},
			},
		},
		{
			name:  "generic_followed_by_plain_params",
			input: "java.util.concurrent.Executor, android.os.OutcomeReceiver<android.telecom.CallControl, android.telecom.CallException>, android.telecom.CallControlCallback",
			expected: []JavapParam{
				{JavaType: "java.util.concurrent.Executor"},
				{JavaType: "android.os.OutcomeReceiver<android.telecom.CallControl, android.telecom.CallException>"},
				{JavaType: "android.telecom.CallControlCallback"},
			},
		},
		{
			name:  "nested_generics",
			input: "java.util.Map<java.lang.String, java.util.List<java.util.Set<java.lang.Integer>>>",
			expected: []JavapParam{
				{JavaType: "java.util.Map<java.lang.String, java.util.List<java.util.Set<java.lang.Integer>>>"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := parseParams(tc.input)
			if !reflect.DeepEqual(result, tc.expected) {
				t.Errorf("got %v, want %v", result, tc.expected)
			}
		})
	}
}

func TestParseJavap_ConstantValues(t *testing.T) {
	// Simulates javap -public -verbose output with ConstantValue attributes.
	verboseOutput := strings.Join([]string{
		`Classfile jar:file:///android.jar!/android/location/LocationManager.class`,
		`  Compiled from "LocationManager.java"`,
		`public class android.location.LocationManager`,
		`  minor version: 0`,
		`  major version: 52`,
		`  flags: (0x0021) ACC_PUBLIC, ACC_SUPER`,
		`Constant pool:`,
		`    #1 = Utf8               GPS_PROVIDER`,
		`{`,
		`  public static final java.lang.String GPS_PROVIDER;`,
		`    descriptor: Ljava/lang/String;`,
		`    flags: (0x0019) ACC_PUBLIC, ACC_STATIC, ACC_FINAL`,
		`    ConstantValue: String gps`,
		``,
		`  public static final java.lang.String NETWORK_PROVIDER;`,
		`    descriptor: Ljava/lang/String;`,
		`    flags: (0x0019) ACC_PUBLIC, ACC_STATIC, ACC_FINAL`,
		`    ConstantValue: String network`,
		``,
		`  public static final int SOME_INT;`,
		`    descriptor: I`,
		`    flags: (0x0019) ACC_PUBLIC, ACC_STATIC, ACC_FINAL`,
		`    ConstantValue: int 42`,
		``,
		`  public boolean isLocationEnabled();`,
		`    descriptor: ()Z`,
		`    flags: (0x0001) ACC_PUBLIC`,
		`}`,
	}, "\n")

	jc, err := parseJavap(verboseOutput)
	if err != nil {
		t.Fatalf("parseJavap: %v", err)
	}

	if jc.FullName != "android.location.LocationManager" {
		t.Errorf("FullName = %q, want %q", jc.FullName, "android.location.LocationManager")
	}

	// Verify constants were parsed with values.
	wantConstants := []JavapConstant{
		{Name: "GPS_PROVIDER", JavaType: "java.lang.String", Value: "gps"},
		{Name: "NETWORK_PROVIDER", JavaType: "java.lang.String", Value: "network"},
		{Name: "SOME_INT", JavaType: "int", Value: "42"},
	}
	if !reflect.DeepEqual(jc.Constants, wantConstants) {
		t.Errorf("Constants:\n  got  %+v\n  want %+v", jc.Constants, wantConstants)
	}

	// Verify methods still parse.
	if len(jc.Methods) != 1 {
		t.Fatalf("len(Methods) = %d, want 1", len(jc.Methods))
	}
	if jc.Methods[0].Name != "isLocationEnabled" {
		t.Errorf("Methods[0].Name = %q, want %q", jc.Methods[0].Name, "isLocationEnabled")
	}
}

func TestParseJavap_ImplementsWithoutBrace(t *testing.T) {
	// In javap -verbose output, the class line does not end with {.
	verboseOutput := strings.Join([]string{
		`Classfile jar:file:///android.jar!/android/location/GnssStatus.class`,
		`  Compiled from "GnssStatus.java"`,
		`public final class android.location.GnssStatus implements android.os.Parcelable`,
		`  minor version: 0`,
		`{`,
		`  public static final int CONSTELLATION_GPS;`,
		`    descriptor: I`,
		`    flags: (0x0019) ACC_PUBLIC, ACC_STATIC, ACC_FINAL`,
		`    ConstantValue: int 1`,
		`}`,
	}, "\n")

	jc, err := parseJavap(verboseOutput)
	if err != nil {
		t.Fatalf("parseJavap: %v", err)
	}

	if jc.FullName != "android.location.GnssStatus" {
		t.Errorf("FullName = %q, want %q", jc.FullName, "android.location.GnssStatus")
	}
	if !jc.IsFinal {
		t.Error("IsFinal = false, want true")
	}
	if len(jc.Implements) != 1 || jc.Implements[0] != "android.os.Parcelable" {
		t.Errorf("Implements = %v, want [android.os.Parcelable]", jc.Implements)
	}
	if len(jc.Constants) != 1 {
		t.Fatalf("len(Constants) = %d, want 1", len(jc.Constants))
	}
	if jc.Constants[0].Value != "1" {
		t.Errorf("Constants[0].Value = %q, want %q", jc.Constants[0].Value, "1")
	}
}

func TestParseJavap_NativeMethods(t *testing.T) {
	// Native methods like MediaRecorder.start() must be parsed correctly.
	verboseOutput := strings.Join([]string{
		`Classfile jar:file:///android.jar!/android/media/MediaRecorder.class`,
		`  Compiled from "MediaRecorder.java"`,
		`public class android.media.MediaRecorder`,
		`{`,
		`  public native void start() throws java.lang.IllegalStateException;`,
		`    descriptor: ()V`,
		`    flags: (0x0101) ACC_PUBLIC, ACC_NATIVE`,
		``,
		`  public native void setAudioSource(int) throws java.lang.IllegalStateException;`,
		`    descriptor: (I)V`,
		`    flags: (0x0101) ACC_PUBLIC, ACC_NATIVE`,
		``,
		`  public native void setVideoSize(int, int) throws java.lang.IllegalStateException;`,
		`    descriptor: (II)V`,
		`    flags: (0x0101) ACC_PUBLIC, ACC_NATIVE`,
		``,
		`  public native int getMaxAmplitude() throws java.lang.IllegalStateException;`,
		`    descriptor: ()I`,
		`    flags: (0x0101) ACC_PUBLIC, ACC_NATIVE`,
		``,
		`  public void prepare() throws java.lang.IllegalStateException, java.io.IOException;`,
		`    descriptor: ()V`,
		`    flags: (0x0001) ACC_PUBLIC`,
		`}`,
	}, "\n")

	jc, err := parseJavap(verboseOutput)
	if err != nil {
		t.Fatalf("parseJavap: %v", err)
	}

	if len(jc.Methods) != 5 {
		t.Fatalf("len(Methods) = %d, want 5; got: %+v", len(jc.Methods), jc.Methods)
	}

	want := []struct {
		name       string
		retType    string
		isStatic   bool
		throws     bool
		paramCount int
	}{
		{"start", "void", false, true, 0},
		{"setAudioSource", "void", false, true, 1},
		{"setVideoSize", "void", false, true, 2},
		{"getMaxAmplitude", "int", false, true, 0},
		{"prepare", "void", false, true, 0},
	}
	for i, w := range want {
		m := jc.Methods[i]
		if m.Name != w.name {
			t.Errorf("[%d] Name = %q, want %q", i, m.Name, w.name)
		}
		if m.ReturnType != w.retType {
			t.Errorf("[%d] ReturnType = %q, want %q", i, m.ReturnType, w.retType)
		}
		if m.IsStatic != w.isStatic {
			t.Errorf("[%d] IsStatic = %v, want %v", i, m.IsStatic, w.isStatic)
		}
		if m.Throws != w.throws {
			t.Errorf("[%d] Throws = %v, want %v", i, m.Throws, w.throws)
		}
		if len(m.Params) != w.paramCount {
			t.Errorf("[%d] len(Params) = %d, want %d", i, len(m.Params), w.paramCount)
		}
	}
}

func TestParseJavap_Constructors(t *testing.T) {
	// Simulates javap output for a class with multiple constructors.
	verboseOutput := strings.Join([]string{
		`Classfile jar:file:///android.jar!/android/media/MediaRecorder.class`,
		`  Compiled from "MediaRecorder.java"`,
		`public class android.media.MediaRecorder`,
		`{`,
		`  public android.media.MediaRecorder();`,
		`    descriptor: ()V`,
		`    flags: (0x0001) ACC_PUBLIC`,
		``,
		`  public android.media.MediaRecorder(android.content.Context);`,
		`    descriptor: (Landroid/content/Context;)V`,
		`    flags: (0x0001) ACC_PUBLIC`,
		``,
		`  public native void start() throws java.lang.IllegalStateException;`,
		`    descriptor: ()V`,
		`    flags: (0x0101) ACC_PUBLIC, ACC_NATIVE`,
		`}`,
	}, "\n")

	jc, err := parseJavap(verboseOutput)
	if err != nil {
		t.Fatalf("parseJavap: %v", err)
	}

	// Verify constructors were parsed.
	if len(jc.Constructors) != 2 {
		t.Fatalf("len(Constructors) = %d, want 2; got: %+v", len(jc.Constructors), jc.Constructors)
	}

	// First constructor: no-arg.
	if len(jc.Constructors[0].Params) != 0 {
		t.Errorf("Constructors[0].Params = %v, want empty", jc.Constructors[0].Params)
	}

	// Second constructor: Context param.
	if len(jc.Constructors[1].Params) != 1 {
		t.Fatalf("len(Constructors[1].Params) = %d, want 1", len(jc.Constructors[1].Params))
	}
	if jc.Constructors[1].Params[0].JavaType != "android.content.Context" {
		t.Errorf("Constructors[1].Params[0].JavaType = %q, want %q",
			jc.Constructors[1].Params[0].JavaType, "android.content.Context")
	}

	// Methods should still parse correctly alongside constructors.
	if len(jc.Methods) != 1 {
		t.Fatalf("len(Methods) = %d, want 1", len(jc.Methods))
	}
	if jc.Methods[0].Name != "start" {
		t.Errorf("Methods[0].Name = %q, want %q", jc.Methods[0].Name, "start")
	}
}

func TestParseJavap_InnerClassConstructors(t *testing.T) {
	output := strings.Join([]string{
		`Compiled from "RecyclerView.java"`,
		`public abstract class androidx.recyclerview.widget.RecyclerView$Adapter<VH extends androidx.recyclerview.widget.RecyclerView$ViewHolder> extends java.lang.Object`,
		`{`,
		`  public androidx.recyclerview.widget.RecyclerView$Adapter();`,
		`  public abstract VH onCreateViewHolder(android.view.ViewGroup, int);`,
		`}`,
	}, "\n")

	jc, err := parseJavap(output)
	if err != nil {
		t.Fatalf("parseJavap: %v", err)
	}
	if len(jc.Constructors) != 1 {
		t.Fatalf("len(Constructors) = %d, want 1; got: %+v", len(jc.Constructors), jc.Constructors)
	}
	if len(jc.Constructors[0].Params) != 0 {
		t.Errorf("Constructors[0].Params = %v, want empty", jc.Constructors[0].Params)
	}
	if jc.SuperClass != "java.lang.Object" {
		t.Errorf("SuperClass = %q, want java.lang.Object", jc.SuperClass)
	}
	if got := jc.TypeParams["VH"]; got != "androidx.recyclerview.widget.RecyclerView$ViewHolder" {
		t.Errorf("TypeParams[VH] = %q, want bound FQN", got)
	}
}

func TestParseJavap_ConstructorNotConfusedWithMethod(t *testing.T) {
	// A class where the "constructor-like" line does NOT match the class name
	// should not be parsed as a constructor.
	verboseOutput := strings.Join([]string{
		`Classfile jar:file:///android.jar!/android/app/Activity.class`,
		`  Compiled from "Activity.java"`,
		`public class android.app.Activity`,
		`{`,
		`  public android.app.Activity();`,
		`    descriptor: ()V`,
		`    flags: (0x0001) ACC_PUBLIC`,
		``,
		`  public void finish();`,
		`    descriptor: ()V`,
		`    flags: (0x0001) ACC_PUBLIC`,
		`}`,
	}, "\n")

	jc, err := parseJavap(verboseOutput)
	if err != nil {
		t.Fatalf("parseJavap: %v", err)
	}

	if len(jc.Constructors) != 1 {
		t.Fatalf("len(Constructors) = %d, want 1", len(jc.Constructors))
	}
	if len(jc.Constructors[0].Params) != 0 {
		t.Errorf("expected no-arg constructor, got params: %v", jc.Constructors[0].Params)
	}
	if len(jc.Methods) != 1 {
		t.Fatalf("len(Methods) = %d, want 1", len(jc.Methods))
	}
}

func TestParseJavap_NonVerboseStillWorks(t *testing.T) {
	// Non-verbose javap output (no ConstantValue lines).
	output := strings.Join([]string{
		`Compiled from "LocationManager.java"`,
		`public class android.location.LocationManager {`,
		`  public static final java.lang.String GPS_PROVIDER;`,
		`  public static final int SOME_INT;`,
		`  public boolean isLocationEnabled();`,
		`}`,
	}, "\n")

	jc, err := parseJavap(output)
	if err != nil {
		t.Fatalf("parseJavap: %v", err)
	}

	// Constants should be parsed but with empty values.
	if len(jc.Constants) != 2 {
		t.Fatalf("len(Constants) = %d, want 2", len(jc.Constants))
	}
	if jc.Constants[0].Value != "" {
		t.Errorf("Constants[0].Value = %q, want empty", jc.Constants[0].Value)
	}
	if jc.Constants[1].Value != "" {
		t.Errorf("Constants[1].Value = %q, want empty", jc.Constants[1].Value)
	}
}

// TestParseJavap_FinalMethodFlag verifies that per-method `final` modifiers
// are captured into JavapMethod.IsFinal. Final methods cannot be overridden,
// so the abstract-callback generator must skip them — and that decision
// requires the flag to survive parsing.
func TestParseJavap_FinalMethodFlag(t *testing.T) {
	output := strings.Join([]string{
		`Compiled from "RecyclerView.java"`,
		`public abstract class androidx.recyclerview.widget.RecyclerView$Adapter`,
		`{`,
		`  public final androidx.recyclerview.widget.RecyclerView$ViewHolder createViewHolder(android.view.ViewGroup, int);`,
		`  public final void bindViewHolder(androidx.recyclerview.widget.RecyclerView$ViewHolder, int);`,
		`  public abstract androidx.recyclerview.widget.RecyclerView$ViewHolder onCreateViewHolder(android.view.ViewGroup, int);`,
		`  public abstract void onBindViewHolder(androidx.recyclerview.widget.RecyclerView$ViewHolder, int);`,
		`}`,
	}, "\n")

	jc, err := parseJavap(output)
	if err != nil {
		t.Fatalf("parseJavap: %v", err)
	}
	want := map[string]bool{
		"createViewHolder":   true,
		"bindViewHolder":     true,
		"onCreateViewHolder": false,
		"onBindViewHolder":   false,
	}
	got := make(map[string]bool, len(jc.Methods))
	for _, m := range jc.Methods {
		got[m.Name] = m.IsFinal
	}
	for name, wantFinal := range want {
		gotFinal, ok := got[name]
		if !ok {
			t.Errorf("method %q not parsed; got methods: %v", name, got)
			continue
		}
		if gotFinal != wantFinal {
			t.Errorf("method %q IsFinal = %v, want %v", name, gotFinal, wantFinal)
		}
	}
}

func TestParseJavap_MethodVerboseFlags(t *testing.T) {
	for _, tc := range []struct {
		name          string
		output        string
		method        string
		wantBridge    bool
		wantSynthetic bool
	}{
		{
			name: "bridge synthetic object clone",
			output: strings.Join([]string{
				`Compiled from "Animator.java"`,
				`public abstract class android.animation.Animator`,
				`{`,
				`  public java.lang.Object clone();`,
				`    descriptor: ()Ljava/lang/Object;`,
				`    flags: (0x1041) ACC_PUBLIC, ACC_BRIDGE, ACC_SYNTHETIC`,
				`}`,
			}, "\n"),
			method:        "clone",
			wantBridge:    true,
			wantSynthetic: true,
		},
		{
			name: "synthetic callback helper",
			output: strings.Join([]string{
				`Compiled from "Callback.java"`,
				`public abstract class com.example.Callback`,
				`{`,
				`  public void access$000();`,
				`    descriptor: ()V`,
				`    flags: (0x1001) ACC_PUBLIC, ACC_SYNTHETIC`,
				`}`,
			}, "\n"),
			method:        "access$000",
			wantSynthetic: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jc, err := parseJavap(tc.output)
			if err != nil {
				t.Fatalf("parseJavap: %v", err)
			}
			if len(jc.Methods) != 1 {
				t.Fatalf("len(Methods) = %d, want 1", len(jc.Methods))
			}
			m := jc.Methods[0]
			if m.Name != tc.method {
				t.Fatalf("method = %q, want %q", m.Name, tc.method)
			}
			if m.IsBridge != tc.wantBridge {
				t.Errorf("IsBridge = %v, want %v", m.IsBridge, tc.wantBridge)
			}
			if m.IsSynthetic != tc.wantSynthetic {
				t.Errorf("IsSynthetic = %v, want %v", m.IsSynthetic, tc.wantSynthetic)
			}
		})
	}
}

func TestParseJavap_ProtectedAbstractMethod(t *testing.T) {
	output := strings.Join([]string{
		`Compiled from "JobIntentService.java"`,
		`public abstract class androidx.core.app.JobIntentService extends android.app.Service`,
		`{`,
		`  protected abstract void onHandleWork(android.content.Intent);`,
		`    descriptor: (Landroid/content/Intent;)V`,
		`    flags: (0x0404) ACC_PROTECTED, ACC_ABSTRACT`,
		`}`,
	}, "\n")

	jc, err := parseJavap(output)
	if err != nil {
		t.Fatalf("parseJavap: %v", err)
	}
	if len(jc.Methods) != 1 {
		t.Fatalf("len(Methods) = %d, want 1", len(jc.Methods))
	}
	m := jc.Methods[0]
	if m.Name != "onHandleWork" {
		t.Fatalf("Name = %q, want onHandleWork", m.Name)
	}
	if !m.IsProtected {
		t.Error("IsProtected = false, want true")
	}
	if !m.IsAbstract {
		t.Error("IsAbstract = false, want true")
	}
}

func TestParseJavap_UnparsedAbstractMethodFlag(t *testing.T) {
	output := strings.Join([]string{
		`Compiled from "FragmentContainer.java"`,
		`public abstract class android.app.FragmentContainer`,
		`{`,
		`  public abstract <T extends android.view.View> T onFindViewById(int);`,
		`}`,
	}, "\n")

	jc, err := parseJavap(output)
	if err != nil {
		t.Fatalf("parseJavap: %v", err)
	}
	if !jc.HasUnparsedAbstractMethods {
		t.Fatal("HasUnparsedAbstractMethods = false, want true")
	}
}

func TestParseJavap_PackagePrivateAbstractMethodFlag(t *testing.T) {
	output := strings.Join([]string{
		`Compiled from "ShapeableDelegate.java"`,
		`public abstract class com.google.android.material.shape.ShapeableDelegate`,
		`{`,
		`  abstract boolean shouldUseCompatClipping();`,
		`}`,
	}, "\n")

	jc, err := parseJavap(output)
	if err != nil {
		t.Fatalf("parseJavap: %v", err)
	}
	if !jc.HasPackagePrivateAbstractMethods {
		t.Fatal("HasPackagePrivateAbstractMethods = false, want true")
	}
}
