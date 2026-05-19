// Package specgen generates Java API YAML specs from .class files using javap.
package specgen

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var (
	// classLineRe matches the class declaration up to the class name. The
	// fourth group captures the class name (dots and `$` allowed for
	// nested classes). An optional generic-parameter block `<...>` may
	// follow the name; it is parsed separately because the bound types
	// can themselves be generic and the angle-bracket nesting is not
	// expressible as a regular expression.
	classLineRe     = regexp.MustCompile(`^public\s+(abstract\s+)?(final\s+)?(class|interface)\s+([\w.$]+)`)
	extendsRe       = regexp.MustCompile(`\sextends\s+([^\s{]+)`)
	implementsRe    = regexp.MustCompile(`implements\s+(.+?)\s*\{?\s*$`)
	constantRe      = regexp.MustCompile(`^\s+public static final\s+(\S+)\s+(\w+);`)
	constantValueRe = regexp.MustCompile(`^\s+ConstantValue:\s+(\S+)\s+(.+)$`)
	methodRe        = regexp.MustCompile(`^\s+(public|protected)\s+(static\s+)?(abstract\s+)?(final\s+)?(native\s+)?(\S+)\s+([\w$]+)\(([^)]*)\)(.*);\s*$`)
	constructorRe   = regexp.MustCompile(`^\s+(public|protected)\s+([\w.$]+)\(([^)]*)\)(.*);\s*$`)
	flagsRe         = regexp.MustCompile(`^\s+flags:\s+\(0x[0-9a-fA-F]+\)\s+(.+)$`)
)

// RunJavap executes javap and parses the output for a single class.
// classPath can contain multiple entries separated by ":".
// Uses -verbose to extract constant values from ConstantValue attributes.
func RunJavap(classPath string, className string) (*JavapClass, error) {
	var cmd *exec.Cmd
	switch {
	case classPath != "":
		cmd = exec.Command("javap", "-package", "-verbose", "-cp", classPath, className)
	default:
		// No classpath — javap uses the JDK's default (for java.* classes).
		cmd = exec.Command("javap", "-package", "-verbose", className)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("javap %s: %w", className, err)
	}
	return parseJavap(string(out))
}

func parseJavap(output string) (*JavapClass, error) {
	lines := strings.Split(output, "\n")
	jc := &JavapClass{}

	// lastConstIdx tracks the index of the most recently parsed constant
	// so that a subsequent ConstantValue line can be attached to it.
	lastConstIdx := -1
	lastMethodIdx := -1

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")

		// Parse class declaration.
		if m := classLineRe.FindStringSubmatch(line); m != nil {
			lastMethodIdx = -1
			jc.IsAbstract = strings.TrimSpace(m[1]) == "abstract"
			jc.IsFinal = strings.TrimSpace(m[2]) == "final"
			jc.IsInterface = m[3] == "interface"
			jc.FullName = m[4]
			// Extract a possible generic-parameter block immediately
			// following the class name. Angle brackets nest, so we scan
			// for the matching `>` instead of using a regex.
			rest := line[len(m[0]):]
			if strings.HasPrefix(rest, "<") {
				if end := matchAngle(rest); end > 0 {
					jc.TypeParams, jc.TypeParamOrder = parseTypeParams(rest[1:end])
					rest = rest[end+1:]
				}
			}
			if em := extendsRe.FindStringSubmatch(rest); em != nil {
				jc.SuperClass = em[1]
			}

			if im := implementsRe.FindStringSubmatch(rest); im != nil {
				for _, iface := range strings.Split(im[1], ",") {
					jc.Implements = append(jc.Implements, strings.TrimSpace(iface))
				}
			}
			continue
		}

		// Parse constants (public static final).
		if m := constantRe.FindStringSubmatch(line); m != nil {
			lastMethodIdx = -1
			jc.Constants = append(jc.Constants, JavapConstant{
				JavaType: m[1],
				Name:     m[2],
			})
			lastConstIdx = len(jc.Constants) - 1
			continue
		}

		// Parse ConstantValue attribute (follows field declaration in
		// javap -verbose output). Attaches the value to the preceding
		// constant field.
		if lastConstIdx >= 0 {
			if m := constantValueRe.FindStringSubmatch(line); m != nil {
				jc.Constants[lastConstIdx].Value = m[2]
				lastConstIdx = -1
				continue
			}
		}

		if lastMethodIdx >= 0 {
			if m := flagsRe.FindStringSubmatch(line); m != nil {
				applyJavapMethodFlags(&jc.Methods[lastMethodIdx], m[1])
				continue
			}
		}

		// Parse methods.
		if m := methodRe.FindStringSubmatch(line); m != nil {
			lastConstIdx = -1
			visibility := strings.TrimSpace(m[1])
			isStatic := strings.TrimSpace(m[2]) == "static"
			isAbstract := strings.TrimSpace(m[3]) == "abstract"
			isFinal := strings.TrimSpace(m[4]) == "final"
			retType := m[6]
			name := m[7]
			paramsStr := m[8]
			throwsStr := m[9]

			method := JavapMethod{
				Name:        name,
				ReturnType:  retType,
				IsPublic:    visibility == "public",
				IsProtected: visibility == "protected",
				IsStatic:    isStatic,
				IsAbstract:  isAbstract,
				IsFinal:     isFinal,
				Throws:      strings.Contains(throwsStr, "throws"),
				Params:      parseParams(paramsStr),
			}
			jc.Methods = append(jc.Methods, method)
			lastMethodIdx = len(jc.Methods) - 1
			continue
		}
		if isUnparsedAbstractMethodLine(line) {
			lastConstIdx = -1
			lastMethodIdx = -1
			jc.HasUnparsedAbstractMethods = true
			continue
		}
		if isPackagePrivateAbstractMethodLine(line) {
			lastConstIdx = -1
			lastMethodIdx = -1
			jc.HasPackagePrivateAbstractMethods = true
			continue
		}

		// Parse constructors.
		// A constructor line looks like: public fully.qualified.ClassName(params);
		// The constructorRe must be checked after methodRe to avoid
		// false-matching regular methods whose return type is a class name.
		if m := constructorRe.FindStringSubmatch(line); m != nil {
			lastConstIdx = -1
			lastMethodIdx = -1
			// Verify this is actually a constructor: the captured name
			// must equal the class name we already parsed.
			if m[2] == jc.FullName {
				visibility := strings.TrimSpace(m[1])
				paramsStr := m[3]
				jc.Constructors = append(jc.Constructors, JavapConstructor{
					Params:      parseParams(paramsStr),
					IsPublic:    visibility == "public",
					IsProtected: visibility == "protected",
				})
			}
			continue
		}
	}

	if jc.FullName == "" {
		return nil, fmt.Errorf("could not parse class name from javap output")
	}
	return jc, nil
}

func isUnparsedAbstractMethodLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.Contains(trimmed, " abstract ") &&
		strings.Contains(trimmed, "(") &&
		strings.HasSuffix(trimmed, ";")
}

func isPackagePrivateAbstractMethodLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "public ") || strings.HasPrefix(trimmed, "protected ") {
		return false
	}
	return strings.HasPrefix(trimmed, "abstract ") &&
		strings.Contains(trimmed, "(") &&
		strings.HasSuffix(trimmed, ";")
}

func applyJavapMethodFlags(method *JavapMethod, flags string) {
	for _, rawFlag := range strings.Split(flags, ",") {
		flag := strings.TrimSpace(rawFlag)
		switch flag {
		case "ACC_PUBLIC":
			method.IsPublic = true
		case "ACC_PROTECTED":
			method.IsProtected = true
		case "ACC_STATIC":
			method.IsStatic = true
		case "ACC_ABSTRACT":
			method.IsAbstract = true
		case "ACC_FINAL":
			method.IsFinal = true
		case "ACC_BRIDGE":
			method.IsBridge = true
		case "ACC_SYNTHETIC":
			method.IsSynthetic = true
		}
	}
}

func parseParams(s string) []JavapParam {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// Split on commas that are not inside angle brackets (generic type
	// parameters). A naive strings.Split(",") would break types like
	// Map<String, SessionConfiguration> into two separate params.
	parts := splitRespectingGenerics(s)
	params := make([]JavapParam, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		params = append(params, JavapParam{JavaType: p})
	}
	return params
}

// matchAngle returns the index of the `>` that matches the leading `<`
// in s, treating angle brackets as nestable. It returns 0 when s does
// not start with `<` or has no matching closer.
func matchAngle(s string) int {
	if len(s) == 0 || s[0] != '<' {
		return 0
	}
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return 0
}

// parseTypeParams parses the inside of a `<...>` generic-parameter block
// from a class declaration into a name → upper-bound map plus the
// declaration order. The bound keeps `$` for nested classes (e.g.
// `androidx.x.Y$Z`), matching the `$`-form FQN convention used
// elsewhere in specs. An unbounded parameter (`<T>`) defaults to
// `java.lang.Object`.
//
// Multiple parameters are separated by top-level commas:
// `<K extends Foo, V extends Bar<X, Y>>`. Commas nested inside angle
// brackets are not separators.
func parseTypeParams(s string) (map[string]string, []string) {
	parts := splitRespectingGenerics(s)
	if len(parts) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(parts))
	order := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		name, bound, hasBound := strings.Cut(p, " extends ")
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		switch {
		case hasBound:
			out[name] = strings.TrimSpace(bound)
		default:
			out[name] = "java.lang.Object"
		}
		order = append(order, name)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, order
}

// splitRespectingGenerics splits a parameter string on commas, but only
// those at the top level (depth == 0 relative to angle brackets). Commas
// inside generic types like Map<K, V> are preserved as part of the type.
func splitRespectingGenerics(s string) []string {
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}
