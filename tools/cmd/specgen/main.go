package main

import (
	"flag"
	"log"

	"github.com/AndroidGoLab/jni/tools/pkg/specgen"
)

func main() {
	refDir := flag.String("ref", "ref", "directory containing reference .class files")
	jarsDir := flag.String("jars-dir", "", "directory tree containing classes.jar files (e.g., .aar-cache/extracted)")
	extraCP := flag.String("classpath", "", "additional classpath for javap (e.g. android.jar)")
	outputDir := flag.String("output", "spec/java", "output directory for generated YAML specs")
	goModule := flag.String("go-module", "github.com/AndroidGoLab/jni", "Go module path")
	flag.Parse()

	if err := specgen.GenerateFromSources(*refDir, *jarsDir, *extraCP, *outputDir, *goModule); err != nil {
		log.Fatalf("generate specs: %v", err)
	}
}
