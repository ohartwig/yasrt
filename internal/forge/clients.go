// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
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
	return r.doWithCheck(ctx, method, path, body, out, nil)
}

// doWithCheck is do with one addition for requests that create something: a
// server error or a timeout on a POST does not say whether the thing was
// created. Before retrying, `settled` is asked; if it reports the request as
// done, the retry is skipped and the earlier error forgotten. Without this a
// release that was created on the first try answers the second with 409 and
// a release that exists is reported as a failure.
func (r *rest) doWithCheck(ctx context.Context, method, path string, body, out any, settled func(context.Context) (bool, error)) error {
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
		retryable := true
		if apiErr, ok := errors.AsType[*APIError](err); ok {
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
		if settled != nil {
			done, checkErr := settled(ctx)
			if checkErr == nil && done {
				return errAlreadySettled
			}
		}
	}
	return last
}

// errAlreadySettled says the request took effect before a retry was needed;
// the caller reads the result back instead of retrying.
var errAlreadySettled = errors.New("request already took effect")

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

// raw sends a request whose body is not JSON and whose URL may live on
// another host, as GitHub's upload endpoint does. Uploads are not retried:
// the body is a stream, and a half-sent file is not something to send twice.
func (r *rest) raw(ctx context.Context, method, fullURL, contentType string, body io.Reader, size int64, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(r.authKey, r.authVal)
	if r.accept != "" {
		req.Header.Set("Accept", r.accept)
	}
	client := r.http
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	rawBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Method: method, Path: fullURL, Body: string(rawBody)}
	}
	if readErr != nil {
		return readErr
	}
	if out == nil || len(bytes.TrimSpace(rawBody)) == 0 {
		return nil
	}
	return json.Unmarshal(rawBody, out)
}

func openUpload(up Upload) (*os.File, int64, error) {
	f, err := os.Open(up.Path)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	if st.IsDir() {
		_ = f.Close()
		return nil, 0, fmt.Errorf("%s is a directory", up.Path)
	}
	return f, st.Size(), nil
}

func notFound(err error) bool {
	apiErr, ok := errors.AsType[*APIError](err)
	return ok && apiErr.Status == http.StatusNotFound
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
	exists := func(ctx context.Context) (bool, error) { _, ok, err := c.GetRelease(ctx, tag); return ok, err }
	err := c.rest.doWithCheck(ctx, http.MethodPost, c.path("releases"), req, &out, exists)
	if errors.Is(err, errAlreadySettled) {
		rel, _, getErr := c.GetRelease(ctx, tag)
		return rel, getErr
	}
	if err != nil {
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

// UploadAsset publishes the file to the generic package registry, which is
// the one upload target a job token may write to. The download URL is the
// same path, so the returned link points straight at it.
func (c *gitlabClient) UploadAsset(ctx context.Context, _ *Release, up Upload) (Link, error) {
	f, size, err := openUpload(up)
	if err != nil {
		return Link{}, err
	}
	defer func() { _ = f.Close() }()
	pkg := up.Package
	if pkg == "" {
		pkg = "release"
	}
	u := strings.TrimRight(c.rest.apiURL, "/") + c.path("packages/generic/"+
		url.PathEscape(pkg)+"/"+url.PathEscape(up.Version)+"/"+url.PathEscape(up.Name))
	if err := c.rest.raw(ctx, http.MethodPut, u, "application/octet-stream", f, size, nil); err != nil {
		return Link{}, fmt.Errorf("upload %q: %w", up.Name, err)
	}
	lt := up.LinkType
	if lt == "" {
		lt = "package"
	}
	return Link{Name: up.Name, URL: u, LinkType: lt}, nil
}

// TriggerPipeline starts a pipeline in another project. GitLab only.
// token, when set, is a pipeline trigger token of that project; empty
// means the job token, under which the pipeline runs as this job's user.
func (c *gitlabClient) TriggerPipeline(ctx context.Context, project, ref string, vars map[string]string, token string) (string, error) {
	// The trigger endpoint is the one call that ignores the JOB-TOKEN header:
	// it wants the token in the body, where a trigger token would go, and
	// answers "token is missing" otherwise. The job token is a valid value.
	if token == "" {
		token = c.rest.authVal
	}
	req := struct {
		Token     string            `json:"token"`
		Ref       string            `json:"ref"`
		Variables map[string]string `json:"variables,omitzero"`
	}{token, ref, vars}
	var out struct {
		WebURL string `json:"web_url,omitzero"`
	}
	p := "/projects/" + url.PathEscape(project) + "/trigger/pipeline"
	// A tag yasrt pushed seconds ago is not always visible to this endpoint
	// yet: GitLab answers 400 "Reference not found" and shows the tag a
	// moment later (pinup/pinup v0.16.1 and v0.17.0, six seconds after the
	// push, 2026-09-14). That one answer is waited out; every other error
	// is returned at once, as before.
	wait := TriggerRefBackoff
	for attempt := 1; ; attempt++ {
		err := c.rest.do(ctx, http.MethodPost, p, req, &out)
		if err == nil {
			return out.WebURL, nil
		}
		if attempt >= triggerRefTries || !isRefNotFound(err) {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait * time.Duration(attempt)):
		}
	}
}

// triggerRefTries bounds the wait for a just-pushed ref to become visible to
// the trigger endpoint: 5 + 10 + 15 + 20 s at the default backoff.
const triggerRefTries = 5

// TriggerRefBackoff is the first wait before retrying a trigger whose ref the
// server does not see yet; each further attempt waits one step longer. A
// variable so the tests need not sit through it.
var TriggerRefBackoff = 5 * time.Second

func isRefNotFound(err error) bool {
	apiErr, ok := errors.AsType[*APIError](err)
	return ok && apiErr.Status == http.StatusBadRequest && strings.Contains(apiErr.Body, "Reference not found")
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
	ID        int64  `json:"id,omitzero"`
	TagName   string `json:"tag_name"`
	Name      string `json:"name,omitzero"`
	HTMLURL   string `json:"html_url,omitzero"`
	UploadURL string `json:"upload_url,omitzero"`
}

func (r ghRelease) release() *Release {
	// GitHub's upload_url carries an RFC 6570 template suffix, {?name,label}.
	up, _, _ := strings.Cut(r.UploadURL, "{")
	return &Release{TagName: r.TagName, Name: r.Name, URL: r.HTMLURL, ID: r.ID, UploadURL: up}
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
	return out.release(), true, nil
}

func (c *ghClient) CreateRelease(ctx context.Context, tag, name, body string) (*Release, error) {
	req := struct {
		TagName string `json:"tag_name"`
		Name    string `json:"name,omitzero"`
		Body    string `json:"body,omitzero"`
	}{tag, name, body}
	var out ghRelease
	exists := func(ctx context.Context) (bool, error) { _, ok, err := c.GetRelease(ctx, tag); return ok, err }
	err := c.rest.doWithCheck(ctx, http.MethodPost, c.path(""), req, &out, exists)
	if errors.Is(err, errAlreadySettled) {
		rel, _, getErr := c.GetRelease(ctx, tag)
		return rel, getErr
	}
	if err != nil {
		return nil, err
	}
	return out.release(), nil
}

// UploadAsset attaches the file to the release. GitHub takes the raw body on
// its upload host; Forgejo takes a multipart form on the API host. Both answer
// with the browser download URL.
func (c *ghClient) UploadAsset(ctx context.Context, rel *Release, up Upload) (Link, error) {
	if rel == nil || rel.ID == 0 {
		return Link{}, fmt.Errorf("upload %q: the release must exist before assets can be attached", up.Name)
	}
	f, size, err := openUpload(up)
	if err != nil {
		return Link{}, err
	}
	defer func() { _ = f.Close() }()

	var out struct {
		URL string `json:"browser_download_url,omitzero"`
	}
	switch c.kind {
	case GitHub:
		base := rel.UploadURL
		if base == "" {
			return Link{}, fmt.Errorf("upload %q: the release carries no upload URL", up.Name)
		}
		u := base + "?name=" + url.QueryEscape(up.Name)
		if err := c.rest.raw(ctx, http.MethodPost, u, "application/octet-stream", f, size, &out); err != nil {
			return Link{}, fmt.Errorf("upload %q: %w", up.Name, err)
		}
	default:
		// Forgejo wants the file as a form field named attachment. Building the
		// form in memory keeps this simple; release assets are not gigabytes.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("attachment", up.Name)
		if err != nil {
			return Link{}, err
		}
		if _, err := io.Copy(part, f); err != nil {
			return Link{}, err
		}
		if err := mw.Close(); err != nil {
			return Link{}, err
		}
		u := strings.TrimRight(c.rest.apiURL, "/") + c.path(fmt.Sprintf("/%d/assets", rel.ID)) +
			"?name=" + url.QueryEscape(up.Name)
		if err := c.rest.raw(ctx, http.MethodPost, u, mw.FormDataContentType(), &buf, int64(buf.Len()), &out); err != nil {
			return Link{}, fmt.Errorf("upload %q: %w", up.Name, err)
		}
	}
	return Link{Name: up.Name, URL: out.URL, LinkType: up.LinkType}, nil
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
