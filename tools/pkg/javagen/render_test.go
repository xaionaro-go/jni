package javagen

import (
	"bytes"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestJavaTemplatesParse(t *testing.T) {
	templates, err := loadTemplates("../../../templates/java")
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	for _, name := range []string{
		"package.go.tmpl", "init.go.tmpl", "class.go.tmpl",
		"callbacks.go.tmpl", "type.go.tmpl", "constants.go.tmpl",
	} {
		if templates.Lookup(name) == nil {
			t.Errorf("template %s not found", name)
		}
	}
}

func TestJavaTemplatesRender(t *testing.T) {
	templates, err := loadTemplates("../../../templates/java")
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}

	spec := testLocationSpec()

	// Templates that take MergedSpec as data.
	specTests := []struct {
		tmpl     string
		contains []string
	}{
		{
			tmpl:     "package.go.tmpl",
			contains: []string{"package location", "jni"},
		},
		{
			tmpl:     "init.go.tmpl",
			contains: []string{"sync.Once", "ensureInit", "doInit", "clsManager", "FindClass"},
		},
		{
			tmpl:     "callbacks.go.tmpl",
			contains: []string{"LocationListener", "registerLocationListener", "NewProxy", "methodName"},
		},
		{
			tmpl:     "constants.go.tmpl",
			contains: []string{"package location", "GPS", "Network"},
		},
	}

	for _, tt := range specTests {
		t.Run(tt.tmpl, func(t *testing.T) {
			tmpl := templates.Lookup(tt.tmpl)
			if tmpl == nil {
				t.Fatalf("template %s not found", tt.tmpl)
			}

			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, spec); err != nil {
				t.Fatalf("execute %s: %v", tt.tmpl, err)
			}

			output := buf.String()
			for _, s := range tt.contains {
				if !strings.Contains(output, s) {
					t.Errorf("output of %s missing %q.\nFull output:\n%s", tt.tmpl, s, output)
				}
			}

			src := generatedHeader + output
			fset := token.NewFileSet()
			if _, err := parser.ParseFile(fset, tt.tmpl+".go", src, parser.AllErrors); err != nil {
				t.Errorf("output of %s is not valid Go:\n%v\n\nSource:\n%s", tt.tmpl, err, src)
			}
		})
	}

	// class.go.tmpl takes classRenderData.
	t.Run("class.go.tmpl", func(t *testing.T) {
		tmpl := templates.Lookup("class.go.tmpl")
		if tmpl == nil {
			t.Fatal("class.go.tmpl not found")
		}
		data := &classRenderData{
			Package:     spec.Package,
			MergedClass: spec.Classes[0],
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			t.Fatalf("execute class.go.tmpl: %v", err)
		}
		output := buf.String()
		for _, s := range []string{"type Manager struct", "NewManager", "VM.Do", "GetSystemService", "GetLastKnownLocation", "func (m *Manager)", "ensureInit"} {
			if !strings.Contains(output, s) {
				t.Errorf("output of class.go.tmpl missing %q.\nFull output:\n%s", s, output)
			}
		}
		src := generatedHeader + output
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, "manager.go", src, parser.AllErrors); err != nil {
			t.Errorf("output of class.go.tmpl is not valid Go:\n%v\n\nSource:\n%s", err, src)
		}
	})

	t.Run("class.go.tmpl constructor guards unavailable class and constructor", func(t *testing.T) {
		tmpl := templates.Lookup("class.go.tmpl")
		if tmpl == nil {
			t.Fatal("class.go.tmpl not found")
		}
		data := &classRenderData{
			Package: "appbar",
			MergedClass: MergedClass{
				JavaClass:         "com.google.android.material.appbar.MaterialToolbar",
				JavaClassSlash:    "com/google/android/material/appbar/MaterialToolbar",
				GoType:            "MaterialToolbar",
				Obtain:            "constructor",
				ConstructorJNISig: "(Landroid/content/Context;)V",
				ConstructorParams: []MergedParam{
					{
						JavaType: "android.content.Context",
						GoName:   "arg0",
						GoType:   "*jni.Object",
						IsObject: true,
					},
				},
			},
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			t.Fatalf("execute class.go.tmpl: %v", err)
		}
		output := buf.String()
		for _, want := range []string{
			`if clsMaterialToolbar == nil {`,
			`return fmt.Errorf("com.google.android.material.appbar.MaterialToolbar is not available on this device")`,
			`if midMaterialToolbarCtor == nil {`,
			`return fmt.Errorf("com.google.android.material.appbar.MaterialToolbar constructor (Landroid/content/Context;)V is not available on this device")`,
		} {
			if !strings.Contains(output, want) {
				t.Errorf("constructor wrapper missing %q.\nFull output:\n%s", want, output)
			}
		}
		if strings.Contains(output, "env.NewObject((*jni.Class)(unsafe.Pointer(clsMaterialToolbar)), midMaterialToolbarCtor") &&
			!strings.Contains(output, "if clsMaterialToolbar == nil") {
			t.Errorf("constructor calls NewObject without guarding clsMaterialToolbar:\n%s", output)
		}
		src := generatedHeader + output
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, "material_toolbar.go", src, parser.AllErrors); err != nil {
			t.Errorf("constructor class.go.tmpl output is not valid Go:\n%v\n\nSource:\n%s", err, src)
		}
	})

	// type.go.tmpl takes dataClassRenderData.
	t.Run("type.go.tmpl", func(t *testing.T) {
		tmpl := templates.Lookup("type.go.tmpl")
		if tmpl == nil {
			t.Fatal("type.go.tmpl not found")
		}
		data := &dataClassRenderData{
			Package:         spec.Package,
			MergedDataClass: spec.DataClasses[0],
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			t.Fatalf("execute type.go.tmpl: %v", err)
		}
		output := buf.String()
		for _, s := range []string{"type Location struct", "Latitude", "ExtractLocation"} {
			if !strings.Contains(output, s) {
				t.Errorf("output of type.go.tmpl missing %q.\nFull output:\n%s", s, output)
			}
		}
		src := generatedHeader + output
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, "location.go", src, parser.AllErrors); err != nil {
			t.Errorf("output of type.go.tmpl is not valid Go:\n%v\n\nSource:\n%s", err, src)
		}
	})
}

func TestJavaTemplatesEmptySpec(t *testing.T) {
	templates, err := loadTemplates("../../../templates/java")
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}

	// A spec with only package + constants (no classes, callbacks, data classes).
	spec := &MergedSpec{
		Package:         "permission",
		JavaPackageDesc: "android.Manifest.permission",
		ConstantGroups: []MergedConstantGroup{
			{
				Values: []MergedConstant{
					{GoName: "Camera", Value: `"android.permission.CAMERA"`},
					{GoName: "Internet", Value: `"android.permission.INTERNET"`},
				},
			},
		},
	}

	// package.go.tmpl and constants.go.tmpl should render fine.
	for _, tmplName := range []string{"package.go.tmpl", "constants.go.tmpl"} {
		tmpl := templates.Lookup(tmplName)
		if tmpl == nil {
			t.Fatalf("template %s not found", tmplName)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, spec); err != nil {
			t.Fatalf("execute %s with empty spec: %v", tmplName, err)
		}
	}

	// init.go.tmpl should render with empty ranges.
	tmpl := templates.Lookup("init.go.tmpl")
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, spec); err != nil {
		t.Fatalf("execute init.go.tmpl with empty spec: %v", err)
	}
	src := generatedHeader + buf.String()
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "init.go", src, parser.AllErrors); err != nil {
		t.Errorf("init.go.tmpl with empty spec is not valid Go:\n%v\n\nSource:\n%s", err, src)
	}
}

// testLocationSpec returns a MergedSpec modelling the location package.
func testLocationSpec() *MergedSpec {
	return &MergedSpec{
		Package:         "location",
		JavaPackageDesc: "android.location (GPS and network location)",
		Classes: []MergedClass{
			{
				JavaClass:      "android.location.LocationManager",
				JavaClassSlash: "android/location/LocationManager",
				GoType:         "Manager",
				Obtain:         "system_service",
				ServiceName:    "location",
				Close:          true,
				Methods: []MergedMethod{
					{
						JavaMethod:     "getLastKnownLocation",
						GoName:         "GetLastKnownLocation",
						JNISig:         "(Ljava/lang/String;)Landroid/location/Location;",
						CallSuffix:     "Object",
						HasError:       true,
						GoParamList:    "provider string",
						GoReturnList:   "(*Location, error)",
						GoReturnVars:   "var result *Location",
						GoReturnValues: "result, callErr",
						JNIArgs:        ", jni.ObjectValue(jProvider)",
						Params: []MergedParam{
							{
								JavaType:       "String",
								GoName:         "provider",
								GoType:         "string",
								IsString:       true,
								ConversionCode: "",
							},
						},
					},
					{
						JavaMethod:     "isProviderEnabled",
						GoName:         "IsProviderEnabled",
						JNISig:         "(Ljava/lang/String;)Z",
						CallSuffix:     "Boolean",
						HasError:       true,
						GoParamList:    "provider string",
						GoReturnList:   "(bool, error)",
						GoReturnVars:   "var result bool",
						GoReturnValues: "result, callErr",
						JNIArgs:        ", jni.ObjectValue(jProvider)",
						Params: []MergedParam{
							{
								JavaType: "String",
								GoName:   "provider",
								GoType:   "string",
								IsString: true,
							},
						},
					},
					{
						JavaMethod:     "removeUpdates",
						GoName:         "RemoveUpdates",
						JNISig:         "(Landroid/location/LocationListener;)V",
						CallSuffix:     "Void",
						ReturnKind:     ReturnVoid,
						HasError:       true,
						GoParamList:    "listener *jni.Object",
						GoReturnList:   "error",
						GoReturnVars:   "",
						GoReturnValues: "callErr",
						JNIArgs:        ", jni.ObjectValue(listener)",
						Params: []MergedParam{
							{
								JavaType: "android.location.LocationListener",
								GoName:   "listener",
								GoType:   "*jni.Object",
								IsObject: true,
							},
						},
					},
				},
			},
		},
		DataClasses: []MergedDataClass{
			{
				JavaClass:      "android.location.Location",
				JavaClassSlash: "android/location/Location",
				GoType:         "Location",
				Fields: []MergedField{
					{JavaMethod: "getLatitude", GoName: "Latitude", GoType: "float64", JNISig: "()D", CallSuffix: "Double"},
					{JavaMethod: "getLongitude", GoName: "Longitude", GoType: "float64", JNISig: "()D", CallSuffix: "Double"},
					{JavaMethod: "getAltitude", GoName: "Altitude", GoType: "float64", JNISig: "()D", CallSuffix: "Double"},
					{JavaMethod: "getAccuracy", GoName: "Accuracy", GoType: "float32", JNISig: "()F", CallSuffix: "Float"},
					{JavaMethod: "getSpeed", GoName: "Speed", GoType: "float32", JNISig: "()F", CallSuffix: "Float"},
					{JavaMethod: "getBearing", GoName: "Bearing", GoType: "float32", JNISig: "()F", CallSuffix: "Float"},
					{JavaMethod: "getTime", GoName: "Time", GoType: "int64", JNISig: "()J", CallSuffix: "Long"},
					{JavaMethod: "getProvider", GoName: "Provider", GoType: "string", JNISig: "()Ljava/lang/String;", CallSuffix: "Object"},
				},
			},
		},
		Callbacks: []MergedCallback{
			{
				JavaInterface: "android.location.LocationListener",
				GoType:        "LocationListener",
				Methods: []MergedCallbackMethod{
					{
						JavaMethod: "onLocationChanged",
						GoField:    "OnLocationChanged",
						GoParams:   "arg0 *jni.Object",
						Params: []MergedParam{
							{JavaType: "android.location.Location", GoName: "arg0", GoType: "*jni.Object", IsObject: true},
						},
					},
					{
						JavaMethod: "onProviderEnabled",
						GoField:    "OnProviderEnabled",
						GoParams:   "arg0 string",
						Params: []MergedParam{
							{JavaType: "String", GoName: "arg0", GoType: "string", IsString: true},
						},
					},
					{
						JavaMethod: "onProviderDisabled",
						GoField:    "OnProviderDisabled",
						GoParams:   "arg0 string",
						Params: []MergedParam{
							{JavaType: "String", GoName: "arg0", GoType: "string", IsString: true},
						},
					},
				},
			},
		},
		ConstantGroups: []MergedConstantGroup{
			{
				Values: []MergedConstant{
					{GoName: "GPS", Value: `"gps"`},
					{GoName: "Network", Value: `"network"`},
					{GoName: "Passive", Value: `"passive"`},
				},
			},
		},
	}
}
