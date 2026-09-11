// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package release_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.ole-hartwig.eu/yasrt/cli/internal/analyze"
	"git.ole-hartwig.eu/yasrt/cli/internal/config"
	"git.ole-hartwig.eu/yasrt/cli/internal/git"
	"git.ole-hartwig.eu/yasrt/cli/internal/gitlab"
	"git.ole-hartwig.eu/yasrt/cli/internal/release"
	"git.ole-hartwig.eu/yasrt/cli/internal/testrepo"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))
var fixedNow = time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)

// fakeGitLab records what the API was asked to do.
type fakeGitLab struct {
	releases   map[string]bool
	creates    atomic.Int32
	links      atomic.Int32
	triggers   atomic.Int32
	failCreate int
	srv        *httptest.Server
}

func newFakeGitLab(t *testing.T) *fakeGitLab {
	f := &fakeGitLab{releases: map[string]bool{}}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v4/projects/{id}/releases/{tag}", func(w http.ResponseWriter, r *http.Request) {
		tag := r.PathValue("tag")
		if !f.releases[tag] {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"404 Not found"}`)
			return
		}
		io.WriteString(w, `{"tag_name":"`+tag+`","_links":{"self":"https://git/x/-/releases/`+tag+`"}}`)
	})
	mux.HandleFunc("POST /api/v4/projects/{id}/releases", func(w http.ResponseWriter, r *http.Request) {
		if f.failCreate > 0 {
			f.failCreate--
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var req map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		tag, _ := req["tag_name"].(string)
		f.releases[tag] = true
		f.creates.Add(1)
		io.WriteString(w, `{"tag_name":"`+tag+`","_links":{"self":"https://git/x/-/releases/`+tag+`"}}`)
	})
	mux.HandleFunc("POST /api/v4/projects/{id}/releases/{tag}/assets/links", func(w http.ResponseWriter, r *http.Request) {
		f.links.Add(1)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /api/v4/projects/{id}/trigger/pipeline", func(w http.ResponseWriter, r *http.Request) {
		f.triggers.Add(1)
		io.WriteString(w, `{"id":7,"web_url":"https://git/p/-/pipelines/7"}`)
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitLab) client() *gitlab.Client {
	c := gitlab.New(f.srv.URL+"/api/v4", "42", "job-token")
	c.Backoff = time.Millisecond
	return c
}

// scenario builds a repository with one released tag and one unreleased feat.
type scenario struct {
	tr   *testrepo.Repo
	repo *git.Repo
	cfg  *config.Config
	res  *analyze.Result
	gl   *fakeGitLab
}

func setup(t *testing.T, cfgSrc string) *scenario {
	t.Helper()
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.0.0")
	tr.CommitFile("src/b.go", "2", "feat(api): add the thing\n\nCloses: #7")
	tr.WithRemote()
	tr.Git("push", "-q", "origin", "1.0.0")

	repo, err := git.Open(tr.Dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(strings.NewReader(cfgSrc), "test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	res, err := analyze.Run(repo, cfg, analyze.Options{}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != analyze.StatusRelease {
		t.Fatalf("precondition: status = %s", res.Status)
	}
	return &scenario{tr: tr, repo: repo, cfg: cfg, res: res, gl: newFakeGitLab(t)}
}

func (s *scenario) opts() release.Options {
	return release.Options{
		Repo: s.repo, Config: s.cfg, Result: s.res, Client: s.gl.client(),
		Remote: "origin", Branch: "main", ProjectURL: "https://git/x",
		Now: fixedNow, Log: discard,
	}
}

func run(t *testing.T, o release.Options) *release.Report {
	t.Helper()
	rep, err := release.Run(context.Background(), o)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	return rep
}

func stepStatus(rep *release.Report, name string) release.StepStatus {
	for _, s := range rep.Steps {
		if s.Name == name {
			return s.Status
		}
	}
	return ""
}

func TestFullRelease(t *testing.T) {
	s := setup(t, "product: image\n")
	rep := run(t, s.opts())

	if rep.Tag != "1.1.0" {
		t.Errorf("tag = %q", rep.Tag)
	}
	if got := s.tr.RemoteTags(); !slices.Contains(got, "1.1.0") {
		t.Errorf("remote tags = %q", got)
	}
	if !s.tr.Exists("CHANGELOG.md") {
		t.Fatal("CHANGELOG.md should have been written")
	}
	cl := s.tr.Read("CHANGELOG.md")
	if !strings.Contains(cl, "## [1.1.0] - 2026-09-11") {
		t.Errorf("changelog:\n%s", cl)
	}
	if !strings.Contains(cl, "[#7](https://git/x/-/issues/7)") {
		t.Errorf("issue reference missing:\n%s", cl)
	}
	if s.gl.creates.Load() != 1 {
		t.Errorf("creates = %d", s.gl.creates.Load())
	}
	if rep.ReleaseURL == "" {
		t.Error("report should carry the release URL")
	}
	for _, step := range []string{"notes", "changelog", "tag", "release-commit", "gitlab-release"} {
		if got := stepStatus(rep, step); got != release.StepDone {
			t.Errorf("step %s = %q, want done", step, got)
		}
	}
}

// The tag must name the commit that was built, not the release commit that
// follows it. This is the whole provenance argument.
func TestTagPointsAtTheBuiltCommit(t *testing.T) {
	s := setup(t, "product: image\n")
	built := s.res.Commit
	run(t, s.opts())

	tagged, err := s.repo.TagSHA("1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if tagged != built {
		t.Fatalf("tag points at %s, want the built commit %s", tagged, built)
	}
	head, _ := s.repo.HeadSHA()
	if head == built {
		t.Fatal("precondition: the release commit should have moved HEAD")
	}
}

func TestReleaseCommitDisabled(t *testing.T) {
	s := setup(t, "product: image\nrelease_commit:\n  enabled: false\n")
	before, _ := s.repo.HeadSHA()
	rep := run(t, s.opts())

	if s.tr.Exists("CHANGELOG.md") {
		t.Error("no release commit means no changelog file")
	}
	after, _ := s.repo.HeadSHA()
	if before != after {
		t.Error("HEAD must not move when the release commit is disabled")
	}
	if got := stepStatus(rep, "release-commit"); got != release.StepSkipped {
		t.Errorf("step = %q", got)
	}
	// The release itself still happens.
	if s.gl.creates.Load() != 1 {
		t.Errorf("creates = %d", s.gl.creates.Load())
	}
}

func TestRefusesWhenStatusIsNotRelease(t *testing.T) {
	s := setup(t, "product: image\n")
	s.res.Status = analyze.StatusNoBump
	_, err := release.Run(context.Background(), s.opts())
	if !errors.Is(err, release.ErrNotReleasing) {
		t.Fatalf("err = %v", err)
	}
}

// Someone pushed between analysis and release: tagging now would tag something
// that was never built.
func TestRefusesWhenHeadMoved(t *testing.T) {
	s := setup(t, "product: image\n")
	s.tr.CommitFile("src/c.go", "3", "feat: pushed in the meantime")

	_, err := release.Run(context.Background(), s.opts())
	if !errors.Is(err, release.ErrCommitMoved) {
		t.Fatalf("err = %v", err)
	}
	if got := s.tr.RemoteTags(); slices.Contains(got, "1.1.0") {
		t.Error("nothing should have been pushed")
	}
}

func TestRefusesWhenTagExistsElsewhere(t *testing.T) {
	s := setup(t, "product: image\n")
	// Publish the target tag on the previous commit, as a stale run would have.
	s.tr.Git("tag", "-a", "-m", "1.1.0", "1.1.0", "HEAD~1")
	s.tr.Git("push", "-q", "origin", "1.1.0")
	s.tr.Git("tag", "-d", "1.1.0")

	_, err := release.Run(context.Background(), s.opts())
	if !errors.Is(err, release.ErrTagMismatch) {
		t.Fatalf("err = %v", err)
	}
}

// A re-run after a completed release must converge without duplicating anything.
func TestRerunIsIdempotent(t *testing.T) {
	s := setup(t, "product: image\n")
	first := run(t, s.opts())

	// A retry reuses the same .release.env, so RELEASE_COMMIT still names the
	// built commit while HEAD has moved on by our own release commit.
	second, err := release.Run(context.Background(), s.opts())
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}

	if s.gl.creates.Load() != 1 {
		t.Errorf("the release must not be created twice: %d", s.gl.creates.Load())
	}
	if got := stepStatus(second, "gitlab-release"); got != release.StepSkipped {
		t.Errorf("second gitlab-release = %q, want skipped", got)
	}
	if got := stepStatus(second, "changelog"); got != release.StepSkipped {
		t.Errorf("second changelog = %q, want skipped", got)
	}
	if strings.Count(s.tr.Read("CHANGELOG.md"), "## [1.1.0]") != 1 {
		t.Errorf("changelog gained a duplicate entry:\n%s", s.tr.Read("CHANGELOG.md"))
	}
	if first.Tag != second.Tag {
		t.Errorf("tags diverged: %q vs %q", first.Tag, second.Tag)
	}
}

// A failure after the tag is pushed must leave a complete release on re-run.
func TestRerunAfterFailedGitLabStep(t *testing.T) {
	s := setup(t, "product: image\n")
	s.gl.failCreate = 5 // exceeds the retry budget

	if _, err := release.Run(context.Background(), s.opts()); err == nil {
		t.Fatal("expected the GitLab step to fail")
	}
	if got := s.tr.RemoteTags(); !slices.Contains(got, "1.1.0") {
		t.Fatalf("the tag should already be published: %q", got)
	}

	// Retry: the tag is found, skipped, and the rest completes.
	s.gl.failCreate = 0
	rep, err := release.Run(context.Background(), s.opts())
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if got := stepStatus(rep, "tag"); got != release.StepSkipped {
		t.Errorf("tag step = %q, want skipped", got)
	}
	if s.gl.creates.Load() != 1 {
		t.Errorf("creates = %d", s.gl.creates.Load())
	}
}

func TestTriggersAreNonFatal(t *testing.T) {
	s := setup(t, `
product: image
after_release:
  triggers:
    - project: "devops/renovate-runner"
      ref: main
      variables: { FAST_LANE: "true" }
`)
	rep := run(t, s.opts())
	if s.gl.triggers.Load() != 1 {
		t.Errorf("triggers = %d", s.gl.triggers.Load())
	}
	if len(rep.Triggers) != 1 || rep.Triggers[0].Pipeline == "" {
		t.Errorf("report triggers = %+v", rep.Triggers)
	}
}

func TestTriggerFailureDoesNotFailTheRelease(t *testing.T) {
	s := setup(t, `
product: image
after_release:
  triggers:
    - project: "devops/nonexistent"
      ref: main
`)
	// Point the client at a server that rejects triggers.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/trigger/pipeline") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{}`)
			return
		}
		io.WriteString(w, `{"tag_name":"1.1.0"}`)
	}))
	t.Cleanup(srv.Close)
	o := s.opts()
	c := gitlab.New(srv.URL+"/api/v4", "42", "job-token")
	c.Backoff = time.Millisecond
	o.Client = c

	rep, err := release.Run(context.Background(), o)
	if err != nil {
		t.Fatalf("a failing trigger must not fail the release: %v", err)
	}
	if len(rep.Triggers) != 1 || rep.Triggers[0].Error == "" {
		t.Errorf("the failure should be recorded: %+v", rep.Triggers)
	}
}

func TestSignRequiredFailsBeforeAnyWrite(t *testing.T) {
	s := setup(t, "product: image\nrelease_commit:\n  sign: required\n")
	o := s.opts()
	o.GPGKeyB64 = "" // no key available

	_, err := release.Run(context.Background(), o)
	if !errors.Is(err, release.ErrSigningRequired) {
		t.Fatalf("err = %v", err)
	}
	if got := s.tr.RemoteTags(); slices.Contains(got, "1.1.0") {
		t.Error("nothing may be pushed when required signing cannot be satisfied")
	}
	if s.tr.Exists("CHANGELOG.md") {
		t.Error("nothing may be written either")
	}
}

func TestSignAutoDegradesWithoutKey(t *testing.T) {
	s := setup(t, "product: image\nrelease_commit:\n  sign: auto\n")
	rep := run(t, s.opts())
	if rep.Signed {
		t.Error("without a key the release is unsigned")
	}
	if got := stepStatus(rep, "tag"); got != release.StepDone {
		t.Errorf("the release should still happen: %q", got)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	s := setup(t, "product: image\n")
	o := s.opts()
	o.DryRun = true
	rep := run(t, o)

	if s.tr.Exists("CHANGELOG.md") {
		t.Error("dry run wrote the changelog")
	}
	if got := s.tr.RemoteTags(); slices.Contains(got, "1.1.0") {
		t.Error("dry run pushed a tag")
	}
	if s.gl.creates.Load() != 0 {
		t.Error("dry run called the API")
	}
	if rep.Notes == "" {
		t.Error("a dry run should still show the notes it would publish")
	}
}

func TestReportIsWritable(t *testing.T) {
	s := setup(t, "product: image\n")
	rep := run(t, s.opts())
	path := t.TempDir() + "/release-report.json"
	if err := release.WriteReport(path, rep); err != nil {
		t.Fatal(err)
	}
	var back release.Report
	b := readFile(t, path)
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Tag != "1.1.0" || len(back.Steps) == 0 {
		t.Errorf("report round trip: %+v", back)
	}
}

func TestReleaseNotesReachGitLab(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		io.WriteString(w, `{"tag_name":"1.1.0"}`)
	}))
	t.Cleanup(srv.Close)

	s := setup(t, "product: image\n")
	o := s.opts()
	o.Client = gitlab.New(srv.URL+"/api/v4", "42", "job-token")
	run(t, o)

	desc, _ := body["description"].(string)
	if !strings.Contains(desc, ":sparkles: Features") || !strings.Contains(desc, "api: add the thing") {
		t.Errorf("description = %q", desc)
	}
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
