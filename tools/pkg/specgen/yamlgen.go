package specgen

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

const (
	dirPerm  = 0o755
	filePerm = 0o644
)

// AndroidServiceName maps known manager class names to their Android
// Context.getSystemService() constant names. Populated at runtime by
// LoadServiceNames, which reflects on android.jar via the svcgen Java tool.
var AndroidServiceName map[string]string

// loadMappingsFromSpecs reads all existing YAML spec files from dir and
// builds a map from Java class name → PackageMapping. This replaces the
// manual knownMappings table: when a class was already generated into a
// spec, its mapping is preserved automatically on re-generation.
func loadMappingsFromSpecs(dir string) (map[string]PackageMapping, error) {
	result := make(map[string]PackageMapping)
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return result, err
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var spec SpecFile
		if err := yaml.Unmarshal(data, &spec); err != nil {
			continue
		}
		if spec.Package == "" || spec.GoImport == "" {
			continue
		}
		for _, cls := range spec.Classes {
			if cls.JavaClass == "" {
				continue
			}
			result[cls.JavaClass] = PackageMapping{
				Package:  spec.Package,
				GoImport: spec.GoImport,
			}
		}
	}
	return result, nil
}

// GenerateSpec generates a YAML spec from .class files in a directory
// by running javap on each class.
func GenerateSpec(
	classPath string,
	className string,
	pkgMapping PackageMapping,
	goModule string,
) (*SpecFile, error) {
	jc, err := RunJavap(classPath, className)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", className, err)
	}

	spec := &SpecFile{
		Package:  pkgMapping.Package,
		GoImport: pkgMapping.GoImport,
	}

	cls := classFromJavap(jc, pkgMapping.Package)
	spec.Classes = append(spec.Classes, cls)

	if acb := abstractCallbackFromJavap(jc, pkgMapping.Package); acb != nil {
		spec.AbstractCallbacks = append(spec.AbstractCallbacks, *acb)
	}

	return spec, nil
}

// GenerateFromRefDir is a backward-compatible wrapper around
// GenerateFromSources that only walks refDir for .class files.
//
// Deprecated: use GenerateFromSources, which can additionally enumerate
// classes from JARs under jarsDir (e.g. AAR-extracted classes.jar trees).
func GenerateFromRefDir(
	refDir string,
	extraClassPath string,
	outputDir string,
	goModule string,
) error {
	return GenerateFromSources(refDir, "", extraClassPath, outputDir, goModule)
}

// GenerateFromSources scans refDir for .class files and (optionally) jarsDir
// for classes.jar files, then generates one YAML spec per top-level class
// (inner classes are grouped with their parent). extraClassPath is appended
// to the javap -cp argument so referenced types resolve. jarsDir may be ""
// to preserve the legacy ref-only behavior.
func GenerateFromSources(
	refDir string,
	jarsDir string,
	extraClassPath string,
	outputDir string,
	goModule string,
) error {
	// Load service name mappings from android.jar via the svcgen Java tool,
	// so that classFromJavap can detect system-service classes.
	if extraClassPath != "" {
		svcNames, err := LoadServiceNames(extraClassPath)
		if err != nil {
			return fmt.Errorf("load service names: %w", err)
		}
		AndroidServiceName = svcNames
	}

	var classFiles []string
	err := filepath.Walk(refDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".class") {
			classFiles = append(classFiles, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", refDir, err)
	}

	// Separate top-level classes from inner classes.
	type classEntry struct {
		className string
	}
	topLevel := make(map[string]classEntry)       // parent class → entry
	innerClasses := make(map[string][]classEntry) // parent class → inner entries

	addClass := func(className string) {
		entry := classEntry{className: className}
		if idx := strings.LastIndex(className, "$"); idx >= 0 {
			parent := className[:idx]
			innerClasses[parent] = append(innerClasses[parent], entry)
			return
		}
		topLevel[className] = entry
	}

	refCount := 0
	for _, cf := range classFiles {
		rel, _ := filepath.Rel(refDir, cf)
		className := strings.TrimSuffix(rel, ".class")
		className = strings.ReplaceAll(className, "/", ".")
		addClass(className)
		refCount++
	}

	// Pull in classes from JAR files (e.g. AAR-extracted classes.jar) when
	// requested. Each JAR is also added to the javap classpath below.
	var jarPaths []string
	jarCount := 0
	if jarsDir != "" {
		jars, jerr := FindClassesJars(jarsDir)
		if jerr != nil {
			return fmt.Errorf("find classes.jar under %s: %w", jarsDir, jerr)
		}
		jarPaths = jars
		for _, jar := range jars {
			names, nerr := EnumerateClassesInJar(jar)
			if nerr != nil {
				return fmt.Errorf("enumerate %s: %w", jar, nerr)
			}
			for _, name := range names {
				addClass(name)
				jarCount++
			}
		}
	}

	fmt.Fprintf(os.Stderr,
		"specgen: %d classes from refDir (%s), %d classes from %d jars under %q\n",
		refCount, refDir, jarCount, len(jarPaths), jarsDir)

	if err := os.MkdirAll(outputDir, dirPerm); err != nil {
		return fmt.Errorf("mkdir %s: %w", outputDir, err)
	}

	// Load mappings from existing spec files so that previously
	// generated class→package assignments are preserved automatically.
	existingMappings, err := loadMappingsFromSpecs(outputDir)
	if err != nil {
		return fmt.Errorf("load existing specs: %w", err)
	}

	cpParts := []string{refDir}
	cpParts = append(cpParts, jarPaths...)
	if extraClassPath != "" {
		cpParts = append(cpParts, extraClassPath)
	}
	cp := strings.Join(cpParts, ":")

	// Accumulate specs per Go import path so that classes from different
	// Java packages that share the same Go package name (e.g.,
	// "android.credentials.*" and "android.service.credentials.*") end up
	// in separate spec files and Go packages.
	specs := make(map[string]*SpecFile) // key: GoImport path
	var specsMu sync.Mutex

	// Bound concurrency to GOMAXPROCS — javap is CPU-bound (each invocation
	// forks an OpenJDK process). The work for a top-level class is independent
	// of every other class, so the loop parallelises naturally.
	workers := runtime.GOMAXPROCS(0)
	if workers < 2 {
		workers = 2
	}
	type job struct{ parentName string }
	jobs := make(chan job, workers*2)
	var wg sync.WaitGroup

	processOne := func(parentName string) {
		mapping := inferClassMapping(parentName, goModule, existingMappings)

		// Parse the top-level class. Skip non-public classes (annotations,
		// package-private types) that cannot be used via JNI.
		jc, err := RunJavap(cp, parentName)
		if err != nil {
			return
		}
		cls := classFromJavap(jc, mapping.Package)
		acb := abstractCallbackFromJavap(jc, mapping.Package)

		specsMu.Lock()
		spec, ok := specs[mapping.GoImport]
		if !ok {
			spec = &SpecFile{
				Package:  mapping.Package,
				GoImport: mapping.GoImport,
			}
			specs[mapping.GoImport] = spec
		}
		spec.Classes = append(spec.Classes, cls)
		addConstants(spec, jc)
		if acb != nil {
			spec.AbstractCallbacks = append(spec.AbstractCallbacks, *acb)
		}
		specsMu.Unlock()

		// Parse inner classes (also potentially expensive — same parallelism).
		for _, inner := range innerClasses[parentName] {
			ijc, err := RunJavap(cp, inner.className)
			if err != nil {
				continue
			}
			icls := classFromJavap(ijc, mapping.Package)
			iacb := abstractCallbackFromJavap(ijc, mapping.Package)

			specsMu.Lock()
			spec.Classes = append(spec.Classes, icls)
			addConstants(spec, ijc)
			if iacb != nil {
				spec.AbstractCallbacks = append(spec.AbstractCallbacks, *iacb)
			}
			specsMu.Unlock()
		}
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				processOne(j.parentName)
			}
		}()
	}

	progress := 0
	total := len(topLevel)
	fmt.Fprintf(os.Stderr, "specgen: processing %d top-level classes with %d workers\n", total, workers)
	for parentName := range topLevel {
		jobs <- job{parentName: parentName}
		progress++
		if progress%500 == 0 {
			fmt.Fprintf(os.Stderr, "specgen: queued %d/%d\n", progress, total)
		}
	}
	close(jobs)
	wg.Wait()

	// Build a map from package name to list of GoImport paths so we can
	// detect when multiple Go import paths share the same package name
	// and need disambiguated filenames.
	pkgImports := make(map[string][]string) // package name → []GoImport
	for goImport, spec := range specs {
		pkgImports[spec.Package] = append(pkgImports[spec.Package], goImport)
	}

	fmt.Fprintf(os.Stderr, "specgen: writing %d spec files to %s\n", len(specs), outputDir)
	for _, spec := range specs {
		spec.Classes = deduplicateGoTypes(spec.Classes)
		spec.Constants = deduplicateConstants(spec.Constants)

		// Use the package name as the spec filename. When multiple
		// Go import paths share the same package name, disambiguate
		// by using the full relative import path (with "/" → "_").
		specName := spec.Package
		if len(pkgImports[spec.Package]) > 1 {
			relPath := strings.TrimPrefix(spec.GoImport, goModule+"/")
			specName = strings.ReplaceAll(relPath, "/", "_")
		}
		outPath := filepath.Join(outputDir, specName+".yaml")
		fmt.Fprintf(os.Stderr, "  -> %s (classes=%d)\n", outPath, len(spec.Classes))
		if err := writeSpecFile(spec, outPath); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}
	}

	return nil
}

func addConstants(spec *SpecFile, jc *JavapClass) {
	for _, c := range jc.Constants {
		spec.Constants = append(spec.Constants, SpecConstant{
			GoName: javaConstantToGoName(c.Name),
			Value:  formatConstantValue(c),
			GoType: javaTypeToGoType(c.JavaType),
		})
	}
}

// formatConstantValue returns a YAML-ready representation of the constant's
// value. If javap provided a ConstantValue attribute, that is used; otherwise
// a type-appropriate placeholder is returned.
func formatConstantValue(c JavapConstant) string {
	if c.Value == "" {
		return inferConstantDefault(c.JavaType)
	}
	switch c.JavaType {
	case "java.lang.String":
		return strconv.Quote(c.Value)
	case "boolean":
		if c.Value == "1" || strings.EqualFold(c.Value, "true") {
			return "true"
		}
		return "false"
	case "long":
		// javap outputs long values with a trailing "l" suffix (e.g. "86400000l")
		// which is not valid Go syntax — strip it.
		return strings.TrimSuffix(c.Value, "l")
	case "float":
		// javap outputs float values with a trailing "f" suffix (e.g. "-4.0f", "NaNf")
		// which is not valid Go syntax — strip it.
		return strings.TrimSuffix(c.Value, "f")
	default:
		return c.Value
	}
}

func classFromJavap(jc *JavapClass, goPkg string) SpecClass {
	cls := SpecClass{
		JavaClass: jc.FullName,
		GoType:    inferGoType(jc.FullName, goPkg),
	}

	// Determine obtain type.
	if svcName, ok := AndroidServiceName[jc.FullName]; ok && svcName != "" {
		cls.Obtain = "system_service"
		cls.ServiceName = svcName
		cls.Close = true
	}

	// If the class has public constructors and isn't already a system
	// service, mark it as constructor-obtainable. Choose the best
	// constructor: prefer one that takes android.content.Context as
	// first param, then fall back to the no-arg constructor, then
	// the first available constructor.
	if cls.Obtain == "" && !jc.IsAbstract && !jc.IsInterface && len(jc.Constructors) > 0 {
		best := chooseBestConstructor(jc.Constructors)
		cls.Obtain = "constructor"
		cls.ConstructorParams = convertConstructorParams(best)
	}

	// Count method names to detect overloads.
	nameCounts := make(map[string]int)
	for _, m := range jc.Methods {
		if hasUnsupportedParams(m) {
			continue
		}
		nameCounts[m.Name]++
	}

	// Track per-name occurrence index for disambiguation.
	nameIndex := make(map[string]int)

	for _, m := range jc.Methods {
		if hasUnsupportedParams(m) {
			continue
		}

		sm := specMethodFromJavap(m)

		// Disambiguate overloaded methods by appending parameter count.
		if nameCounts[m.Name] > 1 {
			idx := nameIndex[m.Name]
			nameIndex[m.Name] = idx + 1
			suffix := fmt.Sprintf("%d", len(m.Params))
			if idx > 0 {
				suffix = fmt.Sprintf("%d_%d", len(m.Params), idx)
			}
			sm.GoName = sm.GoName + suffix
		}

		switch {
		case m.IsStatic:
			cls.StaticMethods = append(cls.StaticMethods, sm)
		default:
			cls.Methods = append(cls.Methods, sm)
		}
	}

	return cls
}

// goVetMethodConflicts lists method names that conflict with well-known
// Go stdlib interface methods (io.ByteReader, io.ByteWriter). These
// cause "should have signature" vet errors when the generated method
// has different params or return types. Always suffix to avoid.
var goVetMethodConflicts = map[string]bool{
	"ReadByte":  true,
	"WriteByte": true,
}

func specMethodFromJavap(m JavapMethod) SpecMethod {
	goName := javaMethodToGoName(m.Name)
	if goVetMethodConflicts[goName] {
		goName += "Value"
	}
	sm := SpecMethod{
		JavaMethod: m.Name,
		GoName:     goName,
		Returns:    javaTypeToSpecType(m.ReturnType),
		Error:      true,
	}

	for i, p := range m.Params {
		sm.Params = append(sm.Params, SpecParam{
			JavaType: javaTypeToSpecType(p.JavaType),
			GoName:   fmt.Sprintf("arg%d", i),
		})
	}

	return sm
}

// hasUnsupportedParams checks if a method has parameter types that can't
// be represented in the YAML spec (byte buffers, handlers, complex generics).
// isTypeVariable returns true if the type string is an unresolved generic
// type variable (single uppercase letter, optionally with [] suffix).
func isTypeVariable(t string) bool {
	t = strings.TrimSuffix(t, "[]")
	return len(t) == 1 && t[0] >= 'A' && t[0] <= 'Z'
}

func hasUnsupportedParams(m JavapMethod) bool {
	if isTypeVariable(m.ReturnType) {
		return true
	}
	for _, p := range m.Params {
		switch {
		case strings.Contains(p.JavaType, "ByteBuffer"):
			return true
		case strings.Contains(p.JavaType, "Handler"):
			return true
		case strings.Contains(p.JavaType, "<"):
			return true
		case isTypeVariable(p.JavaType):
			return true
		}
	}
	return false
}

// inferClassMapping derives the Go package name from a single Java class name.
// It first checks existing spec files (loaded into existingMappings) so that
// previously generated class→package assignments are preserved. Falls back to
// prefix-based heuristics for new classes.
func inferClassMapping(
	className string,
	goModule string,
	existingMappings map[string]PackageMapping,
) PackageMapping {
	if m, ok := existingMappings[className]; ok {
		return m
	}
	return inferPackageMapping(className, goModule)
}

func inferPackageMapping(className string, goModule string) PackageMapping {
	// Derive Go package from the Java package hierarchy.
	// E.g., "android.app.appsearch.AppSearchManager" → package "appsearch",
	//        go_import ".../app/appsearch"
	//
	// Strip "android." prefix, then use the Java package segments as the
	// Go import path. The Go package name is the last segment.
	parts := strings.Split(className, ".")
	if len(parts) < 3 {
		pkg := parts[len(parts)-2]
		return PackageMapping{
			Package:  pkg,
			GoImport: goModule + "/" + pkg,
		}
	}

	// Java package = everything except the class name (last segment).
	javaPkg := parts[:len(parts)-1] // e.g., [android, app, appsearch]

	// Strip "android" prefix for Go path.
	goSegments := javaPkg
	if goSegments[0] == "android" {
		goSegments = goSegments[1:]
	}

	goPath := strings.Join(goSegments, "/")
	pkg := strings.ReplaceAll(goSegments[len(goSegments)-1], "-", "_")

	return PackageMapping{
		Package:  pkg,
		GoImport: goModule + "/" + goPath,
	}
}

// deduplicateGoTypes detects go_type collisions within a spec and
// renames colliding entries by restoring their full (unstripped) Java
// class name. For example, if both "IkeSaProposal" and "SaProposal"
// in package "ike" map to go_type "SaProposal", the former is renamed
// to "IkeSaProposal".
func deduplicateGoTypes(classes []SpecClass) []SpecClass {
	// Count occurrences of each go_type.
	counts := make(map[string]int, len(classes))
	for _, c := range classes {
		counts[c.GoType]++
	}

	// For each collision, regenerate go_type without prefix stripping
	// (i.e., use just the simple class name).
	for i := range classes {
		if counts[classes[i].GoType] <= 1 {
			continue
		}
		parts := strings.Split(classes[i].JavaClass, ".")
		name := parts[len(parts)-1]
		// Handle inner classes: Foo$Bar → FooBar.
		name = strings.ReplaceAll(name, "$", "")
		classes[i].GoType = name
	}
	return classes
}

// deduplicateConstants removes duplicate constants (by GoName) that
// arise when multiple Java classes in the same Go package export
// identically-named constants (e.g. CREATOR on Parcelable classes).
func deduplicateConstants(constants []SpecConstant) []SpecConstant {
	seen := make(map[string]struct{}, len(constants))
	result := make([]SpecConstant, 0, len(constants))
	for _, c := range constants {
		if _, ok := seen[c.GoName]; ok {
			continue
		}
		seen[c.GoName] = struct{}{}
		result = append(result, c)
	}
	return result
}

const generatedFileHeader = "# Code generated by specgen. DO NOT EDIT.\n" +
	"# To change this file, modify the generator at tools/cmd/specgen/\n" +
	"# or the ref/ class files, then run: make specs\n\n"

func writeSpecFile(spec *SpecFile, path string) error {
	data, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	var out []byte
	out = append(out, generatedFileHeader...)
	out = append(out, data...)
	return os.WriteFile(path, out, filePerm)
}

// chooseBestConstructor picks the most appropriate constructor from a
// list of parsed constructors. Preference order:
//  1. Constructor with android.content.Context as first param.
//  2. No-arg constructor.
//  3. First constructor in the list.
func chooseBestConstructor(ctors []JavapConstructor) JavapConstructor {
	// Pass 1: prefer a constructor with Context as first param.
	for _, c := range ctors {
		if len(c.Params) > 0 && c.Params[0].JavaType == "android.content.Context" {
			return c
		}
	}
	// Pass 2: prefer a no-arg constructor.
	for _, c := range ctors {
		if len(c.Params) == 0 {
			return c
		}
	}
	// Fallback: first constructor.
	return ctors[0]
}

// convertConstructorParams converts JavapParams from a constructor
// into SpecParam entries for the YAML spec.
func convertConstructorParams(ctor JavapConstructor) []SpecParam {
	if len(ctor.Params) == 0 {
		return nil
	}
	params := make([]SpecParam, 0, len(ctor.Params))
	for i, p := range ctor.Params {
		params = append(params, SpecParam{
			JavaType: javaTypeToSpecType(p.JavaType),
			GoName:   fmt.Sprintf("arg%d", i),
		})
	}
	return params
}

// ---- Name conversion helpers ----

// javaTypeToSpecType converts a fully-qualified Java type to the
// short form used in YAML specs.
func javaTypeToSpecType(jt string) string {
	switch jt {
	case "void":
		return "void"
	case "boolean":
		return "boolean"
	case "byte":
		return "byte"
	case "char":
		return "char"
	case "short":
		return "short"
	case "int":
		return "int"
	case "long":
		return "long"
	case "float":
		return "float"
	case "double":
		return "double"
	case "java.lang.String":
		return "String"
	case "java.lang.CharSequence":
		return "java.lang.CharSequence"
	case "byte[]":
		return "[B"
	case "int[]":
		return "[I"
	case "long[]":
		return "[J"
	default:
		return jt
	}
}

// javaTypeToGoType converts a Java type name to a Go type for constants.
func javaTypeToGoType(jt string) string {
	switch jt {
	case "int":
		return "int"
	case "long":
		return "int64"
	case "java.lang.String":
		return "string"
	case "boolean":
		return "bool"
	case "float":
		return "float32"
	case "double":
		return "float64"
	default:
		return "int"
	}
}

// javaMethodToGoName converts a Java method name (camelCase) to a Go
// exported name (PascalCase), with raw suffix for complex methods.
func javaMethodToGoName(name string) string {
	if len(name) == 0 {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// inferGoType determines the exported Go type name for a Java class.
// It strips the Go package name prefix when redundant (e.g.,
// "AlarmManager" in package "alarm" becomes "Manager").
func inferGoType(fullClass string, goPkg string) string {
	parts := strings.Split(fullClass, ".")
	name := parts[len(parts)-1]

	// Handle inner classes: Foo$Bar → FooBar (include parent for uniqueness).
	if idx := strings.LastIndex(name, "$"); idx >= 0 {
		parent := name[:idx]
		child := name[idx+1:]
		name = parent + child
	}

	// Strip Go package name prefix when redundant (e.g.,
	// "AlarmManager" in package "alarm" → "Manager").
	if len(goPkg) > 0 {
		prefix := strings.ToUpper(goPkg[:1]) + goPkg[1:]
		if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
			name = name[len(prefix):]
		}
	}

	return name
}

// javaConstantToGoName converts SCREAMING_SNAKE_CASE to PascalCase.
func javaConstantToGoName(name string) string {
	parts := strings.Split(strings.ToLower(name), "_")
	for i := range parts {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// abstractCallbackFromJavap builds a SpecAbstractCallback for an abstract
// class. Android abstract callback classes (e.g. ScanCallback,
// BluetoothGattCallback) typically have concrete methods with empty bodies
// rather than truly abstract methods, so all non-static public methods are
// treated as overridable callback methods.
//
// Returns nil if the class is not abstract, is an interface, or has no methods.
func abstractCallbackFromJavap(jc *JavapClass, goPkg string) *SpecAbstractCallback {
	if !jc.IsAbstract || jc.IsInterface {
		return nil
	}

	var methods []SpecAbstractCallbackMethod
	for _, m := range jc.Methods {
		if m.IsStatic {
			continue
		}
		if hasUnsupportedParams(m) {
			continue
		}

		acm := SpecAbstractCallbackMethod{
			JavaMethod: m.Name,
			Returns:    javaTypeToSpecType(m.ReturnType),
			GoField:    javaMethodToGoName(m.Name),
		}
		for _, p := range m.Params {
			acm.Params = append(acm.Params, javaTypeToSpecType(p.JavaType))
		}
		methods = append(methods, acm)
	}

	if len(methods) == 0 {
		return nil
	}

	return &SpecAbstractCallback{
		JavaClass: jc.FullName,
		GoType:    inferGoType(jc.FullName, goPkg) + "Callback",
		Methods:   methods,
	}
}

// inferConstantDefault returns a placeholder default for a constant.
// The actual values come from the Android SDK; we use 0/"" as placeholders.
func inferConstantDefault(javaType string) string {
	switch javaType {
	case "java.lang.String":
		return `""`
	default:
		return "0"
	}
}
