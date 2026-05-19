package javagen

import (
	"fmt"
	"strings"
)

// Merge combines a Spec and Overlay into a fully resolved MergedSpec.
func Merge(spec *Spec, overlay *Overlay) (*MergedSpec, error) {
	merged := &MergedSpec{
		Package:         spec.Package,
		GoImport:        spec.GoImport,
		JavaPackageDesc: deriveJavaPackageDesc(spec),
	}

	// Inject spec-level intent_extras into the first class (battery pattern).
	// Make a copy to avoid mutating the input spec.
	if len(spec.IntentExtras) > 0 && len(spec.Classes) > 0 {
		cls := spec.Classes[0]
		cls.IntentExtras = append(append([]IntentExtra(nil), cls.IntentExtras...), spec.IntentExtras...)
		spec.Classes[0] = cls
	}

	for _, cls := range spec.Classes {
		if cls.Kind == "data_class" {
			dc, err := mergeDataClass(&cls, overlay)
			if err != nil {
				return nil, fmt.Errorf("merge data class %s: %w", cls.GoType, err)
			}
			merged.DataClasses = append(merged.DataClasses, *dc)
			continue
		}

		// Promote static_fields to synthetic getter methods so they appear
		// as RPCs in protogen output.
		promoteStaticFieldsToMethods(&cls)

		// Promote intent_extras to synthetic getter methods.
		promoteIntentExtrasToMethods(&cls)

		// Inject toString() if missing so every wrapper type has a
		// Go-callable ToString() method (Object.toString is inherited
		// by all Java classes but specgen may not always include it).
		injectToStringIfMissing(&cls)

		mc, err := mergeClass(&cls, overlay)
		if err != nil {
			return nil, fmt.Errorf("merge class %s: %w", cls.GoType, err)
		}
		merged.Classes = append(merged.Classes, *mc)
	}

	for _, cb := range spec.Callbacks {
		mcb, err := mergeCallback(&cb, overlay)
		if err != nil {
			return nil, fmt.Errorf("merge callback %s: %w", cb.GoType, err)
		}
		merged.Callbacks = append(merged.Callbacks, *mcb)
	}

	eligibilityByClass := abstractCallbackEligibilityByClass(spec.AbstractCallbackEligibility)
	for _, acb := range spec.AbstractCallbacks {
		if eligibility, ok := eligibilityByClass[acb.JavaClass]; ok && !eligibility.Generated {
			return nil, fmt.Errorf(
				"abstract callback %s cannot be rendered: %s",
				acb.JavaClass,
				eligibility.Reason,
			)
		}
		macb, err := mergeAbstractCallback(&acb, overlay)
		if err != nil {
			return nil, fmt.Errorf("merge abstract callback %s: %w", acb.JavaClass, err)
		}
		merged.AbstractCallbacks = append(merged.AbstractCallbacks, *macb)
	}

	merged.ConstantGroups = mergeConstants(spec.Constants)

	resolveConstantTypeCollisions(merged)

	return merged, nil
}

// promoteStaticFieldsToMethods converts StaticFields to synthetic getter
// methods so they flow through protogen as RPCs (e.g. build.GetManufacturer).
func promoteStaticFieldsToMethods(cls *Class) {
	for _, sf := range cls.StaticFields {
		goName := "Get" + sf.GoName
		cls.Methods = append(cls.Methods, Method{
			JavaMethod: sf.JavaField,
			GoName:     goName,
			Static:     true,
			Returns:    sf.Returns,
			Error:      true,
		})
	}
}

// promoteIntentExtrasToMethods converts IntentExtras to synthetic getter
// methods so they flow through protogen as RPCs (e.g. battery.GetLevel).
func promoteIntentExtrasToMethods(cls *Class) {
	for _, ie := range cls.IntentExtras {
		if ie.GoName == "" {
			continue
		}
		goName := "Get" + strings.ToUpper(ie.GoName[:1]) + ie.GoName[1:]
		jt := ie.JavaType
		if jt == "" {
			jt = "int"
		}
		cls.Methods = append(cls.Methods, Method{
			JavaMethod: ie.JavaExtra,
			GoName:     goName,
			Static:     false,
			Returns:    jt,
			Error:      true,
		})
	}
}

// injectToStringIfMissing adds a synthetic toString method when the class
// does not already declare one. Every Java class inherits Object.toString(),
// so exposing it as ToString() in Go lets callers avoid raw JNI for the
// most common debugging/display operation.
func injectToStringIfMissing(cls *Class) {
	for _, m := range cls.Methods {
		if m.JavaMethod == "toString" {
			return
		}
	}
	for _, m := range cls.StaticMethods {
		if m.JavaMethod == "toString" {
			return
		}
	}
	cls.Methods = append(cls.Methods, Method{
		JavaMethod: "toString",
		GoName:     "ToString",
		Static:     false,
		Returns:    "String",
		Error:      true,
	})
}

func mergeClass(cls *Class, overlay *Overlay) (*MergedClass, error) {
	mc := &MergedClass{
		JavaClass:      cls.JavaClass,
		JavaClassSlash: JavaClassToSlash(cls.JavaClass),
		GoType:         cls.GoType,
		Obtain:         cls.Obtain,
		ServiceName:    cls.ServiceName,
		Kind:           cls.Kind,
		Close:          cls.Close,
	}

	// Compute constructor JNI signature from constructor_params.
	// Overlay constructor_overrides take precedence over spec-level params.
	if cls.Obtain == "constructor" {
		ctorParams := cls.ConstructorParams
		if overlay != nil {
			if co, ok := overlay.ConstructorOverrides[cls.JavaClass]; ok {
				ctorParams = co.Params
			}
		}
		mc.ConstructorJNISig = JNISignature(ctorParams, "void")
		for _, p := range ctorParams {
			mc.ConstructorParams = append(mc.ConstructorParams, mergeParam(p, overlay))
		}
	}

	methods := cls.AllMethods()
	if overlay != nil && len(overlay.ExtraMethods) > 0 {
		methods = append(methods, overlay.ExtraMethods...)
	}

	for _, m := range methods {
		mm, err := mergeMethod(&m, overlay)
		if err != nil {
			return nil, fmt.Errorf("method %s: %w", m.GoName, err)
		}
		mc.Methods = append(mc.Methods, *mm)
	}

	return mc, nil
}

func mergeMethod(m *Method, overlay *Overlay) (*MergedMethod, error) {
	goName := m.GoName
	if overlay != nil {
		if override, ok := overlay.GoNameOverrides[m.JavaMethod]; ok {
			goName = override
		}
	}

	retType := m.Returns
	if retType == "" {
		retType = "void"
	}

	retConv := ResolveType(retType)
	goReturn := retConv.GoType
	if overlay != nil {
		if override, ok := overlay.TypeOverrides[retType]; ok {
			goReturn = override
		}
	}

	retKind := classifyReturn(retType, retConv)

	var params []MergedParam
	for _, p := range m.Params {
		mp := mergeParam(p, overlay)
		params = append(params, mp)
	}

	jniSig := JNISignature(m.Params, retType)

	mm := &MergedMethod{
		JavaMethod: m.JavaMethod,
		GoName:     goName,
		Static:     m.Static,
		Params:     params,
		Returns:    retType,
		GoReturn:   goReturn,
		JNISig:     jniSig,
		CallSuffix: retConv.CallSuffix,
		ReturnKind: retKind,
		HasError:   m.Error,
	}

	isVoid := retKind == ReturnVoid
	mm.GoParamList = buildGoParamList(params)
	mm.GoParamListMultiLine = buildGoParamListMultiLine(params)
	mm.GoReturnList = buildGoReturnList(goReturn, isVoid, m.Error)
	mm.GoReturnVars = buildGoReturnVars(goReturn, isVoid)
	mm.GoReturnValues = buildGoReturnValues(goReturn, isVoid, m.Error)
	mm.JNIArgs = buildJNIArgs(params)

	return mm, nil
}

func mergeParam(p Param, overlay *Overlay) MergedParam {
	tc := ResolveType(p.JavaType)
	goType := p.GoType
	if goType == "" {
		goType = tc.GoType
	}
	if overlay != nil {
		if override, ok := overlay.TypeOverrides[p.JavaType]; ok && p.GoType == "" {
			goType = override
		}
	}

	isString := p.JavaType == "String" || p.JavaType == "java.lang.String" || p.JavaType == "CharSequence" || p.JavaType == "java.lang.CharSequence"
	isBool := p.JavaType == "boolean"

	// CharSequence parameters accept Go strings: String implements
	// CharSequence, so we create a JNI String and pass it. Override the
	// GoType that ResolveType returns (which is *jni.Object for
	// CharSequence) so the Go API accepts a plain string.
	if isString && goType != "string" && p.GoType == "" {
		goType = "string"
	}

	goName := sanitizeGoName(p.GoName)

	return MergedParam{
		JavaType: p.JavaType,
		GoName:   goName,
		GoType:   goType,
		IsString: isString,
		IsBool:   isBool,
		IsObject: tc.IsObject,
	}
}

// goKeywords is the set of Go reserved keywords that cannot be used as identifiers.
var goKeywords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
}

// sanitizeGoName appends an underscore to names that are Go reserved keywords.
func sanitizeGoName(name string) string {
	if goKeywords[name] {
		return name + "_"
	}
	return name
}

func mergeDataClass(cls *Class, _ *Overlay) (*MergedDataClass, error) {
	dc := &MergedDataClass{
		JavaClass:      cls.JavaClass,
		JavaClassSlash: JavaClassToSlash(cls.JavaClass),
		GoType:         cls.GoType,
	}

	for _, f := range cls.Fields {
		retConv := ResolveType(f.Returns)
		goType := f.GoType
		if goType == "" {
			goType = retConv.GoType
		}

		isField := f.JavaField != ""
		var javaMethod, javaName, jniSig string
		if isField {
			javaName = f.JavaField
			jniSig = JNITypeSignature(f.Returns) // field sig: no () wrapper
		} else {
			javaMethod = f.GetterName()
			jniSig = JNISignature(nil, f.Returns) // method sig: ()RetType
		}

		dc.Fields = append(dc.Fields, MergedField{
			JavaMethod: javaMethod,
			JavaName:   javaName,
			GoName:     f.GoName,
			GoType:     goType,
			JNISig:     jniSig,
			CallSuffix: retConv.CallSuffix,
			IsField:    isField,
		})
	}

	return dc, nil
}

func mergeCallback(cb *Callback, overlay *Overlay) (*MergedCallback, error) {
	mcb := &MergedCallback{
		JavaInterface: cb.JavaInterface,
		GoType:        cb.GoType,
	}

	for _, m := range cb.Methods {
		var params []MergedParam
		for i, jt := range m.Params {
			goName := fmt.Sprintf("arg%d", i)
			isString := jt == "String" || jt == "java.lang.String" || jt == "java.lang.CharSequence" || jt == "CharSequence"
			// Callback args arrive as autoboxed Object[] via JNI proxy.
			// Only strings get converted to Go string; everything else is *jni.Object.
			goType := "*jni.Object"
			if isString {
				goType = "string"
			}
			params = append(params, MergedParam{
				JavaType: jt,
				GoName:   goName,
				GoType:   goType,
				IsString: isString,
				IsObject: true,
			})
		}

		goParams := buildGoParamList(params)

		mcb.Methods = append(mcb.Methods, MergedCallbackMethod{
			JavaMethod: m.JavaMethod,
			GoField:    m.GoField,
			Params:     params,
			GoParams:   goParams,
		})
	}

	return mcb, nil
}

func abstractCallbackEligibilityByClass(
	eligibilities []AbstractCallbackEligibility,
) map[string]AbstractCallbackEligibility {
	out := make(map[string]AbstractCallbackEligibility, len(eligibilities))
	for _, eligibility := range eligibilities {
		if eligibility.JavaClass == "" {
			continue
		}
		out[eligibility.JavaClass] = eligibility
	}
	return out
}

func mergeAbstractCallback(
	acb *AbstractCallback,
	overlay *Overlay,
) (*MergedAbstractCallback, error) {
	if err := validateAbstractCallbackRenderable(acb); err != nil {
		return nil, err
	}

	macb := &MergedAbstractCallback{
		JavaClass:       acb.JavaClass,
		JavaClassSlash:  JavaClassToSlash(acb.JavaClass),
		GoType:          acb.GoType,
		TypeParamBounds: append([]string(nil), acb.TypeParamBounds...),
	}

	for _, m := range acb.Methods {
		var params []MergedParam
		for i, jt := range m.Params {
			goName := fmt.Sprintf("arg%d", i)
			isString := jt == "String" || jt == "java.lang.String" || jt == "java.lang.CharSequence" || jt == "CharSequence"
			goType := "*jni.Object"
			if isString {
				goType = "string"
			}
			params = append(params, MergedParam{
				JavaType: jt,
				GoName:   goName,
				GoType:   goType,
				IsString: isString,
				IsObject: true,
			})
		}

		goParams := buildGoParamList(params)

		macb.Methods = append(macb.Methods, MergedAbstractCallbackMethod{
			JavaMethod: m.JavaMethod,
			GoField:    m.GoField,
			Params:     params,
			GoParams:   goParams,
			Returns:    m.Returns,
		})
	}

	return macb, nil
}

func validateAbstractCallbackRenderable(acb *AbstractCallback) error {
	if acb.JavaClass == "" {
		return fmt.Errorf("missing java_class")
	}
	if acb.JavaClass == "androidx.recyclerview.widget.RecyclerView$ViewHolder" {
		return nil
	}
	if len(acb.Methods) == 0 {
		return fmt.Errorf("no dispatch methods")
	}
	for _, method := range acb.Methods {
		if strings.TrimSpace(method.JavaMethod) == "" {
			return fmt.Errorf("empty java method")
		}
		if err := validateAdapterSourceType(method.Returns, true); err != nil {
			return fmt.Errorf("method %s return type %q: %w", method.JavaMethod, method.Returns, err)
		}
		for _, param := range method.Params {
			if err := validateAdapterSourceType(param, false); err != nil {
				return fmt.Errorf("method %s param type %q: %w", method.JavaMethod, param, err)
			}
		}
	}
	return nil
}

func validateAdapterSourceType(javaType string, allowVoid bool) error {
	javaType = strings.TrimSpace(javaType)
	switch javaType {
	case "":
		if allowVoid {
			return nil
		}
		return fmt.Errorf("empty type")
	case "void":
		if allowVoid {
			return nil
		}
		return fmt.Errorf("void parameter")
	}
	if strings.Contains(javaType, " & ") {
		return fmt.Errorf("compound type cannot be rendered")
	}
	if strings.Count(javaType, "<") != strings.Count(javaType, ">") {
		return fmt.Errorf("malformed generic type")
	}
	base := javaType
	if idx := strings.Index(base, "<"); idx >= 0 {
		base = base[:idx]
	}
	for strings.HasSuffix(base, "[]") {
		base = strings.TrimSuffix(base, "[]")
	}
	if len(base) == 1 && base[0] >= 'A' && base[0] <= 'Z' {
		return fmt.Errorf("unresolved type variable")
	}
	return nil
}

// deriveJavaPackageDesc extracts the Java package name from the first
// class in the spec to produce a human-readable description for the
// doc.go package comment. Returns the Java package (e.g.
// "android.net.wifi") or the Go package name as fallback.
func deriveJavaPackageDesc(spec *Spec) string {
	for _, cls := range spec.Classes {
		if cls.JavaClass == "" {
			continue
		}
		// Extract Java package from fully-qualified class name.
		idx := strings.LastIndex(cls.JavaClass, ".")
		if idx > 0 {
			return cls.JavaClass[:idx]
		}
	}
	return spec.Package
}

// inferBaseType guesses the Go base type from a constant value literal.
// Quoted strings → "string", everything else → "int".
func inferBaseType(value string) string {
	if len(value) >= 2 && value[0] == '"' {
		return "string"
	}
	return "int"
}

// normalizeConstantValue converts a constant value from Java literal
// syntax to valid Go syntax. For example, Java float literals use an
// "f" suffix (e.g. "-4.0f", "NaNf") that must be stripped or converted
// for Go. Returns the normalized value and whether the value requires a
// var declaration instead of const (e.g. NaN is not a compile-time constant).
func normalizeConstantValue(value, baseType string) (normalized string, needsVar bool) {
	switch baseType {
	case "float32":
		return normalizeFloat32Value(value), isNaNValue(value)
	case "float64":
		return normalizeFloat64Value(value), isNaNValue(value)
	default:
		return value, false
	}
}

// normalizeFloat32Value converts a Java float literal to Go syntax.
// "-4.0f" → "-4.0", "NaNf" → "float32(math.NaN())",
// "Infinityf" → "float32(math.Inf(1))", "-Infinityf" → "float32(math.Inf(-1))".
func normalizeFloat32Value(value string) string {
	v := strings.TrimSuffix(value, "f")
	switch v {
	case "NaN":
		return "float32(math.NaN())"
	case "Infinity":
		return "float32(math.Inf(1))"
	case "-Infinity":
		return "float32(math.Inf(-1))"
	default:
		return v
	}
}

// normalizeFloat64Value converts a Java double literal to Go syntax.
// "NaN" → "math.NaN()", "Infinity" → "math.Inf(1)", etc.
func normalizeFloat64Value(value string) string {
	v := strings.TrimSuffix(value, "d")
	switch v {
	case "NaN":
		return "math.NaN()"
	case "Infinity":
		return "math.Inf(1)"
	case "-Infinity":
		return "math.Inf(-1)"
	default:
		return v
	}
}

// isNaNValue reports whether a constant value represents NaN or Infinity,
// which are not valid Go compile-time constants.
func isNaNValue(value string) bool {
	v := strings.TrimSuffix(value, "f")
	v = strings.TrimSuffix(v, "d")
	v = strings.TrimPrefix(v, "-")
	return v == "NaN" || v == "Infinity"
}

// isBuiltinType reports whether t is a Go builtin type that must not
// be used as a named type alias (e.g. "type string string" shadows the builtin).
func isBuiltinType(t string) bool {
	switch t {
	case "string", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"float32", "float64", "bool", "byte", "rune":
		return true
	}
	return false
}

// resolveConstantTypeCollisions detects constant GoNames that collide
// with type names (classes, data classes, callbacks, or constant group
// type aliases) in the same package and renames the constants by
// appending a "Const" suffix.
func resolveConstantTypeCollisions(merged *MergedSpec) {
	// Build a set of all type names in the package.
	typeNames := make(map[string]struct{})
	for _, cls := range merged.Classes {
		typeNames[cls.GoType] = struct{}{}
	}
	for _, dc := range merged.DataClasses {
		typeNames[dc.GoType] = struct{}{}
	}
	for _, cb := range merged.Callbacks {
		typeNames[cb.GoType] = struct{}{}
	}
	for _, acb := range merged.AbstractCallbacks {
		typeNames[acb.GoType] = struct{}{}
	}
	for _, grp := range merged.ConstantGroups {
		if grp.GoType != "" {
			typeNames[grp.GoType] = struct{}{}
		}
	}

	if len(typeNames) == 0 {
		return
	}

	// Rename colliding constants.
	for gi := range merged.ConstantGroups {
		for vi := range merged.ConstantGroups[gi].Values {
			name := merged.ConstantGroups[gi].Values[vi].GoName
			if _, collision := typeNames[name]; collision {
				merged.ConstantGroups[gi].Values[vi].GoName = name + "Const"
			}
		}
	}
}

func mergeConstants(constants []Constant) []MergedConstantGroup {
	if len(constants) == 0 {
		return nil
	}

	groups := make(map[string]*MergedConstantGroup)
	var order []string

	for _, c := range constants {
		key := c.GoType
		if key == "" {
			key = "_untyped"
		}
		g, ok := groups[key]
		if !ok {
			baseType := c.GoUnderlying
			if baseType == "" {
				baseType = c.GoType
			}
			// For untyped constants, infer the base type from the value
			// literal so the generated code has explicit types. When an
			// explicit builtin type is set (e.g. float32), preserve it.
			if baseType == "" {
				baseType = inferBaseType(c.Value)
			}
			g = &MergedConstantGroup{
				BaseType: baseType,
			}
			// Only create a named type for non-builtin types.
			// Builtins like "string" or "int" would shadow Go builtins.
			if !isBuiltinType(key) && key != "_untyped" {
				g.GoType = key
				// If BaseType == GoType (self-referential), infer from values.
				if g.BaseType == g.GoType {
					g.BaseType = inferBaseType(c.Value)
				}
			}
			groups[key] = g
			order = append(order, key)
		}
		normalizedValue, needsVar := normalizeConstantValue(c.Value, g.BaseType)
		g.Values = append(g.Values, MergedConstant{
			GoName: c.GoName,
			Value:  normalizedValue,
			IsVar:  needsVar,
		})
	}

	var result []MergedConstantGroup
	for _, key := range order {
		result = append(result, *groups[key])
	}
	return result
}

// multiLineParamThreshold is the minimum number of parameters that triggers
// multi-line formatting (each parameter on its own line).
const multiLineParamThreshold = 3

func buildGoParamList(params []MergedParam) string {
	var parts []string
	for _, p := range params {
		parts = append(parts, p.GoName+" "+p.GoType)
	}
	return strings.Join(parts, ", ")
}

// buildGoParamListMultiLine returns the parameter list formatted with each
// parameter on its own line (for functions with 3+ parameters).
// Returns empty string if there are fewer than multiLineParamThreshold params.
func buildGoParamListMultiLine(params []MergedParam) string {
	if len(params) < multiLineParamThreshold {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n")
	for _, p := range params {
		sb.WriteString("\t")
		sb.WriteString(p.GoName)
		sb.WriteString(" ")
		sb.WriteString(p.GoType)
		sb.WriteString(",\n")
	}
	return sb.String()
}

func buildGoReturnList(
	goReturn string,
	isVoid bool,
	hasError bool,
) string {
	switch {
	case isVoid && hasError:
		return "error"
	case isVoid && !hasError:
		return ""
	case !isVoid && hasError:
		return "(" + goReturn + ", error)"
	default:
		return goReturn
	}
}

func buildGoReturnVars(goReturn string, isVoid bool) string {
	if isVoid {
		return ""
	}
	return "var result " + goReturn
}

func buildGoReturnValues(
	goReturn string,
	isVoid bool,
	hasError bool,
) string {
	switch {
	case isVoid && hasError:
		return "callErr"
	case isVoid && !hasError:
		return ""
	case !isVoid && hasError:
		return "result, callErr"
	default:
		return "result"
	}
}

func classifyReturn(retType string, retConv TypeConv) ReturnKind {
	switch {
	case retType == "void":
		return ReturnVoid
	case retType == "String" || retType == "java.lang.String":
		return ReturnString
	case retType == "boolean":
		return ReturnBool
	case retConv.IsObject:
		return ReturnObject
	default:
		return ReturnPrimitive
	}
}

func buildJNIArgs(params []MergedParam) string {
	if len(params) == 0 {
		return ""
	}
	var parts []string
	for _, p := range params {
		tc := ResolveType(p.JavaType)
		valFunc := jniValueFunc(tc.CallSuffix)
		argName := p.GoName
		switch {
		case p.IsString:
			argName = "&j" + strings.Title(p.GoName) + ".Object" //nolint:staticcheck // strings.Title is fine here
		case p.IsBool:
			argName = "j" + strings.Title(p.GoName) //nolint:staticcheck // strings.Title is fine here
		}
		parts = append(parts, fmt.Sprintf("jni.%s(%s)", valFunc, argName))
	}
	return ", " + strings.Join(parts, ", ")
}
