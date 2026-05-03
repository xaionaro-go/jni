// Command lock-paths consumes a lock file produced by tools/cmd/aar-resolve
// and acts on its closure: extract AAR/JAR contents, run aapt2 compile per
// artifact, or print Make-friendly absolute path lists for the link step.
//
// Modes are mutually exclusive; exactly one of --extract, --compile, --field
// must be provided per invocation.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/AndroidGoLab/jni/tools/pkg/aarresolve"
	"github.com/AndroidGoLab/jni/tools/pkg/lockpaths"
)

func main() {
	var (
		lockPath = flag.String("lock", ".aar-cache/lock.json", "path to the cycle-2 lock.json")
		cacheDir = flag.String("cache", ".aar-cache", "root of the on-disk artifact cache")
		extract  = flag.Bool("extract", false, "unpack every AAR/JAR under <cache>/extracted/")
		compile  = flag.Bool("compile", false, "run aapt2 compile per artifact under <cache>/compiled/")
		aapt2    = flag.String("aapt2", "", "absolute path to aapt2 (required with --compile)")
		field    = flag.String("field", "", "print one of: flat-args, classes-jars")
	)
	flag.Parse()

	mode := 0
	if *extract {
		mode++
	}
	if *compile {
		mode++
	}
	if *field != "" {
		mode++
	}
	if mode != 1 {
		log.Fatalf("lock-paths: exactly one of --extract, --compile, --field is required (got %d)", mode)
	}

	lock, err := aarresolve.ReadLockFile(*lockPath)
	if err != nil {
		log.Fatalf("lock-paths: %v", err)
	}

	switch {
	case *extract:
		if err := lockpaths.ExtractAll(lock, *cacheDir); err != nil {
			log.Fatalf("lock-paths: %v", err)
		}
	case *compile:
		if *aapt2 == "" {
			log.Fatalf("lock-paths: --compile requires --aapt2 <path>")
		}
		if err := lockpaths.CompileAll(lock, *cacheDir, *aapt2); err != nil {
			log.Fatalf("lock-paths: %v", err)
		}
	case *field != "":
		if err := lockpaths.PrintField(os.Stdout, lock, *cacheDir, *field); err != nil {
			log.Fatalf("lock-paths: %v", err)
		}
	default:
		// Unreachable: mode count was validated above.
		log.Fatalf("lock-paths: %s", fmt.Errorf("internal: no mode dispatched"))
	}
}
