// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package testrepo builds throwaway git repositories for tests.
//
// Everything yasrt does is defined against real git behaviour, so the tests are
// defined against real git too: no fakes, no interface seams for their own
// sake. Each repository lives in the test's temp dir and disappears with it.
package testrepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type Repo struct {
	t   *testing.T
	Dir string
}

// New creates an initialised repository on main with a deterministic identity
// and signing switched off.
func New(t *testing.T) *Repo {
	t.Helper()
	r := &Repo{t: t, Dir: t.TempDir()}
	r.Git("init", "-b", "main")
	r.Git("config", "user.name", "Test Bot")
	r.Git("config", "user.email", "test@example.invalid")
	r.Git("config", "commit.gpgsign", "false")
	r.Git("config", "tag.gpgsign", "false")
	r.Git("config", "core.hooksPath", "/dev/null")
	return r
}

// Git runs a git command in the repository and fails the test on error.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	out, err := r.git(args...)
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// TryGit runs a git command and returns its error instead of failing.
func (r *Repo) TryGit(args ...string) (string, error) { return r.git(args...) }

func (r *Repo) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		// The identity has to be forced here, not merely written with
		// `git config`. Environment variables outrank repository config, so an
		// ambient GIT_AUTHOR_EMAIL — which a CI job or an agent session may
		// well have set — silently replaces the identity these repositories
		// think they configured. That produced an intermittent failure that
		// only ever appeared after this session had run git itself, and never
		// when it was looked for.
		"GIT_AUTHOR_NAME=Test Bot",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=Test Bot",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
		"LC_ALL=C",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}

// Write creates or overwrites a file, making parent directories as needed.
func (r *Repo) Write(path, content string) {
	r.t.Helper()
	full := filepath.Join(r.Dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// Read returns a file's contents.
func (r *Repo) Read(path string) string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.Dir, path))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

// Exists reports whether a path is present in the work tree.
func (r *Repo) Exists(path string) bool {
	_, err := os.Stat(filepath.Join(r.Dir, path))
	return err == nil
}

// CommitFile writes a file, stages it and commits it. It returns the new SHA.
func (r *Repo) CommitFile(path, content, message string) string {
	r.t.Helper()
	r.Write(path, content)
	r.Git("add", "--", path)
	r.Git("commit", "-m", message)
	return r.Head()
}

// CommitEmpty records a commit with no tree change, for message-only cases.
func (r *Repo) CommitEmpty(message string) string {
	r.t.Helper()
	r.Git("commit", "--allow-empty", "-m", message)
	return r.Head()
}

// CommitAs records a commit under a different author, for ignore.authors.
func (r *Repo) CommitAs(author, path, content, message string) string {
	r.t.Helper()
	r.Write(path, content)
	r.Git("add", "--", path)
	r.Git("commit", "--author", author, "-m", message)
	return r.Head()
}

// Tag creates an annotated tag on HEAD.
func (r *Repo) Tag(name string) {
	r.t.Helper()
	r.Git("tag", "-a", "-m", name, name)
}

// WithRemote creates a bare repository, wires it up as origin and pushes the
// current branch to it. Pushes in tests then behave like pushes in CI.
func (r *Repo) WithRemote() string {
	r.t.Helper()
	bare := filepath.Join(r.t.TempDir(), "origin.git")
	cmd := exec.Command("git", "init", "--bare", "-b", "main", bare)
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("init bare: %v\n%s", err, out)
	}
	r.Git("remote", "add", "origin", bare)
	r.Git("push", "-q", "-u", "origin", "main")
	return bare
}

// RemoteTags lists the tags present on origin.
func (r *Repo) RemoteTags() []string {
	r.t.Helper()
	out := r.Git("ls-remote", "--tags", "origin")
	var tags []string
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) == 2 && !strings.HasSuffix(f[1], "^{}") {
			tags = append(tags, strings.TrimPrefix(f[1], "refs/tags/"))
		}
	}
	return tags
}

// Head returns the current commit SHA.
func (r *Repo) Head() string { return r.Git("rev-parse", "HEAD") }

// Clone makes a clone of this repository, optionally shallow, and returns it.
func (r *Repo) Clone(depth int) *Repo { return r.cloneFrom(r.Dir, depth) }

// CloneRemote clones the bare origin this repository pushes to -- a second
// actor on the same remote, for the races a release has to survive.
func (r *Repo) CloneRemote() *Repo {
	r.t.Helper()
	return r.cloneFrom(r.Git("remote", "get-url", "origin"), 0)
}

func (r *Repo) cloneFrom(src string, depth int) *Repo {
	r.t.Helper()
	dst := r.t.TempDir()
	args := []string{"clone"}
	if depth > 0 {
		// git ignores --depth for local path clones; file:// forces the
		// transport that can actually truncate history.
		args = append(args, "--depth", itoa(depth))
		src = "file://" + src
	}
	args = append(args, src, dst)
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("clone: %v\n%s", err, out)
	}
	c := &Repo{t: r.t, Dir: dst}
	c.Git("config", "user.name", "Test Bot")
	c.Git("config", "user.email", "test@example.invalid")
	c.Git("config", "commit.gpgsign", "false")
	return c
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
