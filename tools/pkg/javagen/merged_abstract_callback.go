package javagen

import "strings"

const generatedAdapterPackageName = "center.dx.jni.generated"

// MergedAbstractCallback is a resolved abstract callback class.
type MergedAbstractCallback struct {
	JavaClass       string
	JavaClassSlash  string
	GoType          string
	Methods         []MergedAbstractCallbackMethod
	TypeParamBounds []string
}

// JavaClassSource returns the JavaClass FQN with binary nested-class
// separators ($) rewritten to source-level separators (.). javac rejects
// `RecyclerView$Adapter` in import/extends positions even though that
// form is the JVM binary name; source code must use `RecyclerView.Adapter`.
// AdapterClassName, JavaSimpleName, etc. continue to use the $ binary
// form because tryAbstractAdapter in proxy.go resolves the generated
// adapter class by binary name.
func (m *MergedAbstractCallback) JavaClassSource() string {
	return strings.ReplaceAll(m.JavaClass, "$", ".")
}

// JavaImportSource returns the class import needed by the adapter source.
// Nested classes import their outer class so source can refer to
// Outer.Nested in extends and method signatures.
func (m *MergedAbstractCallback) JavaImportSource() string {
	if idx := strings.Index(m.JavaClass, "$"); idx >= 0 {
		return strings.ReplaceAll(m.JavaClass[:idx], "$", ".")
	}
	return m.JavaClassSource()
}

// JavaExtendsSource returns the superclass expression for the Java adapter.
func (m *MergedAbstractCallback) JavaExtendsSource() string {
	extends := m.javaClassSourceInImportedContext(m.JavaClass)
	if len(m.TypeParamBounds) == 0 {
		return extends
	}

	bounds := make([]string, 0, len(m.TypeParamBounds))
	for _, bound := range m.TypeParamBounds {
		if !isSafeJavaExtendsTypeArg(bound) {
			return extends
		}
		bounds = append(bounds, m.javaClassSourceInImportedContext(bound))
	}
	return extends + "<" + strings.Join(bounds, ", ") + ">"
}

// IsRecyclerViewViewHolderMinimalShim reports whether the adapter should be
// the exact RecyclerView.ViewHolder shim with a View constructor and no Go
// dispatch surface.
func (m *MergedAbstractCallback) IsRecyclerViewViewHolderMinimalShim() bool {
	return m.JavaClass == "androidx.recyclerview.widget.RecyclerView$ViewHolder"
}

// HasDispatchMethods reports whether generated methods should dispatch to Go.
func (m *MergedAbstractCallback) HasDispatchMethods() bool {
	return !m.IsRecyclerViewViewHolderMinimalShim()
}

func (m *MergedAbstractCallback) javaClassSourceInImportedContext(javaClass string) string {
	if idx := strings.Index(javaClass, "$"); idx >= 0 {
		outerClass := javaClass[:idx]
		if outerClass == m.importedOuterClass() {
			pkgIdx := strings.LastIndex(outerClass, ".")
			if pkgIdx >= 0 {
				return strings.ReplaceAll(javaClass[pkgIdx+1:], "$", ".")
			}
			return strings.ReplaceAll(javaClass, "$", ".")
		}
	}
	return strings.ReplaceAll(javaClass, "$", ".")
}

func (m *MergedAbstractCallback) importedOuterClass() string {
	if idx := strings.Index(m.JavaClass, "$"); idx >= 0 {
		return m.JavaClass[:idx]
	}
	return m.JavaClass
}

func isSafeJavaExtendsTypeArg(javaClass string) bool {
	javaClass = strings.TrimSpace(javaClass)
	if javaClass == "" {
		return false
	}
	if strings.Contains(javaClass, "<") || strings.Contains(javaClass, ">") ||
		strings.Contains(javaClass, "?") || strings.Contains(javaClass, " & ") {
		return false
	}
	for _, token := range javaTypeArgTokens(javaClass) {
		if len(token) == 1 && token[0] >= 'A' && token[0] <= 'Z' {
			return false
		}
	}
	return true
}

func javaTypeArgTokens(javaClass string) []string {
	return strings.FieldsFunc(javaClass, func(r rune) bool {
		return !isJavaTypeArgTokenRune(r)
	})
}

func isJavaTypeArgTokenRune(r rune) bool {
	return r == '_' || r == '$' || r == '.' ||
		r >= '0' && r <= '9' ||
		r >= 'A' && r <= 'Z' ||
		r >= 'a' && r <= 'z'
}

// AdapterClassName returns the Java adapter class name for this abstract callback.
// The name uses the Java simple class name (not the Go type) with an "Adapter"
// suffix (e.g. "ScanCallbackAdapter" for "android.bluetooth.le.ScanCallback").
func (m *MergedAbstractCallback) AdapterClassName() string {
	return m.JavaSimpleName() + "Adapter"
}

// AdapterPackageName returns the generated adapter package name.
func (m *MergedAbstractCallback) AdapterPackageName() string {
	javaPackage := m.JavaPackage()
	if javaPackage == "" {
		return generatedAdapterPackageName
	}
	return generatedAdapterPackageName + "." + javaPackage
}

// AdapterJavaName returns the generated adapter binary class name.
func (m *MergedAbstractCallback) AdapterJavaName() string {
	return m.AdapterPackageName() + "." + m.AdapterClassName()
}

// AdapterJNIName returns the generated adapter class name in JNI slash form.
func (m *MergedAbstractCallback) AdapterJNIName() string {
	return strings.ReplaceAll(m.AdapterJavaName(), ".", "/")
}

// JavaSimpleName returns the simple (unqualified) Java class name
// (e.g. "ScanCallback" from "android.bluetooth.le.ScanCallback").
func (m *MergedAbstractCallback) JavaSimpleName() string {
	for i := len(m.JavaClass) - 1; i >= 0; i-- {
		if m.JavaClass[i] == '.' {
			return m.JavaClass[i+1:]
		}
	}
	return m.JavaClass
}

// JavaPackage returns the Java package of the abstract class
// (e.g. "android.bluetooth.le" from "android.bluetooth.le.ScanCallback").
func (m *MergedAbstractCallback) JavaPackage() string {
	for i := len(m.JavaClass) - 1; i >= 0; i-- {
		if m.JavaClass[i] == '.' {
			return m.JavaClass[:i]
		}
	}
	return ""
}
