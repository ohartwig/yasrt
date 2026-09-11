// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package gitlab talks to the three GitLab endpoints yasrt needs: create a
// release, attach a release link, and trigger a pipeline in another project.
//
// There is no client library here on purpose. Three endpoints do not justify a
// dependency tree in a tool whose reason for existing is dependency reduction,
// and the estate's own release jobs already call these endpoints with curl.
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

// Client is a GitLab API client authenticated with a CI job token.
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

// Release is the subset of a GitLab release yasrt reads back.
type Release struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name,omitzero"`
	Description string `json:"description,omitzero"`
	Links       struct {
		Self string `json:"self,omitzero"`
	} `json:"_links,omitzero"`
}

// URL is the web address of the release, when GitLab supplied one.
func (r *Release) URL() string {
	if r == nil {
		return ""
	}
	return r.Links.Self
}

// CreateReleaseRequest is the body of POST /releases.
type CreateReleaseRequest struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name,omitzero"`
	Description string `json:"description,omitzero"`
	// Ref is only sent when the tag does not exist yet. yasrt always creates
	// the tag itself first, so this stays empty.
	Ref string `json:"ref,omitzero"`
}

// CreateRelease publishes a release for an existing tag.
func (c *Client) CreateRelease(ctx context.Context, req CreateReleaseRequest) (*Release, error) {
	var out Release
	err := c.do(ctx, http.MethodPost, c.projectPath("releases"), req, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRelease reads a release. ok is false when GitLab reports 404, which is the
// normal "not created yet" case rather than an error.
func (c *Client) GetRelease(ctx context.Context, tag string) (rel *Release, ok bool, err error) {
	var out Release
	err = c.do(ctx, http.MethodGet, c.projectPath("releases/"+url.PathEscape(tag)), nil, &out)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &out, true, nil
}

// Link is a release asset link.
type Link struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	LinkType string `json:"link_type,omitzero"`
}

// AddReleaseLink attaches one asset link to a release.
func (c *Client) AddReleaseLink(ctx context.Context, tag string, l Link) error {
	path := c.projectPath("releases/" + url.PathEscape(tag) + "/assets/links")
	return c.do(ctx, http.MethodPost, path, l, nil)
}

// Pipeline is the response of a pipeline trigger.
type Pipeline struct {
	ID     int    `json:"id"`
	WebURL string `json:"web_url,omitzero"`
	Status string `json:"status,omitzero"`
}

// triggerRequest is the body of POST /trigger/pipeline. Variables are sent as
// a map, which the API accepts alongside the JOB-TOKEN header.
type triggerRequest struct {
	Ref       string            `json:"ref"`
	Variables map[string]string `json:"variables,omitzero"`
}

// TriggerPipeline starts a pipeline in another project. The target project must
// list this project on its job-token allowlist.
func (c *Client) TriggerPipeline(ctx context.Context, project, ref string, vars map[string]string) (*Pipeline, error) {
	path := "/projects/" + url.PathEscape(project) + "/trigger/pipeline"
	var out Pipeline
	if err := c.do(ctx, http.MethodPost, path, triggerRequest{Ref: ref, Variables: vars}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

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
	defer resp.Body.Close()

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
