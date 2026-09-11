// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package release_test

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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

	mu     sync.Mutex
	assets map[string]string // name -> content-type of the upload request
}

func newFakeGitHubish(t *testing.T) *fakeGitHubish {
	f := &fakeGitHubish{releases: map[string]bool{}, assets: map[string]string{}}
	mux := http.NewServeMux()
	record := func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ct := r.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "multipart/form-data") {
			if err := r.ParseMultipartForm(1 << 20); err != nil || r.MultipartForm.File["attachment"] == nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		f.mu.Lock()
		f.assets[name] = ct
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"browser_download_url":"https://dl/`+name+`"}`)
	}
	// GitHub's upload host; the release's upload_url points here.
	mux.HandleFunc("POST /uploads/releases/{id}/assets", record)
	// Forgejo attaches on the API host.
	mux.HandleFunc("POST /repos/{owner}/{repo}/releases/{id}/assets", record)
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
		io.WriteString(w, `{"id":7,"tag_name":"`+tag+`","html_url":"https://gh/o/r/releases/tag/`+tag+`",`+
			`"upload_url":"`+f.srv.URL+`/uploads/releases/7/assets{?name,label}"}`)
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

// Uploads: GitLab stores the file in the generic package registry and links
// it; GitHub takes the raw body on its upload host; Forgejo a multipart form.
func TestReleaseUploadsAssets(t *testing.T) {
	const cfg = `product: image
gitlab_release:
  assets:
    - path: "dist/*.tar.gz"
      package: yasrt
    - path: "SHA256SUMS"
      name: "checksums-${version}.txt"
      link_type: other
`
	prepare := func(t *testing.T) *scenario {
		s := setup(t, cfg)
		s.tr.Write("dist/yasrt-linux-amd64.tar.gz", "amd64")
		s.tr.Write("dist/yasrt-linux-arm64.tar.gz", "arm64")
		s.tr.Write("SHA256SUMS", "sums")
		return s
	}

	t.Run("gitlab", func(t *testing.T) {
		s := prepare(t)
		rep := run(t, s.opts())
		if got := stepStatus(rep, "release"); got != release.StepDone {
			t.Fatalf("release step = %q", got)
		}
		s.gl.mu.Lock()
		defer s.gl.mu.Unlock()
		want := map[string]string{
			"yasrt/1.1.0/yasrt-linux-amd64.tar.gz": "amd64",
			"yasrt/1.1.0/yasrt-linux-arm64.tar.gz": "arm64",
			"release/1.1.0/checksums-1.1.0.txt":    "sums",
		}
		for k, v := range want {
			if string(s.gl.uploads[k]) != v {
				t.Errorf("upload %s = %q, want %q", k, s.gl.uploads[k], v)
			}
		}
		if len(s.gl.linkURLs) != 3 {
			t.Fatalf("links = %q", s.gl.linkURLs)
		}
		for _, u := range s.gl.linkURLs {
			if !strings.Contains(u, "/packages/generic/") {
				t.Errorf("link should point at the package: %s", u)
			}
		}
	})

	for _, kind := range []forge.Kind{forge.GitHub, forge.Forgejo} {
		t.Run(string(kind), func(t *testing.T) {
			s := prepare(t)
			f := newFakeGitHubish(t)
			o := s.opts()
			o.Forge = f.client(t, kind)
			o.URLs = forge.URLs{Kind: kind, Base: "https://gh/o/r"}
			rep := run(t, o)
			if got := stepStatus(rep, "release"); got != release.StepDone {
				t.Fatalf("release step = %q", got)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.assets) != 3 {
				t.Fatalf("assets = %v", f.assets)
			}
			ct := f.assets["checksums-1.1.0.txt"]
			switch kind {
			case forge.GitHub:
				if ct != "application/octet-stream" {
					t.Errorf("github upload content-type = %q", ct)
				}
			case forge.Forgejo:
				if !strings.HasPrefix(ct, "multipart/form-data") {
					t.Errorf("forgejo upload content-type = %q", ct)
				}
			}
			// Uploads are attached, not listed; the body lists only URL links.
			if strings.Contains(*f.lastBody.Load(), "### Assets") {
				t.Errorf("uploads must not be listed as links:\n%s", *f.lastBody.Load())
			}
		})
	}

	t.Run("a path that matches nothing fails before the release exists", func(t *testing.T) {
		s := setup(t, cfg) // no dist/ files written
		_, err := release.Run(context.Background(), s.opts())
		if err == nil || !strings.Contains(err.Error(), "matches no file") {
			t.Fatalf("err = %v", err)
		}
		if s.gl.creates.Load() != 0 {
			t.Error("the release must not be created when an asset is missing")
		}
	})
}

// on_failure hooks are told what failed, so a person can be. They run for the
// failure and never turn it into a success.
func TestOnFailureHookIsToldWhatFailed(t *testing.T) {
	s := setup(t, `
product: image
hooks:
  on_failure:
    - run: ./notify.sh
`)
	s.tr.Write("notify.sh", "#!/bin/sh\nprintf '%s|%s|%s' \"$YASRT_EVENT\" \"$YASRT_FAILED_STEP\" \"$YASRT_ERROR\" > failure.txt\ncat > payload.json\n")
	if err := os.Chmod(s.tr.Dir+"/notify.sh", 0o755); err != nil {
		t.Fatal(err)
	}
	s.gl.failCreate = 5 // more than the client retries

	rep, err := release.Run(context.Background(), s.opts())
	if err == nil {
		t.Fatal("the release should have failed")
	}
	got := s.tr.Read("failure.txt")
	if !strings.HasPrefix(got, "on_failure|release|") || !strings.Contains(got, "500") {
		t.Errorf("hook saw %q", got)
	}
	if p := s.tr.Read("payload.json"); !strings.Contains(p, `"failed_step":"release"`) || !strings.Contains(p, `"error":`) {
		t.Errorf("payload:\n%s", p)
	}
	if len(rep.Hooks) != 1 || rep.Hooks[0].Event != "on_failure" {
		t.Errorf("report hooks = %+v", rep.Hooks)
	}
}

func TestOnFailureHookDoesNotRunOnSuccess(t *testing.T) {
	s := setup(t, `
product: image
hooks:
  on_failure:
    - run: ./notify.sh
`)
	s.tr.Write("notify.sh", "#!/bin/sh\ntouch failure.txt\n")
	if err := os.Chmod(s.tr.Dir+"/notify.sh", 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, s.opts())
	if s.tr.Exists("failure.txt") {
		t.Error("on_failure ran on a successful release")
	}
}
