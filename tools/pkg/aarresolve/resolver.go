package aarresolve

import (
	"context"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// generatedBy stamps the lock file's "generated_by" field.
	generatedBy = "tools/cmd/aar-resolve"

	// parentDepthLimit caps the parent-POM walk so a malformed POM with a
	// self-referential parent cannot loop forever.
	parentDepthLimit = 10

	// defaultMaxConcurrency is the fallback when Options.MaxConcurrency is
	// non-positive.
	defaultMaxConcurrency = 8

	// defaultHTTPTimeout caps every Maven HTTP request to keep CI runs
	// bounded if a repo stalls.
	defaultHTTPTimeout = 60 * time.Second
)

// DefaultRepos is the repository chain used by the CLI when --repo is not
// passed: Google Maven first (AndroidX, Material), then Maven Central as a
// fallback for transitive third-party deps (e.g. errorprone annotations).
var DefaultRepos = []string{
	"https://maven.google.com",
	"https://repo1.maven.org/maven2",
}

// DefaultScopes mirrors Maven's compile/runtime/(default) inclusion set used
// by the resolver when Options.Scopes is empty. The Maven default scope
// (empty string) is treated as "compile".
var DefaultScopes = []string{"compile", "runtime", ""}

// Options drives Resolve. Zero values get sensible defaults; see DefaultRepos,
// DefaultScopes, defaultMaxConcurrency.
type Options struct {
	Top             []string
	Cache           string
	Lock            string
	Repos           []string
	Scopes          []string
	IncludeOptional bool
	MaxConcurrency  int
	VerifyOnly      bool
	Verbose         bool
}

// Resolver carries the state of a single Resolve call.
type Resolver struct {
	opts  Options
	chain *RepoChain

	mu           sync.Mutex
	pomCache     map[string]*POM            // raw fetched POM, keyed by g:a:v
	pomBytes     map[string][]byte          // raw POM bytes, keyed by g:a:v
	pomURL       map[string]string          // origin URL of the POM
	parentMerged map[string]*mergedPOM      // POM merged with all parents, keyed by g:a:v
	scopeSet     map[string]struct{}        // included scopes, fast lookup
	resolved     map[string]*resolvedNode   // nearest-wins map keyed by group:artifact
	depMgmtCache map[string]map[string]string // managed versions per merged POM, keyed by g:a:v
}

// mergedPOM holds a POM merged with its parent chain: properties, depMgmt,
// and dependencies all unioned with parent values overridden by child.
type mergedPOM struct {
	pom        *POM
	properties map[string]string
	depMgmt    []POMDep
}

// resolvedNode is one entry in the BFS nearest-wins map.
type resolvedNode struct {
	group     string
	artifact  string
	version   string
	depth     int
	declOrder int
	deps      []string // group:artifact of direct dependencies
}

// Resolve performs BFS from each --top root, applying Maven nearest-wins
// version resolution, scope filtering, BOM-import merging, and downloads
// every resolved artifact concurrently. The result is written to opts.Lock.
func Resolve(ctx context.Context, opts Options) error {
	if len(opts.Top) == 0 {
		return fmt.Errorf("at least one --top coordinate is required")
	}
	if opts.Cache == "" {
		return fmt.Errorf("--cache is required")
	}
	if opts.Lock == "" {
		return fmt.Errorf("--lock is required")
	}
	repos := opts.Repos
	if len(repos) == 0 {
		repos = DefaultRepos
	}
	scopes := opts.Scopes
	if len(scopes) == 0 {
		scopes = DefaultScopes
	}
	maxConc := opts.MaxConcurrency
	if maxConc <= 0 {
		maxConc = defaultMaxConcurrency
	}

	r := &Resolver{
		opts: opts,
		chain: &RepoChain{
			BaseURLs: repos,
			Client:   &http.Client{Timeout: defaultHTTPTimeout},
		},
		pomCache:     make(map[string]*POM),
		pomBytes:     make(map[string][]byte),
		pomURL:       make(map[string]string),
		parentMerged: make(map[string]*mergedPOM),
		scopeSet:     make(map[string]struct{}, len(scopes)),
		resolved:     make(map[string]*resolvedNode),
		depMgmtCache: make(map[string]map[string]string),
	}
	for _, s := range scopes {
		r.scopeSet[s] = struct{}{}
	}

	if err := r.bfs(ctx); err != nil {
		return err
	}
	return r.downloadAll(ctx, maxConc)
}

// bfs walks the dependency graph from every --top coordinate, recording
// nearest-wins versions in r.resolved.
func (r *Resolver) bfs(ctx context.Context) error {
	type queueItem struct {
		group, artifact, version string
		depth                    int
		declOrder                int
	}
	var queue []queueItem
	for i, top := range r.opts.Top {
		g, a, v, err := parseCoordinate(top)
		if err != nil {
			return fmt.Errorf("--top %q: %w", top, err)
		}
		queue = append(queue, queueItem{g, a, v, 0, i})
	}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		key := coordKey(item.group, item.artifact, item.version)

		merged, err := r.getMergedPOM(ctx, item.group, item.artifact, item.version)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", key, err)
		}

		mgmt, err := r.depManagementMap(ctx, merged)
		if err != nil {
			return fmt.Errorf("dep-management %s: %w", key, err)
		}

		gaKey := item.group + ":" + item.artifact
		node := r.resolved[gaKey]
		isNew := node == nil
		if isNew || item.depth < node.depth {
			node = &resolvedNode{
				group:     item.group,
				artifact:  item.artifact,
				version:   item.version,
				depth:     item.depth,
				declOrder: item.declOrder,
			}
			r.resolved[gaKey] = node
		} else if item.depth == node.depth && item.declOrder < node.declOrder {
			node.version = item.version
			node.declOrder = item.declOrder
		}

		// Always walk children at the chosen version so nearest-wins picks up
		// the correct transitive set; but only enqueue children that improve
		// on a previously-seen version.
		var directDeps []string
		for _, dep := range merged.pom.Dependencies {
			if _, ok := r.scopeSet[dep.Scope]; !ok {
				continue
			}
			if dep.Optional == "true" && !r.opts.IncludeOptional {
				continue
			}
			depGroup, err := Expand(dep.GroupID, merged.properties, item.group, item.artifact, item.version)
			if err != nil {
				return fmt.Errorf("expand groupId %s in %s: %w", dep.GroupID, key, err)
			}
			depArtifact, err := Expand(dep.ArtifactID, merged.properties, item.group, item.artifact, item.version)
			if err != nil {
				return fmt.Errorf("expand artifactId %s in %s: %w", dep.ArtifactID, key, err)
			}
			depVersion, err := r.resolveDepVersion(dep, merged.properties, item.group, item.artifact, item.version, mgmt)
			if err != nil {
				return fmt.Errorf("resolve version for %s:%s in %s: %w", depGroup, depArtifact, key, err)
			}
			depGAKey := depGroup + ":" + depArtifact
			directDeps = append(directDeps, depGAKey)

			existing := r.resolved[depGAKey]
			childDepth := item.depth + 1
			if existing == nil || childDepth < existing.depth {
				queue = append(queue, queueItem{depGroup, depArtifact, depVersion, childDepth, item.declOrder})
			}
		}
		// Record direct deps on the resolved node (overwrites prior on update).
		sort.Strings(directDeps)
		node.deps = directDeps
	}
	return nil
}

// getMergedPOM fetches a POM, walks its parent chain (depth-limited), and
// returns a POM merged with all ancestor properties and dep-management.
func (r *Resolver) getMergedPOM(ctx context.Context, group, artifact, version string) (*mergedPOM, error) {
	key := coordKey(group, artifact, version)
	r.mu.Lock()
	if m, ok := r.parentMerged[key]; ok {
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()

	pom, err := r.fetchPOM(ctx, group, artifact, version)
	if err != nil {
		return nil, err
	}

	props := make(map[string]string)
	if pom.Properties.M != nil {
		for k, v := range pom.Properties.M {
			props[k] = v
		}
	}
	var depMgmt []POMDep
	depMgmt = append(depMgmt, pom.DepMgmt.Deps...)

	curParent := pom.Parent
	for d := 0; d < parentDepthLimit; d++ {
		if curParent.GroupID == "" || curParent.ArtifactID == "" || curParent.Version == "" {
			break
		}
		parent, err := r.fetchPOM(ctx, curParent.GroupID, curParent.ArtifactID, curParent.Version)
		if err != nil {
			return nil, fmt.Errorf("fetch parent %s:%s:%s: %w", curParent.GroupID, curParent.ArtifactID, curParent.Version, err)
		}
		if parent.Properties.M != nil {
			for k, v := range parent.Properties.M {
				if _, exists := props[k]; !exists {
					props[k] = v
				}
			}
		}
		// Append parent depMgmt at the end so child entries take precedence
		// when we build the version map.
		depMgmt = append(depMgmt, parent.DepMgmt.Deps...)
		curParent = parent.Parent
	}

	merged := &mergedPOM{pom: pom, properties: props, depMgmt: depMgmt}
	r.mu.Lock()
	r.parentMerged[key] = merged
	r.mu.Unlock()
	return merged, nil
}

// depManagementMap collapses merged.depMgmt into a (group:artifact -> version)
// map, recursively importing BOMs (<type>pom</type><scope>import</scope>).
// Cached per merged POM.
func (r *Resolver) depManagementMap(ctx context.Context, merged *mergedPOM) (map[string]string, error) {
	key := coordKey(merged.pom.GroupID, merged.pom.ArtifactID, merged.pom.Version)
	r.mu.Lock()
	if m, ok := r.depMgmtCache[key]; ok {
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()

	out := make(map[string]string)
	group, artifact, version := merged.pom.GroupID, merged.pom.ArtifactID, merged.pom.Version
	for _, dm := range merged.depMgmt {
		dmGroup, err := Expand(dm.GroupID, merged.properties, group, artifact, version)
		if err != nil {
			return nil, err
		}
		dmArtifact, err := Expand(dm.ArtifactID, merged.properties, group, artifact, version)
		if err != nil {
			return nil, err
		}
		dmVersion, err := Expand(dm.Version, merged.properties, group, artifact, version)
		if err != nil {
			return nil, err
		}
		dmVersion, err = stripVersionRange(dmVersion)
		if err != nil {
			return nil, err
		}
		if dm.Type == "pom" && dm.Scope == "import" {
			imported, err := r.getMergedPOM(ctx, dmGroup, dmArtifact, dmVersion)
			if err != nil {
				return nil, fmt.Errorf("import BOM %s:%s:%s: %w", dmGroup, dmArtifact, dmVersion, err)
			}
			importedMap, err := r.depManagementMap(ctx, imported)
			if err != nil {
				return nil, err
			}
			for k, v := range importedMap {
				if _, exists := out[k]; !exists {
					out[k] = v
				}
			}
			continue
		}
		gaKey := dmGroup + ":" + dmArtifact
		if _, exists := out[gaKey]; !exists {
			out[gaKey] = dmVersion
		}
	}

	r.mu.Lock()
	r.depMgmtCache[key] = out
	r.mu.Unlock()
	return out, nil
}

// resolveDepVersion picks a concrete version for a <dependency> entry by
// expanding properties, stripping range brackets, and falling back to the
// dep-management map when the dependency declares no <version>.
func (r *Resolver) resolveDepVersion(
	dep POMDep,
	props map[string]string,
	group, artifact, version string,
	mgmt map[string]string,
) (string, error) {
	if dep.Version != "" {
		expanded, err := Expand(dep.Version, props, group, artifact, version)
		if err != nil {
			return "", err
		}
		stripped, err := stripVersionRange(expanded)
		if err != nil {
			return "", err
		}
		return stripped, nil
	}
	depGroup, err := Expand(dep.GroupID, props, group, artifact, version)
	if err != nil {
		return "", err
	}
	depArtifact, err := Expand(dep.ArtifactID, props, group, artifact, version)
	if err != nil {
		return "", err
	}
	if v, ok := mgmt[depGroup+":"+depArtifact]; ok {
		return v, nil
	}
	return "", fmt.Errorf("no version for %s:%s and not in dependencyManagement", depGroup, depArtifact)
}

// fetchPOM fetches and parses the POM for one coordinate, caching the result.
func (r *Resolver) fetchPOM(ctx context.Context, group, artifact, version string) (*POM, error) {
	key := coordKey(group, artifact, version)
	r.mu.Lock()
	if p, ok := r.pomCache[key]; ok {
		r.mu.Unlock()
		return p, nil
	}
	r.mu.Unlock()

	if r.opts.Verbose {
		log.Printf("fetching POM %s", key)
	}
	body, url, err := r.chain.FetchPOM(ctx, group, artifact, version)
	if err != nil {
		return nil, fmt.Errorf("fetch POM %s: %w", key, err)
	}
	pom := &POM{}
	if err := xml.Unmarshal(body, pom); err != nil {
		return nil, fmt.Errorf("parse POM %s: %w", key, err)
	}
	// POMs frequently inherit groupId/version from their parent without
	// restating them on the project element. Backfill so downstream code can
	// treat the merged POM as self-contained.
	if pom.GroupID == "" {
		pom.GroupID = pom.Parent.GroupID
	}
	if pom.Version == "" {
		pom.Version = pom.Parent.Version
	}
	if pom.GroupID == "" {
		pom.GroupID = group
	}
	if pom.ArtifactID == "" {
		pom.ArtifactID = artifact
	}
	if pom.Version == "" {
		pom.Version = version
	}

	r.mu.Lock()
	r.pomCache[key] = pom
	r.pomBytes[key] = body
	r.pomURL[key] = url
	r.mu.Unlock()

	// Cache the POM bytes alongside the artifact in the on-disk cache.
	if _, err := CacheFile(r.opts.Cache, layoutPath(group, artifact, version, "pom"), body, r.opts.VerifyOnly); err != nil {
		return nil, fmt.Errorf("cache POM %s: %w", key, err)
	}
	return pom, nil
}

// downloadAll fetches every resolved artifact concurrently, computes its
// SHA-256, and writes the LockFile at the end.
func (r *Resolver) downloadAll(ctx context.Context, maxConc int) error {
	keys := make([]string, 0, len(r.resolved))
	for k := range r.resolved {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	type result struct {
		entry ArtifactEntry
		err   error
	}
	results := make([]result, len(keys))
	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup
	for i, k := range keys {
		i, k := i, k
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = r.downloadOne(ctx, k)
		}()
	}
	wg.Wait()

	lock := &LockFile{
		GeneratedBy: generatedBy,
		TopLevel:    append([]string(nil), r.opts.Top...),
	}
	for _, res := range results {
		if res.err != nil {
			return res.err
		}
		lock.Artifacts = append(lock.Artifacts, res.entry)
	}
	return lock.Write(r.opts.Lock)
}

func (r *Resolver) downloadOne(ctx context.Context, gaKey string) (res struct {
	entry ArtifactEntry
	err   error
}) {
	node := r.resolved[gaKey]
	pomKey := coordKey(node.group, node.artifact, node.version)
	merged, err := r.getMergedPOM(ctx, node.group, node.artifact, node.version)
	if err != nil {
		res.err = fmt.Errorf("get merged POM %s: %w", pomKey, err)
		return
	}
	packaging := merged.pom.Packaging
	if packaging == "" {
		packaging = "jar"
	}
	if r.opts.Verbose {
		log.Printf("fetching artifact %s (%s)", pomKey, packaging)
	}
	body, url, err := r.chain.FetchArtifact(ctx, node.group, node.artifact, node.version, packaging)
	if err != nil {
		res.err = fmt.Errorf("fetch artifact %s: %w", pomKey, err)
		return
	}
	relPath := layoutPath(node.group, node.artifact, node.version, packaging)
	sha, err := CacheFile(r.opts.Cache, relPath, body, r.opts.VerifyOnly)
	if err != nil {
		res.err = err
		return
	}
	pomRelPath := layoutPath(node.group, node.artifact, node.version, "pom")
	r.mu.Lock()
	pomBytes := r.pomBytes[pomKey]
	r.mu.Unlock()
	pomSHA, err := CacheFile(r.opts.Cache, pomRelPath, pomBytes, r.opts.VerifyOnly)
	if err != nil {
		res.err = err
		return
	}
	res.entry = ArtifactEntry{
		Coordinate:   pomKey,
		Group:        node.group,
		Artifact:     node.artifact,
		Version:      node.version,
		Packaging:    packaging,
		Repo:         baseURLOf(url, relPath),
		Path:         relPath,
		SHA256:       sha,
		POMPath:      pomRelPath,
		POMSHA256:    pomSHA,
		Dependencies: append([]string(nil), node.deps...),
	}
	return
}

// baseURLOf returns the repository base URL by stripping the layout suffix
// from the full URL the artifact came from.
func baseURLOf(fullURL, relPath string) string {
	suffix := "/" + relPath
	if strings.HasSuffix(fullURL, suffix) {
		return strings.TrimSuffix(fullURL, suffix)
	}
	return fullURL
}

// parseCoordinate parses "group:artifact:version".
func parseCoordinate(s string) (string, string, string, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("expected group:artifact:version, got %q", s)
	}
	return parts[0], parts[1], parts[2], nil
}

func coordKey(group, artifact, version string) string {
	return group + ":" + artifact + ":" + version
}

// stripVersionRange handles the limited Maven range syntax used by Google
// Maven POMs in practice: [X], [X,], [,X] all collapse to X. Anything else
// is rejected so a surprise range can't be silently misresolved.
func stripVersionRange(v string) (string, error) {
	if v == "" || (v[0] != '[' && v[0] != '(') {
		return v, nil
	}
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return "", fmt.Errorf("unsupported version range %q (only [X], [X,], [,X] supported)", v)
	}
	inner := v[1 : len(v)-1]
	if inner == "" {
		return "", fmt.Errorf("empty version range %q", v)
	}
	if !strings.Contains(inner, ",") {
		return inner, nil
	}
	parts := strings.SplitN(inner, ",", 2)
	left, right := parts[0], parts[1]
	switch {
	case left == "" && right != "":
		return right, nil
	case right == "" && left != "":
		return left, nil
	case left != "" && right != "" && left == right:
		return left, nil
	}
	return "", fmt.Errorf("unsupported version range %q (only [X], [X,], [,X] supported)", v)
}
