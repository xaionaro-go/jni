package aarresolve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureFile is one (path, body) entry served by newFixtureServer.
type fixtureFile struct {
	path string
	body []byte
}

// newFixtureServer serves a deterministic Maven Layout 2 tree from in-memory
// bodies. Unknown paths return 404 so the resolver's repo fallthrough path is
// exercised even with a single repo.
func newFixtureServer(t *testing.T, files []fixtureFile) *httptest.Server {
	t.Helper()
	idx := make(map[string][]byte, len(files))
	for _, f := range files {
		idx["/"+f.path] = f.body
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := idx[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// detBytes returns deterministic bytes derived from seed; required when two
// runs of the same resolver must produce byte-identical lock files. The
// blueprint allows random bytes ("4 random bytes each") but determinism is
// required for the lock-file equality test, so a hash-based stream is used.
func detBytes(seed string, n int) []byte {
	out := make([]byte, 0, n)
	cur := []byte(seed)
	for len(out) < n {
		sum := sha256.Sum256(cur)
		out = append(out, sum[:]...)
		cur = sum[:]
	}
	return out[:n]
}

func pomBody(group, artifact, version string, body string) []byte {
	header := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>%s</groupId>
  <artifactId>%s</artifactId>
  <version>%s</version>
`, group, artifact, version)
	return []byte(header + body + "\n</project>\n")
}

// basicFixture returns the file set used by TestResolveBasicClosure and
// TestResolveDeterminism. Top: g.a:r1:1.0.0.
//
//	r1 (compile -> d1, optional -> d2)
//	d1 (parent p; declares d3 with no version)
//	p  (properties: kotlinVersion=1.0.0; depMgmt: g.a:d3:1.0.0)
//	d2, d3 (leaf POMs)
//
// All artifact bodies are 4 deterministic bytes so the SHA-256 is stable.
func basicFixture() []fixtureFile {
	r1POM := pomBody("g.a", "r1", "1.0.0", `
  <packaging>jar</packaging>
  <dependencies>
    <dependency>
      <groupId>g.a</groupId>
      <artifactId>d1</artifactId>
      <version>1.0.0</version>
      <scope>compile</scope>
    </dependency>
    <dependency>
      <groupId>g.a</groupId>
      <artifactId>d2</artifactId>
      <version>1.0.0</version>
      <scope>compile</scope>
      <optional>true</optional>
    </dependency>
  </dependencies>`)
	d1POM := pomBody("g.a", "d1", "1.0.0", `
  <packaging>jar</packaging>
  <parent>
    <groupId>g.a</groupId>
    <artifactId>p</artifactId>
    <version>1.0.0</version>
  </parent>
  <dependencies>
    <dependency>
      <groupId>g.a</groupId>
      <artifactId>d3</artifactId>
    </dependency>
  </dependencies>`)
	pPOM := pomBody("g.a", "p", "1.0.0", `
  <packaging>pom</packaging>
  <properties>
    <kotlinVersion>1.0.0</kotlinVersion>
  </properties>
  <dependencyManagement>
    <dependencies>
      <dependency>
        <groupId>g.a</groupId>
        <artifactId>d3</artifactId>
        <version>${kotlinVersion}</version>
      </dependency>
    </dependencies>
  </dependencyManagement>`)
	d2POM := pomBody("g.a", "d2", "1.0.0", `<packaging>jar</packaging>`)
	d3POM := pomBody("g.a", "d3", "1.0.0", `<packaging>jar</packaging>`)
	return []fixtureFile{
		{"g/a/r1/1.0.0/r1-1.0.0.pom", r1POM},
		{"g/a/r1/1.0.0/r1-1.0.0.jar", detBytes("r1", 4)},
		{"g/a/d1/1.0.0/d1-1.0.0.pom", d1POM},
		{"g/a/d1/1.0.0/d1-1.0.0.jar", detBytes("d1", 4)},
		{"g/a/p/1.0.0/p-1.0.0.pom", pPOM},
		{"g/a/p/1.0.0/p-1.0.0.pom.pom", pPOM}, // packaging=pom -> <a>-<v>.pom (same as POM URL)
		{"g/a/d2/1.0.0/d2-1.0.0.pom", d2POM},
		{"g/a/d2/1.0.0/d2-1.0.0.jar", detBytes("d2", 4)},
		{"g/a/d3/1.0.0/d3-1.0.0.pom", d3POM},
		{"g/a/d3/1.0.0/d3-1.0.0.jar", detBytes("d3", 4)},
	}
}

func mustReadLock(t *testing.T, path string) *LockFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	var l LockFile
	if err := json.Unmarshal(data, &l); err != nil {
		t.Fatalf("parse lock: %v", err)
	}
	return &l
}

func resolvedCoords(l *LockFile) map[string]ArtifactEntry {
	out := make(map[string]ArtifactEntry, len(l.Artifacts))
	for _, a := range l.Artifacts {
		out[a.Coordinate] = a
	}
	return out
}

func TestResolveBasicClosure(t *testing.T) {
	srv := newFixtureServer(t, basicFixture())
	dir := t.TempDir()
	opts := Options{
		Top:            []string{"g.a:r1:1.0.0"},
		Cache:          filepath.Join(dir, "cache"),
		Lock:           filepath.Join(dir, "lock.json"),
		Repos:          []string{srv.URL},
		MaxConcurrency: 4,
	}
	if err := Resolve(context.Background(), opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	lock := mustReadLock(t, opts.Lock)
	got := resolvedCoords(lock)

	for _, want := range []string{"g.a:r1:1.0.0", "g.a:d1:1.0.0", "g.a:d3:1.0.0"} {
		entry, ok := got[want]
		if !ok {
			t.Errorf("missing %s in closure; have %v", want, keys(got))
			continue
		}
		if len(entry.SHA256) != 64 {
			t.Errorf("%s: SHA-256 should be 64 hex chars, got %q", want, entry.SHA256)
		}
		if _, err := hex.DecodeString(entry.SHA256); err != nil {
			t.Errorf("%s: SHA-256 not hex: %v", want, err)
		}
		if len(entry.POMSHA256) != 64 {
			t.Errorf("%s: POM SHA-256 should be 64 hex chars, got %q", want, entry.POMSHA256)
		}
	}
	if _, ok := got["g.a:d2:1.0.0"]; ok {
		t.Errorf("d2 (optional) should be skipped by default; got %v", keys(got))
	}
}

func TestResolveDeterminism(t *testing.T) {
	srv := newFixtureServer(t, basicFixture())
	dir := t.TempDir()
	opts := Options{
		Top:            []string{"g.a:r1:1.0.0"},
		Cache:          filepath.Join(dir, "cache"),
		Lock:           filepath.Join(dir, "lock.json"),
		Repos:          []string{srv.URL},
		MaxConcurrency: 4,
	}
	if err := Resolve(context.Background(), opts); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	first, err := os.ReadFile(opts.Lock)
	if err != nil {
		t.Fatalf("read first lock: %v", err)
	}
	if err := Resolve(context.Background(), opts); err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	second, err := os.ReadFile(opts.Lock)
	if err != nil {
		t.Fatalf("read second lock: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("lock not deterministic\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestResolveVerifyOnly(t *testing.T) {
	srv := newFixtureServer(t, basicFixture())
	dir := t.TempDir()
	opts := Options{
		Top:            []string{"g.a:r1:1.0.0"},
		Cache:          filepath.Join(dir, "cache"),
		Lock:           filepath.Join(dir, "lock.json"),
		Repos:          []string{srv.URL},
		MaxConcurrency: 4,
	}
	if err := Resolve(context.Background(), opts); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	// Corrupt one cached file.
	corrupted := filepath.Join(opts.Cache, "g/a/d1/1.0.0/d1-1.0.0.jar")
	if err := os.WriteFile(corrupted, []byte("garbage"), 0o644); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	verifyOpts := opts
	verifyOpts.VerifyOnly = true
	err := Resolve(context.Background(), verifyOpts)
	if err == nil {
		t.Fatal("expected verify-only Resolve to fail after corruption")
	}
	if !strings.Contains(err.Error(), "SHA-256 mismatch") && !strings.Contains(err.Error(), "missing") {
		t.Errorf("expected SHA-256 mismatch error, got: %v", err)
	}
}

func TestPropertyExpansion(t *testing.T) {
	props := map[string]string{"kotlinVersion": "1.0.0"}
	got, err := Expand("${project.version}-${kotlinVersion}", props, "g.a", "r1", "1.0.0")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "1.0.0-1.0.0" {
		t.Errorf("got %q, want %q", got, "1.0.0-1.0.0")
	}
	if _, err := Expand("${unknown.thing}", props, "g.a", "r1", "1.0.0"); err == nil {
		t.Error("expected error for unknown token")
	}
	// Cycle detection.
	cycle := map[string]string{"a": "${b}", "b": "${a}"}
	if _, err := Expand("${a}", cycle, "g", "a", "v"); err == nil {
		t.Error("expected error for property cycle")
	}
}

// TestNearestWinsConflict checks both directions of nearest-wins:
//
//  1. shallow-then-deep: r1 -> d1@1.0.0 (depth 1) and r1 -> d2 -> d1@2.0.0
//     (depth 2) -> d1@1.0.0 wins.
//  2. deep-then-shallow: same graph, but the top order is reversed so the
//     transitively-deeper path is encountered first in declaration order.
//     Even reversed, the shallower depth still wins because BFS pops shallow
//     items first regardless of declaration order.
func TestNearestWinsConflict(t *testing.T) {
	// Case 1: r1 directly depends on d1@1.0.0 (depth 1) plus d2 that pulls
	// d1@2.0.0 (depth 2). Expected: d1@1.0.0.
	r1POM := pomBody("g.a", "r1", "1.0.0", `
  <dependencies>
    <dependency><groupId>g.a</groupId><artifactId>d1</artifactId><version>1.0.0</version></dependency>
    <dependency><groupId>g.a</groupId><artifactId>d2</artifactId><version>1.0.0</version></dependency>
  </dependencies>`)
	d1V1POM := pomBody("g.a", "d1", "1.0.0", "")
	d1V2POM := pomBody("g.a", "d1", "2.0.0", "")
	d2POM := pomBody("g.a", "d2", "1.0.0", `
  <dependencies>
    <dependency><groupId>g.a</groupId><artifactId>d1</artifactId><version>2.0.0</version></dependency>
  </dependencies>`)
	// Case 2: top1 -> mid -> d1@2.0.0 (depth 2). top2 -> d1@1.0.0 (depth 1).
	// Both top1 and top2 are roots; BFS records depth-1 d1@1.0.0 before the
	// depth-2 d1@2.0.0 entry can overwrite it.
	top1POM := pomBody("g.a", "top1", "1.0.0", `
  <dependencies>
    <dependency><groupId>g.a</groupId><artifactId>mid</artifactId><version>1.0.0</version></dependency>
  </dependencies>`)
	top2POM := pomBody("g.a", "top2", "1.0.0", `
  <dependencies>
    <dependency><groupId>g.a</groupId><artifactId>d1</artifactId><version>1.0.0</version></dependency>
  </dependencies>`)
	midPOM := pomBody("g.a", "mid", "1.0.0", `
  <dependencies>
    <dependency><groupId>g.a</groupId><artifactId>d1</artifactId><version>2.0.0</version></dependency>
  </dependencies>`)
	files := []fixtureFile{
		{"g/a/r1/1.0.0/r1-1.0.0.pom", r1POM},
		{"g/a/r1/1.0.0/r1-1.0.0.jar", detBytes("r1", 4)},
		{"g/a/d1/1.0.0/d1-1.0.0.pom", d1V1POM},
		{"g/a/d1/1.0.0/d1-1.0.0.jar", detBytes("d1v1", 4)},
		{"g/a/d1/2.0.0/d1-2.0.0.pom", d1V2POM},
		{"g/a/d1/2.0.0/d1-2.0.0.jar", detBytes("d1v2", 4)},
		{"g/a/d2/1.0.0/d2-1.0.0.pom", d2POM},
		{"g/a/d2/1.0.0/d2-1.0.0.jar", detBytes("d2", 4)},
		{"g/a/top1/1.0.0/top1-1.0.0.pom", top1POM},
		{"g/a/top1/1.0.0/top1-1.0.0.jar", detBytes("top1", 4)},
		{"g/a/top2/1.0.0/top2-1.0.0.pom", top2POM},
		{"g/a/top2/1.0.0/top2-1.0.0.jar", detBytes("top2", 4)},
		{"g/a/mid/1.0.0/mid-1.0.0.pom", midPOM},
		{"g/a/mid/1.0.0/mid-1.0.0.jar", detBytes("mid", 4)},
	}
	srv := newFixtureServer(t, files)

	// Case 1.
	dir1 := t.TempDir()
	opts1 := Options{
		Top:            []string{"g.a:r1:1.0.0"},
		Cache:          filepath.Join(dir1, "cache"),
		Lock:           filepath.Join(dir1, "lock.json"),
		Repos:          []string{srv.URL},
		MaxConcurrency: 2,
	}
	if err := Resolve(context.Background(), opts1); err != nil {
		t.Fatalf("Resolve case1: %v", err)
	}
	got1 := resolvedCoords(mustReadLock(t, opts1.Lock))
	if _, ok := got1["g.a:d1:1.0.0"]; !ok {
		t.Errorf("case1: expected d1:1.0.0 (depth 1); have %v", keys(got1))
	}
	if _, ok := got1["g.a:d1:2.0.0"]; ok {
		t.Errorf("case1: did not expect d1:2.0.0 (depth 2) in closure; have %v", keys(got1))
	}

	// Case 2: top1 declared FIRST so its transitive d1@2.0.0 is discovered
	// before top2's direct d1@1.0.0. The shallower depth must still win.
	dir2 := t.TempDir()
	opts2 := Options{
		Top:            []string{"g.a:top1:1.0.0", "g.a:top2:1.0.0"},
		Cache:          filepath.Join(dir2, "cache"),
		Lock:           filepath.Join(dir2, "lock.json"),
		Repos:          []string{srv.URL},
		MaxConcurrency: 2,
	}
	if err := Resolve(context.Background(), opts2); err != nil {
		t.Fatalf("Resolve case2: %v", err)
	}
	got2 := resolvedCoords(mustReadLock(t, opts2.Lock))
	if _, ok := got2["g.a:d1:1.0.0"]; !ok {
		t.Errorf("case2: expected d1:1.0.0 (depth 1 via top2); have %v", keys(got2))
	}
	if _, ok := got2["g.a:d1:2.0.0"]; ok {
		t.Errorf("case2: did not expect d1:2.0.0 (depth 2 via top1->mid); have %v", keys(got2))
	}
}

func keys(m map[string]ArtifactEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
