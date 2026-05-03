package specgen

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultJarSkipPrefixes is the set of dot-separated Java class-name
// prefixes that EnumerateClassesInJar excludes by default when called via
// EnumerateClassesInJarFiltered. These cover Kotlin runtime libraries and
// JetBrains/Google annotation packages that are pulled transitively by
// Material/AndroidX AARs but for which we do not need Go bindings.
var DefaultJarSkipPrefixes = []string{
	"kotlin.",
	"kotlinx.",
	"org.intellij.",
	"org.jetbrains.",
	"com.google.errorprone.",
	"javax.inject.",
	"com.google.j2objc.",
}

// EnumerateClassesInJar lists every class entry inside a JAR. The returned
// names are dot-separated with the .class suffix stripped (e.g.
// "com.example.Foo$Inner"). META-INF/* and module-info.class are skipped.
// $-suffixed inner classes are returned alongside their parent; the caller
// groups them using the className string.
func EnumerateClassesInJar(jarPath string) ([]string, error) {
	return EnumerateClassesInJarFiltered(jarPath, nil)
}

// EnumerateClassesInJarFiltered behaves like EnumerateClassesInJar but
// additionally omits any class whose dot-separated name starts with any
// entry in skipPrefixes. Pass nil to keep all classes.
func EnumerateClassesInJarFiltered(jarPath string, skipPrefixes []string) ([]string, error) {
	zr, err := zip.OpenReader(jarPath)
	if err != nil {
		return nil, fmt.Errorf("open jar %s: %w", jarPath, err)
	}
	defer func() { _ = zr.Close() }()

	var out []string
	for _, f := range zr.File {
		name := f.Name
		switch {
		case !strings.HasSuffix(name, ".class"):
			continue
		case strings.HasPrefix(name, "META-INF/"):
			continue
		case name == "module-info.class":
			continue
		}
		cls := strings.TrimSuffix(name, ".class")
		cls = strings.ReplaceAll(cls, "/", ".")
		if hasAnyPrefix(cls, skipPrefixes) {
			continue
		}
		out = append(out, cls)
	}
	return out, nil
}

// hasAnyPrefix reports whether s begins with any entry in prefixes.
// Empty prefixes are ignored to avoid an accidental match-all.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if p == "" {
			continue
		}
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// FindClassesJars walks dir and returns absolute paths to every classes.jar
// file beneath it. Used to discover AAR-extracted JARs in the cycle 3
// .aar-cache/extracted/ tree.
func FindClassesJars(dir string) ([]string, error) {
	var out []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Base(path) != "classes.jar" {
			return nil
		}
		abs, absErr := filepath.Abs(path)
		if absErr != nil {
			return absErr
		}
		out = append(out, abs)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	return out, nil
}
