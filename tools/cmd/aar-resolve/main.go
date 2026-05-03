// Command aar-resolve resolves the transitive Maven closure of one or more
// AAR/JAR coordinates and writes a deterministic JSON lock file plus a
// SHA-256 verified on-disk cache.
package main

import (
	"context"
	"flag"
	"log"
	"strings"

	"github.com/AndroidGoLab/jni/tools/pkg/aarresolve"
)

// stringSlice is a flag.Value backing repeated flags like --top a:b:c --top d:e:f.
type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	var (
		tops            stringSlice
		repos           stringSlice
		scopes          stringSlice
		cache           = flag.String("cache", ".aar-cache", "directory for cached POMs/AARs/JARs")
		lock            = flag.String("lock", ".aar-cache/lock.json", "output lock file path")
		includeOptional = flag.Bool("include-optional", false, "include <optional>true</optional> dependencies")
		maxConcurrency  = flag.Int("max-concurrency", 8, "maximum concurrent HTTP fetches for artifact downloads")
		verifyOnly      = flag.Bool("verify-only", false, "fail if the cache is missing or corrupted instead of downloading")
		verbose         = flag.Bool("verbose", false, "log every POM and artifact fetch to stderr")
	)
	flag.Var(&tops, "top", "top-level coordinate group:artifact:version (repeatable)")
	flag.Var(&repos, "repo", "Maven repository base URL (repeatable; default: maven.google.com then repo1.maven.org/maven2)")
	flag.Var(&scopes, "scope", "included Maven scope (repeatable; default: compile, runtime, and the empty default scope)")
	flag.Parse()

	opts := aarresolve.Options{
		Top:             []string(tops),
		Cache:           *cache,
		Lock:            *lock,
		Repos:           []string(repos),
		Scopes:          []string(scopes),
		IncludeOptional: *includeOptional,
		MaxConcurrency:  *maxConcurrency,
		VerifyOnly:      *verifyOnly,
		Verbose:         *verbose,
	}
	if err := aarresolve.Resolve(context.Background(), opts); err != nil {
		log.Fatalf("aar-resolve: %v", err)
	}
}
