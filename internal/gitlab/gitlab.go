// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package gitlab talks to the three GitLab endpoints yasrt needs: create a
// release, attach a release link, and trigger a pipeline in another project.
//
// There is no client library here on purpose. Three endpoints do not justify a
// dependency tree in a tool whose reason for existing is dependency reduction,
// and release jobs commonly call these endpoints with curl already.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a GitLab API client authenticated with a CI job token. It reads
// the protection rules `yasrt check` compares; publishing goes through
// internal/forge.
type Client struct {
	APIURL    string // e.g. https://git.example.com/api/v4
	ProjectID string // numeric id or URL-encoded path
	Token     string // CI_JOB_TOKEN

	HTTP *http.Client
	// MaxAttempts bounds retries of transient failures. Zero means 3.
	MaxAttempts int
	// Backoff is the pause after the first failed attempt; it doubles.
	Backoff time.Duration
}

// New builds a client with sensible timeouts.
func New(apiURL, projectID, token string) *Client {
	return &Client{
		APIURL:    strings.TrimRight(apiURL, "/"),
		ProjectID: projectID,
		Token:     token,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// APIError carries a non-2xx response.
type APIError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 400 {
		body = body[:400] + "…"
	}
	return fmt.Sprintf("gitlab %s %s: %d %s: %s",
		e.Method, e.Path, e.Status, http.StatusText(e.Status), body)
}

// Retryable reports whether another attempt could plausibly succeed.
func (e *APIError) Retryable() bool { return e.Status >= 500 || e.Status == http.StatusTooManyRequests }

func (c *Client) projectPath(suffix string) string {
	return "/projects/" + url.PathEscape(c.ProjectID) + "/" + suffix
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	attempts := c.MaxAttempts
	if attempts <= 0 {
		attempts = 3
	}
	backoff := c.Backoff
	if backoff <= 0 {
		backoff = 500 * time.Millisecond
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		err := c.attempt(ctx, method, path, body, out)
		if err == nil {
			return nil
		}
		lastErr = err

		var apiErr *APIError
		retryable := errors.As(err, &apiErr) && apiErr.Retryable()
		if !errors.As(err, &apiErr) {
			retryable = true // transport failure
		}
		if !retryable || attempt == attempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return lastErr
}

func (c *Client) attempt(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.APIURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("JOB-TOKEN", c.Token)
	req.Header.Set("Accept", "application/json")

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: string(raw)}
	}
	if readErr != nil {
		return readErr
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// AccessLevel is GitLab's numeric permission scale. Only the values that
// matter for deciding who may release are named.
type AccessLevel int

const (
	AccessNoOne      AccessLevel = 0
	AccessDeveloper  AccessLevel = 30
	AccessMaintainer AccessLevel = 40
	AccessOwner      AccessLevel = 50
)

func (a AccessLevel) String() string {
	switch {
	case a >= AccessOwner:
		return "Owner"
	case a >= AccessMaintainer:
		return "Maintainer"
	case a >= AccessDeveloper:
		return "Developer"
	case a <= AccessNoOne:
		return "no one"
	}
	return fmt.Sprintf("level %d", int(a))
}

type accessEntry struct {
	AccessLevel int `json:"access_level"`
}

// ProtectedBranch is the subset of a branch protection rule yasrt reads.
type ProtectedBranch struct {
	Name              string        `json:"name"`
	MergeAccessLevels []accessEntry `json:"merge_access_levels"`
	PushAccessLevels  []accessEntry `json:"push_access_levels"`
}

// LowestMergeLevel is the least privileged role that may merge into the branch.
func (b *ProtectedBranch) LowestMergeLevel() AccessLevel { return lowest(b.MergeAccessLevels) }

// ProtectedTag is the subset of a protected-tag rule yasrt reads.
type ProtectedTag struct {
	Name               string        `json:"name"`
	CreateAccessLevels []accessEntry `json:"create_access_levels"`
}

// LowestCreateLevel is the least privileged role that may create such a tag.
func (t *ProtectedTag) LowestCreateLevel() AccessLevel { return lowest(t.CreateAccessLevels) }

func lowest(entries []accessEntry) AccessLevel {
	if len(entries) == 0 {
		return AccessNoOne
	}
	min := entries[0].AccessLevel
	for _, e := range entries[1:] {
		if e.AccessLevel < min {
			min = e.AccessLevel
		}
	}
	return AccessLevel(min)
}

// ListProtectedBranches reads every branch protection rule.
//
// Deliberately a list rather than a lookup of one branch: GitLab answers 404
// for an unauthorised read just as it does for a branch that carries no
// protection, so a per-branch GET cannot tell "not protected" from "not
// allowed to look". Listing makes the difference explicit — an error is an
// error, and an empty list really is an unprotected repository. A safety check
// must not fail open.
func (c *Client) ListProtectedBranches(ctx context.Context) ([]ProtectedBranch, error) {
	var out []ProtectedBranch
	if err := c.do(ctx, http.MethodGet, c.projectPath("protected_branches"), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListProtectedTags reads every protected-tag rule.
func (c *Client) ListProtectedTags(ctx context.Context) ([]ProtectedTag, error) {
	var out []ProtectedTag
	if err := c.do(ctx, http.MethodGet, c.projectPath("protected_tags"), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
