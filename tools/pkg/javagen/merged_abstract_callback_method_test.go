package javagen

import (
	"testing"
)

func TestMergedAbstractCallbackMethod_VoidReturn(t *testing.T) {
	m := MergedAbstractCallbackMethod{
		JavaMethod: "onEvent",
		Returns:    "void",
		Params:     []MergedParam{{JavaType: "int", GoName: "arg0"}},
	}

	if m.JavaReturnType() != "void" {
		t.Errorf("JavaReturnType = %q, want void", m.JavaReturnType())
	}
	if m.HasReturn() {
		t.Error("HasReturn should be false for void")
	}
}

func TestMergedAbstractCallbackMethod_IntReturn(t *testing.T) {
	m := MergedAbstractCallbackMethod{
		JavaMethod: "getCount",
		Returns:    "int",
		Params:     nil,
	}

	if m.JavaReturnType() != "int" {
		t.Errorf("JavaReturnType = %q, want int", m.JavaReturnType())
	}
	if !m.HasReturn() {
		t.Error("HasReturn should be true for int")
	}
	if m.JavaCastReturn() != "((Integer)" {
		t.Errorf("JavaCastReturn = %q, want ((Integer)", m.JavaCastReturn())
	}
	if m.JavaUnboxReturn() != ").intValue()" {
		t.Errorf("JavaUnboxReturn = %q, want ).intValue()", m.JavaUnboxReturn())
	}
}

func TestMergedAbstractCallbackMethod_BooleanReturn(t *testing.T) {
	m := MergedAbstractCallbackMethod{Returns: "boolean"}
	if m.JavaCastReturn() != "((Boolean)" {
		t.Errorf("JavaCastReturn = %q", m.JavaCastReturn())
	}
	if m.JavaUnboxReturn() != ").booleanValue()" {
		t.Errorf("JavaUnboxReturn = %q", m.JavaUnboxReturn())
	}
}

func TestMergedAbstractCallbackMethod_ObjectReturn(t *testing.T) {
	m := MergedAbstractCallbackMethod{Returns: "android.bluetooth.BluetoothDevice"}
	if m.JavaCastReturn() != "(android.bluetooth.BluetoothDevice)" {
		t.Errorf("JavaCastReturn = %q", m.JavaCastReturn())
	}
	if m.JavaUnboxReturn() != "" {
		t.Errorf("JavaUnboxReturn should be empty for objects, got %q", m.JavaUnboxReturn())
	}
}

func TestMergedAbstractCallbackMethod_DescriptorArrayTypesRenderAsJavaSource(t *testing.T) {
	m := MergedAbstractCallbackMethod{
		Returns: "[Landroidx.recyclerview.widget.RecyclerView$ViewHolder;",
		Params: []MergedParam{
			{JavaType: "[I", GoName: "arg0"},
			{JavaType: "[B", GoName: "arg1"},
			{JavaType: "[Z", GoName: "arg2"},
			{JavaType: "[Ljava.lang.String;", GoName: "arg3"},
			{JavaType: "[Landroidx.recyclerview.widget.RecyclerView$ViewHolder;", GoName: "arg4"},
			{JavaType: "[[I", GoName: "arg5"},
		},
	}

	if got, want := m.JavaReturnType(), "androidx.recyclerview.widget.RecyclerView.ViewHolder[]"; got != want {
		t.Errorf("JavaReturnType = %q, want %q", got, want)
	}
	if got, want := m.JavaCastReturn(), "(androidx.recyclerview.widget.RecyclerView.ViewHolder[])"; got != want {
		t.Errorf("JavaCastReturn = %q, want %q", got, want)
	}
	wantParams := "int[] arg0, byte[] arg1, boolean[] arg2, java.lang.String[] arg3, androidx.recyclerview.widget.RecyclerView.ViewHolder[] arg4, int[][] arg5"
	if got := m.JavaParamList(); got != wantParams {
		t.Errorf("JavaParamList = %q, want %q", got, wantParams)
	}
}

func TestMergedAbstractCallbackMethod_JavaParamList(t *testing.T) {
	m := MergedAbstractCallbackMethod{
		Params: []MergedParam{
			{JavaType: "int", GoName: "arg0"},
			{JavaType: "android.bluetooth.le.ScanResult", GoName: "arg1"},
		},
	}

	want := "int arg0, android.bluetooth.le.ScanResult arg1"
	if got := m.JavaParamList(); got != want {
		t.Errorf("JavaParamList = %q, want %q", got, want)
	}
}

func TestMergedAbstractCallbackMethod_JavaArgList(t *testing.T) {
	m := MergedAbstractCallbackMethod{
		Params: []MergedParam{
			{JavaType: "int", GoName: "arg0"},
			{JavaType: "android.bluetooth.le.ScanResult", GoName: "arg1"},
			{JavaType: "boolean", GoName: "arg2"},
		},
	}

	want := "Integer.valueOf(arg0), arg1, Boolean.valueOf(arg2)"
	if got := m.JavaArgList(); got != want {
		t.Errorf("JavaArgList = %q, want %q", got, want)
	}
}

func TestMergedAbstractCallbackMethod_EmptyParams(t *testing.T) {
	m := MergedAbstractCallbackMethod{Params: nil}
	if got := m.JavaParamList(); got != "" {
		t.Errorf("JavaParamList for no params = %q, want empty", got)
	}
	if got := m.JavaArgList(); got != "" {
		t.Errorf("JavaArgList for no params = %q, want empty", got)
	}
}

func TestMergedAbstractCallback_AdapterClassName(t *testing.T) {
	tests := []struct {
		javaClass string
		want      string
	}{
		{"android.bluetooth.le.ScanCallback", "ScanCallbackAdapter"},
		{"android.bluetooth.BluetoothGattCallback", "BluetoothGattCallbackAdapter"},
		{"androidx.recyclerview.widget.RecyclerView$ViewHolder", "RecyclerView$ViewHolderAdapter"},
		{"SomeCallback", "SomeCallbackAdapter"},
	}
	for _, tt := range tests {
		acb := MergedAbstractCallback{JavaClass: tt.javaClass}
		if got := acb.AdapterClassName(); got != tt.want {
			t.Errorf("AdapterClassName(%q) = %q, want %q", tt.javaClass, got, tt.want)
		}
	}
}

func TestMergedAbstractCallback_AdapterNames(t *testing.T) {
	tests := []struct {
		javaClass          string
		wantAdapterPackage string
		wantAdapterJNI     string
		wantAdapterJava    string
	}{
		{
			javaClass:          "android.bluetooth.le.ScanCallback",
			wantAdapterPackage: "center.dx.jni.generated.android.bluetooth.le",
			wantAdapterJNI:     "center/dx/jni/generated/android/bluetooth/le/ScanCallbackAdapter",
			wantAdapterJava:    "center.dx.jni.generated.android.bluetooth.le.ScanCallbackAdapter",
		},
		{
			javaClass:          "androidx.recyclerview.widget.RecyclerView$ViewHolder",
			wantAdapterPackage: "center.dx.jni.generated.androidx.recyclerview.widget",
			wantAdapterJNI:     "center/dx/jni/generated/androidx/recyclerview/widget/RecyclerView$ViewHolderAdapter",
			wantAdapterJava:    "center.dx.jni.generated.androidx.recyclerview.widget.RecyclerView$ViewHolderAdapter",
		},
		{
			javaClass:          "SomeCallback",
			wantAdapterPackage: "center.dx.jni.generated",
			wantAdapterJNI:     "center/dx/jni/generated/SomeCallbackAdapter",
			wantAdapterJava:    "center.dx.jni.generated.SomeCallbackAdapter",
		},
	}
	for _, tt := range tests {
		acb := MergedAbstractCallback{JavaClass: tt.javaClass}
		if got := acb.AdapterPackageName(); got != tt.wantAdapterPackage {
			t.Errorf("AdapterPackageName(%q) = %q, want %q", tt.javaClass, got, tt.wantAdapterPackage)
		}
		if got := acb.AdapterJNIName(); got != tt.wantAdapterJNI {
			t.Errorf("AdapterJNIName(%q) = %q, want %q", tt.javaClass, got, tt.wantAdapterJNI)
		}
		if got := acb.AdapterJavaName(); got != tt.wantAdapterJava {
			t.Errorf("AdapterJavaName(%q) = %q, want %q", tt.javaClass, got, tt.wantAdapterJava)
		}
	}
}

func TestMergedAbstractCallback_JavaPackage(t *testing.T) {
	tests := []struct {
		javaClass string
		want      string
	}{
		{"android.bluetooth.le.ScanCallback", "android.bluetooth.le"},
		{"SomeCallback", ""},
	}
	for _, tt := range tests {
		acb := MergedAbstractCallback{JavaClass: tt.javaClass}
		if got := acb.JavaPackage(); got != tt.want {
			t.Errorf("JavaPackage(%q) = %q, want %q", tt.javaClass, got, tt.want)
		}
	}
}
