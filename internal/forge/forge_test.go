// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package forge_test

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.ole-hartwig.eu/yasrt/cli/internal/forge"
)

func envOf(m map[string]string) forge.Getenv {
	return func(k string) string { return m[k] }
}

func TestDetect(t *testing.T) {
	t.Run("gitlab", func(t *testing.T) {
		e, ok := forge.Detect(envOf(map[string]string{
			"GITLAB_CI": "true", "CI_API_V4_URL": "https://git.example/api/v4",
			"CI_PROJECT_ID": "42", "CI_PROJECT_URL": "https://git.example/a/b",
			"CI_JOB_TOKEN": "t", "CI_COMMIT_BRANCH": "main", "CI_DEFAULT_BRANCH": "main",
		}))
		if !ok || e.Kind != forge.GitLab {
			t.Fatalf("kind = %q ok = %v", e.Kind, ok)
		}
		if e.Repo != "42" || e.ProjectURL != "https://git.example/a/b" {
			t.Errorf("env = %+v", e)
		}
	})

	t.Run("github", func(t *testing.T) {
		e, ok := forge.Detect(envOf(map[string]string{
			"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com",
			"GITHUB_REPOSITORY": "acme/widget", "GITHUB_TOKEN": "t",
			"GITHUB_REF": "refs/heads/main",
		}))
		if !ok || e.Kind != forge.GitHub {
			t.Fatalf("kind = %q", e.Kind)
		}
		if e.APIURL != "https://api.github.com" {
			t.Errorf("api = %q", e.APIURL)
		}
		if e.ProjectURL != "https://github.com/acme/widget" || e.Branch != "main" {
			t.Errorf("env = %+v", e)
		}
	})

	// Forgejo's runner sets GITHUB_* for compatibility. Detecting it as GitHub
	// would send the release to api.github.com with a token that does not
	// belong there, so the server URL decides.
	t.Run("forgejo is not github", func(t *testing.T) {
		e, ok := forge.Detect(envOf(map[string]string{
			"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://code.example.org",
			"GITHUB_REPOSITORY": "acme/widget", "GITHUB_TOKEN": "t",
		}))
		if !ok || e.Kind != forge.Forgejo {
			t.Fatalf("kind = %q, want forgejo", e.Kind)
		}
		if e.APIURL != "https://code.example.org/api/v1" {
			t.Errorf("api = %q", e.APIURL)
		}
	})

	t.Run("forgejo by its own marker", func(t *testing.T) {
		e, _ := forge.Detect(envOf(map[string]string{
			"FORGEJO_ACTIONS": "true", "GITHUB_REPOSITORY": "a/b",
			"GITHUB_SERVER_URL": "https://github.com", "FORGEJO_TOKEN": "t",
		}))
		if e.Kind != forge.Forgejo {
			t.Errorf("kind = %q", e.Kind)
		}
	})

	t.Run("nothing", func(t *testing.T) {
		if _, ok := forge.Detect(envOf(map[string]string{})); ok {
			t.Error("no CI markers should detect nothing")
		}
	})

	// A tag build is not on a branch, and saying otherwise would make a
	// prerelease or maintenance branch match by accident.
	t.Run("a tag ref yields no branch", func(t *testing.T) {
		e, _ := forge.Detect(envOf(map[string]string{
			"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "a/b",
			"GITHUB_REF": "refs/tags/v1.0.0", "GITHUB_TOKEN": "t",
		}))
		if e.Branch != "" {
			t.Errorf("branch = %q", e.Branch)
		}
	})
}

func TestURLShapes(t *testing.T) {
	for _, tc := range []struct {
		kind       forge.Kind
		wantChange string
	}{
		{forge.GitLab, "https://h/a/b/merge_requests/7"},
		{forge.GitHub, "https://h/a/b/pull/7"},
		{forge.Forgejo, "https://h/a/b/pulls/7"},
	} {
		u := forge.URLs{Kind: tc.kind, Base: "https://h/a/b"}
		if got := u.Change("7"); got != tc.wantChange {
			t.Errorf("%s change = %q, want %q", tc.kind, got, tc.wantChange)
		}
		// These are the same everywhere.
		if got := u.Commit("abc"); got != "https://h/a/b/commit/abc" {
			t.Errorf("%s commit = %q", tc.kind, got)
		}
		if got := u.Compare("1.0.0", "1.1.0"); got != "https://h/a/b/compare/1.0.0...1.1.0" {
			t.Errorf("%s compare = %q", tc.kind, got)
		}
		if got := u.Issue("7"); got != "https://h/a/b/issues/7" {
			t.Errorf("%s issue = %q", tc.kind, got)
		}
	}
	// Without a base there is nothing to link to, and a half-built URL would
	// be worse than plain text.
	if got := (forge.URLs{Kind: forge.GitHub}).Commit("abc"); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestPushCredentials(t *testing.T) {
	for kind, want := range map[forge.Kind]string{
		forge.GitLab:  "gitlab-ci-token:tok",
		forge.GitHub:  "x-access-token:tok",
		forge.Forgejo: "tok:x-oauth-basic",
	} {
		if got := forge.PushCredentials(kind, "tok"); got != want {
			t.Errorf("%s = %q, want %q", kind, got, want)
		}
	}
}

// One server per platform, answering the endpoint that platform actually has.
func clientFor(t *testing.T, kind forge.Kind, h http.HandlerFunc) forge.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	api := srv.URL + "/api/v4"
	repo := "42"
	if kind != forge.GitLab {
		api = srv.URL
		repo = "acme/widget"
	}
	c, _, err := forge.New(forge.Environment{
		Kind: kind, APIURL: api, Repo: repo, Token: "tok",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCreateReleaseSpeaksEachAPI(t *testing.T) {
	for _, tc := range []struct {
		kind           forge.Kind
		wantPath       string
		wantAuthHeader string
		wantAuthValue  string
		bodyField      string
	}{
		{forge.GitLab, "/api/v4/projects/42/releases", "Job-Token", "tok", "description"},
		{forge.GitHub, "/repos/acme/widget/releases", "Authorization", "Bearer tok", "body"},
		{forge.Forgejo, "/repos/acme/widget/releases", "Authorization", "token tok", "body"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			var gotPath, gotAuth string
			var body map[string]any
			c := clientFor(t, tc.kind, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAuth = r.Header.Get(tc.wantAuthHeader)
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &body)
				io.WriteString(w, `{"tag_name":"1.1.0","html_url":"https://h/r/1.1.0","_links":{"self":"https://h/r/1.1.0"}}`)
			})
			rel, err := c.CreateRelease(context.Background(), "1.1.0", "1.1.0", "the notes")
			if err != nil {
				t.Fatal(err)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tc.wantPath)
			}
			if gotAuth != tc.wantAuthValue {
				t.Errorf("%s = %q, want %q", tc.wantAuthHeader, gotAuth, tc.wantAuthValue)
			}
			if body[tc.bodyField] != "the notes" {
				t.Errorf("the notes must travel in %q; body = %+v", tc.bodyField, body)
			}
			if rel.URL == "" {
				t.Error("the release URL should be read back")
			}
		})
	}
}

func TestMissingReleaseIsNotAnError(t *testing.T) {
	for _, kind := range forge.Kinds() {
		c := clientFor(t, kind, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"not found"}`)
		})
		rel, ok, err := c.GetRelease(context.Background(), "1.1.0")
		if err != nil {
			t.Errorf("%s: a missing release is the normal case: %v", kind, err)
		}
		if ok || rel != nil {
			t.Errorf("%s: ok=%v rel=%+v", kind, ok, rel)
		}
	}
}

// Only GitLab models a bare URL as a release asset. The others must say so
// rather than silently dropping the links.
func TestLinkSupportIsStated(t *testing.T) {
	for _, kind := range forge.Kinds() {
		c := clientFor(t, kind, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
		})
		links := []forge.Link{{Name: "binary", URL: "https://example/x"}}
		err := c.AddLinks(context.Background(), "1.1.0", links)
		if kind == forge.GitLab {
			if !c.SupportsLinks() || err != nil {
				t.Errorf("gitlab should attach links: %v", err)
			}
			continue
		}
		if c.SupportsLinks() {
			t.Errorf("%s has no link endpoint", kind)
		}
		if err == nil || !strings.Contains(err.Error(), "release body") {
			t.Errorf("%s should say where the links went instead: %v", kind, err)
		}
	}
}

func TestLinksAsMarkdown(t *testing.T) {
	out := forge.LinksAsMarkdown([]forge.Link{
		{Name: "binary", URL: "https://example/x"},
		{Name: "sums", URL: "https://example/s"},
	})
	if !strings.Contains(out, "* [binary](https://example/x)") || !strings.Contains(out, "### Assets") {
		t.Errorf("got %q", out)
	}
	if forge.LinksAsMarkdown(nil) != "" {
		t.Error("nothing to render for no links")
	}
}

func TestTriggerSupport(t *testing.T) {
	for _, kind := range forge.Kinds() {
		c := clientFor(t, kind, func(w http.ResponseWriter, r *http.Request) {})
		want := kind == forge.GitLab
		if c.SupportsTriggers() != want {
			t.Errorf("%s triggers = %v, want %v", kind, c.SupportsTriggers(), want)
		}
	}
}

func TestExplicitKindOverridesDetection(t *testing.T) {
	e := forge.Environment{Kind: forge.GitLab, APIURL: "https://x", Repo: "a/b", Token: "t"}
	c, _, err := forge.New(e, "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind() != forge.Forgejo {
		t.Errorf("kind = %q", c.Kind())
	}
	if _, _, err := forge.New(e, "bitbucket"); err == nil {
		t.Error("an unknown forge should be rejected by name")
	}
}

func TestIncompleteEnvironmentIsReported(t *testing.T) {
	_, _, err := forge.New(forge.Environment{Kind: forge.GitHub, APIURL: "https://x"}, "")
	if err == nil || !strings.Contains(err.Error(), "repo=") {
		t.Errorf("the error should say what is missing: %v", err)
	}
}
