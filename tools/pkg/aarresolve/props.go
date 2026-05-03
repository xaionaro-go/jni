package aarresolve

import (
	"fmt"
	"strings"
)

// maxExpandDepth caps recursive ${...} substitution to catch property cycles.
// Maven itself uses 30; 16 is plenty for AndroidX/Material POMs and keeps
// cycle detection cheap.
const maxExpandDepth = 16

// Expand replaces every ${...} token in s using props plus the project
// triplet (group, artifact, version). Recognized variables:
//
//   - ${project.groupId}    -> group
//   - ${project.artifactId} -> artifact
//   - ${project.version}    -> version
//   - any key in props
//
// Anything else returns an error naming the offending token. The expansion
// recurses through chained substitutions up to maxExpandDepth levels.
func Expand(s string, props map[string]string, group, artifact, version string) (string, error) {
	return expandWithDepth(s, props, group, artifact, version, 0)
}

func expandWithDepth(
	s string,
	props map[string]string,
	group, artifact, version string,
	depth int,
) (string, error) {
	if depth > maxExpandDepth {
		return "", fmt.Errorf("property expansion exceeded depth %d (cycle?) while expanding %q", maxExpandDepth, s)
	}
	if !strings.Contains(s, "${") {
		return s, nil
	}
	var out strings.Builder
	i := 0
	for i < len(s) {
		j := strings.Index(s[i:], "${")
		if j < 0 {
			out.WriteString(s[i:])
			break
		}
		out.WriteString(s[i : i+j])
		k := strings.Index(s[i+j:], "}")
		if k < 0 {
			return "", fmt.Errorf("unterminated ${ in %q", s)
		}
		name := s[i+j+2 : i+j+k]
		val, ok := lookupProperty(name, props, group, artifact, version)
		if !ok {
			return "", fmt.Errorf("unresolved property %q in %q", name, s)
		}
		expanded, err := expandWithDepth(val, props, group, artifact, version, depth+1)
		if err != nil {
			return "", err
		}
		out.WriteString(expanded)
		i = i + j + k + 1
	}
	return out.String(), nil
}

func lookupProperty(name string, props map[string]string, group, artifact, version string) (string, bool) {
	switch name {
	case "project.groupId":
		return group, true
	case "project.artifactId":
		return artifact, true
	case "project.version":
		return version, true
	}
	v, ok := props[name]
	return v, ok
}
