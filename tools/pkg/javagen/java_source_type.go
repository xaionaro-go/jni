package javagen

import "strings"

var descriptorPrimitiveSourceTypes = map[byte]string{
	'Z': "boolean",
	'B': "byte",
	'C': "char",
	'S': "short",
	'I': "int",
	'J': "long",
	'F': "float",
	'D': "double",
}

func javaSourceType(javaType string) string {
	if sourceType, ok := descriptorArrayJavaSourceType(javaType); ok {
		return sourceType
	}
	return strings.ReplaceAll(javaType, "$", ".")
}

func descriptorArrayJavaSourceType(javaType string) (string, bool) {
	if !strings.HasPrefix(javaType, "[") || strings.HasSuffix(javaType, "[]") {
		return "", false
	}

	dimensions := 0
	for dimensions < len(javaType) && javaType[dimensions] == '[' {
		dimensions++
	}
	if dimensions == len(javaType) {
		return "", false
	}

	elementDescriptor := javaType[dimensions:]
	var elementType string
	switch {
	case len(elementDescriptor) == 1:
		var ok bool
		elementType, ok = descriptorPrimitiveSourceTypes[elementDescriptor[0]]
		if !ok {
			return "", false
		}
	case strings.HasPrefix(elementDescriptor, "L") && strings.HasSuffix(elementDescriptor, ";"):
		elementType = strings.TrimSuffix(strings.TrimPrefix(elementDescriptor, "L"), ";")
		elementType = strings.ReplaceAll(elementType, "/", ".")
		elementType = strings.ReplaceAll(elementType, "$", ".")
	default:
		return "", false
	}

	return elementType + strings.Repeat("[]", dimensions), true
}
