//go:build android

package widgetui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/AndroidGoLab/jni"
)

func TestLogfWritesEachLineToSink(t *testing.T) {
	var buf bytes.Buffer
	SetLogSinkForTest(&buf)
	defer SetLogSinkForTest(nil)

	Logf("hello %d", 42)
	Logf("multi\nline")

	got := buf.String()
	if !strings.HasPrefix(got, "hello 42\n") {
		t.Errorf("want first line 'hello 42\\n', got %q", got)
	}
	if !strings.Contains(got, "multi\nline\n") {
		t.Errorf("want 'multi\\nline\\n' in output, got %q", got)
	}
}

func TestLogfNoSinkIsNoCrash(t *testing.T) {
	// With sink unset, Logf must not panic. We cannot inspect logcat
	// from a unit test on the host (the cgo path requires liblog from
	// an Android process); the contract is "must not panic".
	SetLogSinkForTest(nil)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Logf panicked: %v", r)
		}
	}()
	Logf("noop %s", "ok")
}

func TestRegisterStoresCallback(t *testing.T) {
	prev := s.setupFn
	defer func() {
		s.mu.Lock()
		s.setupFn = prev
		s.mu.Unlock()
	}()

	called := 0
	Register(func(vm *jni.VM, activity *jni.Object) error {
		called++
		return nil
	})
	s.mu.Lock()
	got := s.setupFn
	s.mu.Unlock()
	if got == nil {
		t.Fatal("Register did not store callback")
	}
	_ = got(nil, nil)
	if called != 1 {
		t.Errorf("want callback invoked once, got %d", called)
	}
}

func TestOnCreateCapturesVMAndActivity(t *testing.T) {
	prevVM, prevAct := s.vm, s.activity
	defer func() {
		s.mu.Lock()
		s.vm, s.activity = prevVM, prevAct
		s.mu.Unlock()
	}()

	OnCreate(nil, nil)
	s.mu.Lock()
	gotVM, gotAct := s.vm, s.activity
	s.mu.Unlock()
	if gotVM != nil {
		t.Errorf("vm: want nil, got %v", gotVM)
	}
	if gotAct != nil {
		t.Errorf("activity: want nil, got %v", gotAct)
	}
}
