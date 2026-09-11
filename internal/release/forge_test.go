// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package release_test

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"git.ole-hartwig.eu/yasrt/cli/internal/forge"
	"git.ole-hartwig.eu/yasrt/cli/internal/release"
)

// fakeGitHubish serves the release endpoints that GitHub and Forgejo share.
// It records the last created body so the test can see where links ended up.
type fakeGitHubish struct {
	releases map[string]bool
	creates  atomic.Int32
	lastBody atomic.Pointer[string]
	lastAuth atomic.Pointer[string]
	srv      *httptest.Server
}

func newFakeGitHubish(t *testing.T) *fakeGitHubish {
	f := &fakeGitHubish{releases: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{repo}/releases/tags/{tag}", func(w http.ResponseWriter, r *http.Request) {
		tag := r.PathValue("tag")
		if !f.releases[tag] {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"Not Found"}`)
			return
		}
		io.WriteString(w, `{"tag_name":"`+tag+`","html_url":"https://gh/o/r/releases/tag/`+tag+`"}`)
	})
	mux.HandleFunc("POST /repos/{owner}/{repo}/releases", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		f.lastAuth.Store(&auth)
		var req map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		tag, _ := req["tag_name"].(string)
		body, _ := req["body"].(string)
		f.lastBody.Store(&body)
		f.releases[tag] = true
		f.creates.Add(1)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"tag_name":"`+tag+`","html_url":"https://gh/o/r/releases/tag/`+tag+`"}`)
	})
	// Neither platform has a bare-URL link endpoint; a call here is a bug.
	mux.HandleFunc("POST /repos/{owner}/{repo}/releases/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected asset upload on %s", r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHubish) client(t *testing.T, kind forge.Kind) forge.Client {
	t.Helper()
	c, _, err := forge.New(forge.Environment{
		Kind: kind, APIURL: f.srv.URL, Repo: "o/r", Token: "tok",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const ghishCfg = `product: image
gitlab_release:
  assets:
    - name: "binary ${version}"
      url: "https://dl/${tag}/yasrt"
      link_type: package
after_release:
  triggers:
    - project: other/repo
      ref: main
`

func TestReleaseOnGitHubAndForgejo(t *testing.T) {
	for _, tc := range []struct {
		kind     forge.Kind
		wantAuth string
		base     string
	}{
		{forge.GitHub, "Bearer tok", "https://github.com/o/r"},
		{forge.Forgejo, "token tok", "https://codeberg.org/o/r"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			s := setup(t, ghishCfg)
			f := newFakeGitHubish(t)
			o := s.opts()
			o.Forge = f.client(t, tc.kind)
			o.URLs = forge.URLs{Kind: tc.kind, Base: tc.base}
			rep := run(t, o)

			if rep.Tag != "1.1.0" {
				t.Errorf("tag = %q", rep.Tag)
			}
			if f.creates.Load() != 1 {
				t.Fatalf("creates = %d, want 1", f.creates.Load())
			}
			if got := *f.lastAuth.Load(); got != tc.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, tc.wantAuth)
			}
			// Links cannot be attached, so they must be in the body.
			body := *f.lastBody.Load()
			if !strings.Contains(body, "### Assets") || !strings.Contains(body, "[binary 1.1.0](https://dl/1.1.0/yasrt)") {
				t.Errorf("links missing from body:\n%s", body)
			}
			if !strings.Contains(body, "]("+tc.base+"/commit/") || !strings.Contains(body, "]("+tc.base+"/issues/7)") {
				t.Errorf("notes use the wrong link base:\n%s", body)
			}
			if !strings.HasPrefix(rep.ReleaseURL, "https://gh/o/r/releases/tag/") {
				t.Errorf("release URL = %q", rep.ReleaseURL)
			}
			for _, step := range []string{"notes", "changelog", "tag", "release-commit", "release"} {
				if got := stepStatus(rep, step); got != release.StepDone {
					t.Errorf("step %s = %q, want done", step, got)
				}
			}
			// Triggers have no endpoint here: reported, not fatal, not silent.
			if len(rep.Triggers) != 1 {
				t.Fatalf("triggers = %+v", rep.Triggers)
			}
			if tr := rep.Triggers[0]; tr.Error == "" || !strings.Contains(tr.Error, "no pipeline-trigger endpoint") {
				t.Errorf("trigger result = %+v, want a no-endpoint error", tr)
			}

			// A rerun finds the release and skips it.
			second, err := release.Run(context.Background(), o)
			if err != nil {
				t.Fatalf("rerun: %v", err)
			}
			if got := stepStatus(second, "release"); got != release.StepSkipped {
				t.Errorf("second release step = %q, want skipped", got)
			}
			if f.creates.Load() != 1 {
				t.Errorf("rerun created again: creates = %d", f.creates.Load())
			}
		})
	}
}

// The issue and commit link shapes differ per forge; the changelog written
// into the repository has to use the right ones.
func TestChangelogLinksFollowTheForge(t *testing.T) {
	s := setup(t, "product: image\n")
	f := newFakeGitHubish(t)
	o := s.opts()
	o.Forge = f.client(t, forge.GitHub)
	o.URLs = forge.URLs{Kind: forge.GitHub, Base: "https://github.com/o/r"}
	run(t, o)

	cl := s.tr.Read("CHANGELOG.md")
	if !strings.Contains(cl, "[#7](https://github.com/o/r/issues/7)") {
		t.Errorf("issue link:\n%s", cl)
	}
	if !strings.Contains(cl, "https://github.com/o/r/commit/") {
		t.Errorf("commit link:\n%s", cl)
	}
	if strings.Contains(cl, "/-/") {
		t.Errorf("GitLab-style path on GitHub:\n%s", cl)
	}
}
