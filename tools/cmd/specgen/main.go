package main

import (
	"flag"
	"log"
	"strings"

	"github.com/AndroidGoLab/jni/tools/pkg/specgen"
)

// stringList is a flag.Value implementation that accumulates repeated
// values from the command line into a string slice (e.g.
// `--skip-jar-prefix kotlin. --skip-jar-prefix kotlinx.`).
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	refDir := flag.String("ref", "ref", "directory containing reference .class files")
	jarsDir := flag.String("jars-dir", "", "directory tree containing classes.jar files (e.g., .aar-cache/extracted)")
	extraCP := flag.String("classpath", "", "additional classpath for javap (e.g. android.jar)")
	outputDir := flag.String("output", "spec/java", "output directory for generated YAML specs")
	goModule := flag.String("go-module", "github.com/AndroidGoLab/jni", "Go module path")

	var skipPrefixes stringList
	flag.Var(&skipPrefixes, "skip-jar-prefix",
		"Java class-name prefix to omit when enumerating JARs "+
			"(repeatable). When unset, the default skip list "+
			"(kotlin., kotlinx., org.intellij., org.jetbrains., "+
			"com.google.errorprone., javax.inject., com.google.j2objc.) is used.")
	flag.Parse()

	// Distinguish "user did not pass any --skip-jar-prefix" (use defaults)
	// from "user passed an explicit list" (which may even be empty).
	var skipArg []string
	if skipPrefixes != nil {
		skipArg = []string(skipPrefixes)
	}

	if err := specgen.GenerateFromSources(*refDir, *jarsDir, *extraCP, *outputDir, *goModule, skipArg); err != nil {
		log.Fatalf("generate specs: %v", err)
	}
}
