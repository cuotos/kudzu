// Package github implements gate.Evicter using a GitHub App. When the circuit
// breaker trips, it lists the in-flight merge-group branches of a repo
// (gh-readonly-queue/<base>/*) and posts a failing commit status to each head
// SHA so GitHub evicts those PRs from the merge queue immediately.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
)

// Client authenticates as a GitHub App installation and posts commit statuses.
type Client struct {
	http    *http.Client
	apiBase string // normalised, trailing slash
	log     *slog.Logger
}

// New builds a Client. apiBase is the REST API root (e.g.
// "https://api.github.com/", or "https://ghe.example.com/api/v3/" for GHES).
func New(appID, installationID int64, privateKey []byte, apiBase string, log *slog.Logger) (*Client, error) {
	itr, err := ghinstallation.New(http.DefaultTransport, appID, installationID, privateKey)
	if err != nil {
		return nil, fmt.Errorf("github app transport: %w", err)
	}
	if apiBase == "" {
		apiBase = "https://api.github.com/"
	}
	if !strings.HasSuffix(apiBase, "/") {
		apiBase += "/"
	}
	itr.BaseURL = strings.TrimSuffix(apiBase, "/") // token-mint endpoint base
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		http:    &http.Client{Transport: itr, Timeout: 15 * time.Second},
		apiBase: apiBase,
		log:     log,
	}, nil
}

type ref struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

// Evict posts state=failure / checkContext to the head commit of every
// gh-readonly-queue/<base>/* branch in repo ("owner/name"). It returns the SHAs
// it acted on; per-branch failures are logged and skipped, not fatal.
func (c *Client) Evict(ctx context.Context, repo, base, checkContext, description string) ([]string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return nil, fmt.Errorf("invalid repo %q (want owner/name)", repo)
	}
	refs, err := c.matchingRefs(ctx, owner, name, base)
	if err != nil {
		return nil, fmt.Errorf("list merge-queue refs: %w", err)
	}
	var shas []string
	for _, r := range refs {
		sha := r.Object.SHA
		if sha == "" {
			continue
		}
		if err := c.postFailureStatus(ctx, owner, name, sha, checkContext, description); err != nil {
			c.log.Error("post eviction status failed", "repo", repo, "sha", sha, "err", err)
			continue
		}
		shas = append(shas, sha)
	}
	return shas, nil
}

func (c *Client) matchingRefs(ctx context.Context, owner, name, base string) ([]ref, error) {
	// GET /repos/{owner}/{repo}/git/matching-refs/heads/gh-readonly-queue/{base}/
	p := fmt.Sprintf("repos/%s/%s/git/matching-refs/heads/gh-readonly-queue/%s/",
		url.PathEscape(owner), url.PathEscape(name), url.PathEscape(base))
	var refs []ref
	if err := c.do(ctx, http.MethodGet, p, nil, &refs); err != nil {
		return nil, err
	}
	return refs, nil
}

func (c *Client) postFailureStatus(ctx context.Context, owner, name, sha, checkContext, description string) error {
	p := fmt.Sprintf("repos/%s/%s/statuses/%s", url.PathEscape(owner), url.PathEscape(name), url.PathEscape(sha))
	body := map[string]string{
		"state":       "failure",
		"context":     checkContext,
		"description": truncate(description, 140), // GitHub caps status descriptions
	}
	return c.do(ctx, http.MethodPost, p, body, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiBase+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("github %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
