package aarresolve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// RepoChain fetches Maven artifacts by trying a list of base URLs in order,
// falling through on 404. The first repo to return 200 wins.
type RepoChain struct {
	BaseURLs []string
	Client   *http.Client
}

// errArtifactNotFound is returned when every repo in the chain returns 404.
var errArtifactNotFound = errors.New("artifact not found in any repository")

// FetchPOM fetches the POM XML for the given coordinate.
func (r *RepoChain) FetchPOM(ctx context.Context, group, artifact, version string) ([]byte, string, error) {
	rel := layoutPath(group, artifact, version, "pom")
	return r.fetch(ctx, rel)
}

// FetchArtifact fetches the binary artifact (AAR or JAR) for the given
// coordinate. The packaging argument selects the file extension; an empty
// packaging defaults to "jar" to match Maven semantics.
func (r *RepoChain) FetchArtifact(ctx context.Context, group, artifact, version, packaging string) ([]byte, string, error) {
	if packaging == "" {
		packaging = "jar"
	}
	rel := layoutPath(group, artifact, version, packaging)
	return r.fetch(ctx, rel)
}

func (r *RepoChain) fetch(ctx context.Context, relPath string) ([]byte, string, error) {
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	var lastErr error
	for _, base := range r.BaseURLs {
		url := strings.TrimRight(base, "/") + "/" + relPath
		body, status, err := httpGet(ctx, client, url)
		if err != nil {
			lastErr = fmt.Errorf("GET %s: %w", url, err)
			continue
		}
		switch status {
		case http.StatusOK:
			return body, url, nil
		case http.StatusNotFound:
			continue
		default:
			lastErr = fmt.Errorf("GET %s: HTTP %d", url, status)
		}
	}
	if lastErr == nil {
		lastErr = errArtifactNotFound
	}
	return nil, "", lastErr
}

func httpGet(ctx context.Context, client *http.Client, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// Drain the body so the connection can be reused, but don't return it.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, resp.StatusCode, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// layoutPath produces a Maven Layout 2 relative path:
//
//	{groupSlashes}/{artifact}/{version}/{artifact}-{version}.{ext}
func layoutPath(group, artifact, version, ext string) string {
	groupSlashes := strings.ReplaceAll(group, ".", "/")
	return fmt.Sprintf("%s/%s/%s/%s-%s.%s", groupSlashes, artifact, version, artifact, version, ext)
}
