// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package forge

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
	return fmt.Sprintf("%s %s: %d %s: %s", e.Method, e.Path, e.Status, http.StatusText(e.Status), body)
}

func (e *APIError) Retryable() bool { return e.Status >= 500 || e.Status == http.StatusTooManyRequests }

// rest is the small HTTP client all three platforms share. They differ in how
// they authenticate and in what their endpoints are called, not in how a
// request is made.
type rest struct {
	apiURL  string
	authKey string
	authVal string
	accept  string
	http    *http.Client
	backoff time.Duration
	tries   int
}

func (r *rest) do(ctx context.Context, method, path string, body, out any) error {
	tries := r.tries
	if tries <= 0 {
		tries = 3
	}
	wait := r.backoff
	if wait <= 0 {
		wait = 500 * time.Millisecond
	}
	var last error
	for attempt := 1; attempt <= tries; attempt++ {
		err := r.attempt(ctx, method, path, body, out)
		if err == nil {
			return nil
		}
		last = err
		var apiErr *APIError
		retryable := true
		if errors.As(err, &apiErr) {
			retryable = apiErr.Retryable()
		}
		if !retryable || attempt == tries {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
	return last
}

func (r *rest) attempt(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.apiURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(r.authKey, r.authVal)
	if r.accept != "" {
		req.Header.Set("Accept", r.accept)
	}
	client := r.http
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
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

func notFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// ---------- GitLab ----------

type gitlabClient struct {
	rest rest
	repo string
}

func newGitLab(e Environment) Client {
	return &gitlabClient{
		rest: rest{apiURL: e.APIURL, authKey: "JOB-TOKEN", authVal: e.Token, accept: "application/json"},
		repo: e.Repo,
	}
}

func (c *gitlabClient) Kind() Kind          { return GitLab }
func (c *gitlabClient) SupportsLinks() bool { return true }

// Only GitLab exposes the pipeline-trigger endpoint yasrt posts to.
func (c *gitlabClient) SupportsTriggers() bool { return true }

func (c *gitlabClient) path(suffix string) string {
	return "/projects/" + url.PathEscape(c.repo) + "/" + suffix
}

type glRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name,omitzero"`
	Links   struct {
		Self string `json:"self,omitzero"`
	} `json:"_links,omitzero"`
}

func (c *gitlabClient) GetRelease(ctx context.Context, tag string) (*Release, bool, error) {
	var out glRelease
	err := c.rest.do(ctx, http.MethodGet, c.path("releases/"+url.PathEscape(tag)), nil, &out)
	if notFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &Release{TagName: out.TagName, Name: out.Name, URL: out.Links.Self}, true, nil
}

func (c *gitlabClient) CreateRelease(ctx context.Context, tag, name, body string) (*Release, error) {
	req := struct {
		TagName     string `json:"tag_name"`
		Name        string `json:"name,omitzero"`
		Description string `json:"description,omitzero"`
	}{tag, name, body}
	var out glRelease
	if err := c.rest.do(ctx, http.MethodPost, c.path("releases"), req, &out); err != nil {
		return nil, err
	}
	return &Release{TagName: out.TagName, Name: out.Name, URL: out.Links.Self}, nil
}

func (c *gitlabClient) AddLinks(ctx context.Context, tag string, links []Link) error {
	var errs []error
	for _, l := range links {
		req := struct {
			Name     string `json:"name"`
			URL      string `json:"url"`
			LinkType string `json:"link_type,omitzero"`
		}{l.Name, l.URL, l.LinkType}
		p := c.path("releases/" + url.PathEscape(tag) + "/assets/links")
		if err := c.rest.do(ctx, http.MethodPost, p, req, nil); err != nil {
			errs = append(errs, fmt.Errorf("link %q: %w", l.Name, err))
		}
	}
	return errors.Join(errs...)
}

// TriggerPipeline starts a pipeline in another project. GitLab only.
func (c *gitlabClient) TriggerPipeline(ctx context.Context, project, ref string, vars map[string]string) (string, error) {
	req := struct {
		Ref       string            `json:"ref"`
		Variables map[string]string `json:"variables,omitzero"`
	}{ref, vars}
	var out struct {
		WebURL string `json:"web_url,omitzero"`
	}
	p := "/projects/" + url.PathEscape(project) + "/trigger/pipeline"
	if err := c.rest.do(ctx, http.MethodPost, p, req, &out); err != nil {
		return "", err
	}
	return out.WebURL, nil
}

// ---------- GitHub and Forgejo ----------

// Forgejo inherited Gitea's API, which was modelled on GitHub's: the release
// endpoints are the same shape and differ only in the API root and in how the
// token is presented.
type ghClient struct {
	rest rest
	repo string
	kind Kind
}

func newGitHubish(e Environment) Client {
	authVal := "Bearer " + e.Token
	accept := "application/vnd.github+json"
	if e.Kind == Forgejo {
		authVal = "token " + e.Token
		accept = "application/json"
	}
	return &ghClient{
		rest: rest{apiURL: e.APIURL, authKey: "Authorization", authVal: authVal, accept: accept},
		repo: e.Repo,
		kind: e.Kind,
	}
}

func (c *ghClient) Kind() Kind { return c.kind }

// Neither models a bare URL as a release asset; both attach uploaded files.
// Links therefore go into the body, which the caller is told about.
func (c *ghClient) SupportsLinks() bool { return false }

// Neither has the pipeline-trigger endpoint yasrt posts to. Dispatching a
// workflow is a different call with different permissions, and inventing a
// mapping would be guessing at what the target expects.
func (c *ghClient) SupportsTriggers() bool { return false }

func (c *ghClient) path(suffix string) string {
	return "/repos/" + c.repo + "/releases" + suffix
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name,omitzero"`
	HTMLURL string `json:"html_url,omitzero"`
}

func (c *ghClient) GetRelease(ctx context.Context, tag string) (*Release, bool, error) {
	var out ghRelease
	err := c.rest.do(ctx, http.MethodGet, c.path("/tags/"+url.PathEscape(tag)), nil, &out)
	if notFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &Release{TagName: out.TagName, Name: out.Name, URL: out.HTMLURL}, true, nil
}

func (c *ghClient) CreateRelease(ctx context.Context, tag, name, body string) (*Release, error) {
	req := struct {
		TagName string `json:"tag_name"`
		Name    string `json:"name,omitzero"`
		Body    string `json:"body,omitzero"`
	}{tag, name, body}
	var out ghRelease
	if err := c.rest.do(ctx, http.MethodPost, c.path(""), req, &out); err != nil {
		return nil, err
	}
	return &Release{TagName: out.TagName, Name: out.Name, URL: out.HTMLURL}, nil
}

func (c *ghClient) AddLinks(ctx context.Context, tag string, links []Link) error {
	if len(links) == 0 {
		return nil
	}
	return fmt.Errorf("%s has no release-link endpoint; %d link(s) were appended to the release body instead", c.kind, len(links))
}

// LinksAsMarkdown renders links for a platform that cannot attach them, so
// they end up in the body rather than being lost.
func LinksAsMarkdown(links []Link) string {
	if len(links) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n### Assets\n\n")
	for _, l := range links {
		fmt.Fprintf(&b, "* [%s](%s)\n", l.Name, l.URL)
	}
	return b.String()
}
