// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package git_test

import (
	"slices"
	"strings"
	"testing"

	"git.ole-hartwig.eu/yasrt/cli/internal/git"
	"git.ole-hartwig.eu/yasrt/cli/internal/testrepo"
)

func open(t *testing.T, dir string) *git.Repo {
	t.Helper()
	r, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// mustRun is a diagnostic helper: it never fails the test, it only produces
// context for one that already has.
func mustRun(t *testing.T, r *git.Repo, args ...string) string {
	t.Helper()
	out, err := r.Run(args...)
	if err != nil {
		return "(diagnostic command failed: " + err.Error() + ")"
	}
	return out
}

func TestOpenRejectsNonRepo(t *testing.T) {
	if _, err := git.Open(t.TempDir()); err == nil {
		t.Fatal("expected an error outside a work tree")
	}
}

func TestLogParsesMultilineMessages(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("a.txt", "1", "feat: first")
	tr.CommitFile("b.txt", "2", "fix(scope): second\n\nbody line one\nbody line two\n\nBREAKING CHANGE: gone")

	r := open(t, tr.Dir)
	commits, err := r.Log("", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		// This assertion has failed twice in full-suite runs and never in
		// isolation or under -race; dump everything so the next occurrence
		// explains itself instead of needing a re-run to reproduce.
		t.Fatalf("got %d commits, want 2: %#v\nraw log:\n%s",
			len(commits), commits, mustRun(t, r, "log", "--format=%H %h %an <%ae>%n%B%n--"))
	}
	// Newest first. Every failure below dumps the raw log as well: this test
	// has failed four times in full-suite runs and never once when looked for,
	// so the next occurrence has to explain itself rather than need a repro.
	raw := mustRun(t, r, "log", "--format=%H %h %an <%ae>%n%B%n--")
	if !strings.HasPrefix(commits[0].Message, "fix(scope): second") {
		t.Errorf("message = %q\nparsed: %#v\nraw log:\n%s", commits[0].Message, commits, raw)
	}
	if !strings.Contains(commits[0].Message, "BREAKING CHANGE: gone") {
		t.Errorf("multi-line message truncated: %q\nraw log:\n%s", commits[0].Message, raw)
	}
	if commits[0].AuthorEmail != "test@example.invalid" {
		t.Errorf("author = %q\nraw log:\n%s", commits[0].AuthorEmail, raw)
	}
	if commits[0].ShortSHA == "" || !strings.HasPrefix(commits[0].SHA, commits[0].ShortSHA) {
		t.Errorf("sha=%q short=%q\nraw log:\n%s", commits[0].SHA, commits[0].ShortSHA, raw)
	}
}

func TestLogRange(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("a.txt", "1", "feat: first")
	tr.Tag("1.0.0")
	tr.CommitFile("b.txt", "2", "fix: second")
	tr.CommitFile("c.txt", "3", "fix: third")

	r := open(t, tr.Dir)
	commits, err := r.Log("1.0.0", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("range should exclude the tagged commit, got %d", len(commits))
	}
}

func TestMergedTagsExcludesUnmergedBranches(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("a.txt", "1", "feat: first")
	tr.Tag("1.0.0")
	tr.Git("checkout", "-q", "-b", "side")
	tr.CommitFile("s.txt", "s", "feat: side work")
	tr.Tag("9.9.9")
	tr.Git("checkout", "-q", "main")
	tr.CommitFile("b.txt", "2", "fix: main work")

	r := open(t, tr.Dir)
	merged, err := r.MergedTags("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(merged, "1.0.0") {
		t.Errorf("merged = %q, want 1.0.0", merged)
	}
	if slices.Contains(merged, "9.9.9") {
		t.Errorf("a tag on an unmerged branch is not a predecessor: %q", merged)
	}
	all, err := r.AllTags()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("AllTags = %q", all)
	}
}

func TestTagsPointingAt(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("a.txt", "1", "feat: first")
	tr.Tag("1.0.0")
	r := open(t, tr.Dir)

	tags, err := r.TagsPointingAt("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tags, []string{"1.0.0"}) {
		t.Errorf("tags = %q", tags)
	}

	tr.CommitFile("b.txt", "2", "fix: second")
	tags, _ = r.TagsPointingAt("HEAD")
	if len(tags) != 0 {
		t.Errorf("expected no tag on the new HEAD, got %q", tags)
	}
}

func TestTagSHADereferencesAnnotatedTag(t *testing.T) {
	tr := testrepo.New(t)
	want := tr.CommitFile("a.txt", "1", "feat: first")
	tr.Tag("1.0.0")
	r := open(t, tr.Dir)
	got, err := r.TagSHA("1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("TagSHA = %s, want the commit %s", got, want)
	}
}

func TestChangedPaths(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.0.0")
	tr.CommitFile("docs/guide.md", "d", "docs: write")
	tr.CommitFile("README.md", "r", "docs: readme")

	r := open(t, tr.Dir)
	paths, err := r.ChangedPaths("1.0.0", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{"README.md", "docs/guide.md"}) {
		t.Errorf("paths = %q", paths)
	}

	// With no predecessor every tracked file counts, because all of it is new.
	all, err := r.ChangedPaths("", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("initial release should see every file, got %q", all)
	}
}

func TestIsShallow(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("a.txt", "1", "feat: one")
	tr.CommitFile("b.txt", "2", "feat: two")
	tr.CommitFile("c.txt", "3", "feat: three")

	full := open(t, tr.Dir)
	if shallow, err := full.IsShallow(); err != nil || shallow {
		t.Errorf("full clone reported shallow=%v err=%v", shallow, err)
	}

	sc := tr.Clone(1)
	shallowRepo := open(t, sc.Dir)
	shallow, err := shallowRepo.IsShallow()
	if err != nil {
		t.Fatal(err)
	}
	if !shallow {
		t.Error("depth-1 clone should report shallow")
	}
}

func TestRemoteTagSHA(t *testing.T) {
	origin := testrepo.New(t)
	origin.CommitFile("a.txt", "1", "feat: first")
	origin.Tag("1.0.0")
	wantSHA := origin.Head()

	clone := origin.Clone(0)
	r := open(t, clone.Dir)

	sha, ok, err := r.RemoteTagSHA("origin", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("tag should exist on the remote")
	}
	if sha != wantSHA {
		t.Errorf("annotated tag must dereference to the commit: got %s want %s", sha, wantSHA)
	}

	_, ok, err = r.RemoteTagSHA("origin", "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("missing tag should report ok=false, not an error")
	}
}

func TestCommitAndStaging(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("a.txt", "1", "feat: first")
	r := open(t, tr.Dir)

	staged, err := r.HasStagedChanges()
	if err != nil {
		t.Fatal(err)
	}
	if staged {
		t.Error("clean tree should have nothing staged")
	}

	tr.Write("CHANGELOG.md", "# Changelog\n")
	// Add skips paths that do not exist rather than failing.
	if err := r.Add("CHANGELOG.md", "does-not-exist.md"); err != nil {
		t.Fatal(err)
	}
	staged, err = r.HasStagedChanges()
	if err != nil {
		t.Fatal(err)
	}
	if !staged {
		t.Fatal("expected staged changes")
	}
	if err := r.Commit("chore(release): 1.0.0", "release-bot <bot@example.invalid>", false); err != nil {
		t.Fatal(err)
	}
	commits, _ := r.Log("", "HEAD")
	if commits[0].Message != "chore(release): 1.0.0" {
		t.Errorf("message = %q", commits[0].Message)
	}
	if commits[0].AuthorEmail != "bot@example.invalid" {
		t.Errorf("author = %q", commits[0].AuthorEmail)
	}
}

func TestCreateAnnotatedTagOnSpecificCommit(t *testing.T) {
	tr := testrepo.New(t)
	target := tr.CommitFile("a.txt", "1", "feat: first")
	tr.CommitFile("b.txt", "2", "chore(release): 1.0.0")

	r := open(t, tr.Dir)
	// The tag must land on what was built, not on the release commit.
	if err := r.CreateAnnotatedTag("1.0.0", "1.0.0", target, false); err != nil {
		t.Fatal(err)
	}
	got, err := r.TagSHA("1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Errorf("tag landed on %s, want %s", got, target)
	}
	if !r.TagExistsLocally("1.0.0") {
		t.Error("TagExistsLocally should see it")
	}
	if r.TagExistsLocally("2.0.0") {
		t.Error("TagExistsLocally should not invent tags")
	}
}

func TestMaskURLCredentials(t *testing.T) {
	for in, want := range map[string]string{
		"https://gitlab-ci-token:supersecret@git.example.com/a/b.git": "https://***@git.example.com/a/b.git",
		"https://git.example.com/a/b.git":                             "https://git.example.com/a/b.git",
		"fatal: could not read https://user:pw@host/x":                "fatal: could not read https://***@host/x",
		"plain text": "plain text",
	} {
		if got := git.MaskURLCredentials(in); got != want {
			t.Errorf("MaskURLCredentials(%q) = %q, want %q", in, got, want)
		}
	}
}

// The masker used to return after the first match, so a second credentialed
// URL in the same line survived. golangci-lint's SA4004 found it.
func TestMaskURLCredentialsMasksEveryOccurrence(t *testing.T) {
	in := "fatal: https://a:secret1@host/x.git failed, retrying https://b:secret2@host/y.git"
	got := git.MaskURLCredentials(in)
	for _, leaked := range []string{"secret1", "secret2"} {
		if strings.Contains(got, leaked) {
			t.Errorf("%s survived masking: %q", leaked, got)
		}
	}
	if strings.Count(got, "***") != 2 {
		t.Errorf("expected two masked credentials, got %q", got)
	}
	if !strings.Contains(got, "host/x.git") || !strings.Contains(got, "host/y.git") {
		t.Errorf("the rest of the message must survive: %q", got)
	}
}

func TestMaskRegisteredSecretInErrors(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("a.txt", "1", "feat: first")
	r := open(t, tr.Dir)
	r.AddSecret("s3cr3t-token")

	// Provoke a failure whose message contains the secret.
	_, err := r.Run("rev-parse", "--verify", "refs/tags/s3cr3t-token")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "s3cr3t-token") {
		t.Errorf("secret leaked into error: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Errorf("expected masking placeholder, got %v", err)
	}
}

func TestCurrentBranch(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("a.txt", "1", "feat: first")
	r := open(t, tr.Dir)
	b, err := r.CurrentBranch()
	if err != nil || b != "main" {
		t.Errorf("branch = %q err = %v", b, err)
	}
	tr.Git("checkout", "-q", "--detach")
	if _, err := r.CurrentBranch(); err == nil {
		t.Error("detached HEAD should be an error, not an empty string")
	}
}
