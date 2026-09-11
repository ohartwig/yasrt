// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package git drives the git binary through os/exec.
//
// This is deliberately not go-git. GPG and SSH signing, credential handling and
// shallow-clone semantics have to be exactly what git does in the job image,
// and the only way to guarantee that is to run git itself.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Field and record separators for log parsing. Both are control characters that
// cannot occur in a commit message.
const (
	fieldSep  = "\x1f"
	recordSep = "\x1e"
)

// RawCommit is what git log yields, before any Conventional Commits meaning is
// read into it.
type RawCommit struct {
	SHA         string
	ShortSHA    string
	AuthorName  string
	AuthorEmail string
	Message     string
}

// Repo is a working copy.
type Repo struct {
	dir string
	// secrets are masked out of every error and log line this package emits.
	secrets []string
}

// Open verifies that dir is inside a work tree and returns its root.
func Open(dir string) (*Repo, error) {
	r := &Repo{dir: dir}
	out, err := r.run("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not a git repository (%s): %w", dir, err)
	}
	r.dir = out
	return r, nil
}

// Dir is the repository root.
func (r *Repo) Dir() string { return r.dir }

// AddSecret registers a value that must never appear in output.
func (r *Repo) AddSecret(s string) {
	if s != "" {
		r.secrets = append(r.secrets, s)
	}
}

// Mask replaces registered secrets, and any credentials embedded in a URL, with
// a placeholder. Every error this package returns has already been through it.
func (r *Repo) Mask(s string) string {
	for _, sec := range r.secrets {
		s = strings.ReplaceAll(s, sec, "***")
	}
	return MaskURLCredentials(s)
}

// MaskURLCredentials rewrites https://user:secret@host as https://***@host.
func MaskURLCredentials(s string) string {
	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, "://")
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i+3])
		rest = rest[i+3:]

		at := strings.IndexByte(rest, '@')
		if at < 0 {
			b.WriteString(rest)
			return b.String()
		}
		// Only credentials when nothing space-like intervenes; otherwise this
		// is an ordinary URL followed by prose that happens to contain an @.
		if strings.ContainsAny(rest[:at], " \t\n/") {
			continue
		}
		b.WriteString("***")
		rest = rest[at:]
	}
}

func (r *Repo) run(args ...string) (string, error) {
	stdout, _, err := r.runFull(args...)
	return stdout, err
}

func (r *Repo) runFull(args ...string) (string, string, error) {
	cmd := exec.Command("git", args...)
	if r.dir != "" {
		cmd.Dir = r.dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", // never block a CI job on a credential prompt
		"GIT_ADVICE=0",
		"LC_ALL=C",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	so := strings.TrimRight(stdout.String(), "\n")
	se := strings.TrimRight(stderr.String(), "\n")
	if err != nil {
		return so, se, fmt.Errorf("git %s: %w: %s",
			r.Mask(strings.Join(args, " ")), err, r.Mask(se))
	}
	return so, se, nil
}

// Run exposes an arbitrary git invocation, for probes that do not deserve their
// own method.
func (r *Repo) Run(args ...string) (string, error) { return r.run(args...) }

// IsShallow reports whether the clone has a truncated history. yasrt refuses to
// analyse one: a missing tag or a missing commit silently changes the answer.
func (r *Repo) IsShallow() (bool, error) {
	out, err := r.run("rev-parse", "--is-shallow-repository")
	if err != nil {
		return false, err
	}
	return out == "true", nil
}

// HeadSHA returns the full object name of HEAD.
func (r *Repo) HeadSHA() (string, error) { return r.run("rev-parse", "HEAD") }

// ResolveSHA turns any revision into a full object name.
func (r *Repo) ResolveSHA(ref string) (string, error) { return r.run("rev-parse", ref+"^{commit}") }

// HasCommits reports whether the repository has any history at all.
func (r *Repo) HasCommits() bool {
	_, err := r.run("rev-parse", "--verify", "HEAD")
	return err == nil
}

// MergedTags lists tags reachable from ref: a tag on an unmerged branch is not
// a predecessor of this commit and must not be treated as one.
func (r *Repo) MergedTags(ref string) ([]string, error) {
	out, err := r.run("tag", "--merged", ref)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// AllTags lists every tag, used only to warn about foreign tag formats.
func (r *Repo) AllTags() ([]string, error) {
	out, err := r.run("tag", "--list")
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// TagsPointingAt lists tags on exactly this commit, which is how
// already-released is detected.
func (r *Repo) TagsPointingAt(ref string) ([]string, error) {
	out, err := r.run("tag", "--points-at", ref)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// TagSHA resolves the commit a tag points at, dereferencing annotated tags.
func (r *Repo) TagSHA(tag string) (string, error) { return r.run("rev-list", "-n", "1", tag) }

// Log returns commits in from..to order, newest first. An empty from means the
// whole history, which is the no-tag case.
func (r *Repo) Log(from, to string) ([]RawCommit, error) {
	spec := to
	if from != "" {
		spec = from + ".." + to
	}
	format := strings.Join([]string{"%H", "%h", "%an", "%ae", "%B"}, fieldSep) + recordSep
	out, err := r.run("log", "--format="+format, spec)
	if err != nil {
		return nil, err
	}
	var commits []RawCommit
	for rec := range strings.SplitSeq(out, recordSep) {
		rec = strings.TrimLeft(rec, "\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.Split(rec, fieldSep)
		if len(f) < 5 {
			continue
		}
		commits = append(commits, RawCommit{
			SHA:         f[0],
			ShortSHA:    f[1],
			AuthorName:  f[2],
			AuthorEmail: f[3],
			Message:     strings.TrimRight(f[4], "\n"),
		})
	}
	return commits, nil
}

// ChangedPaths lists files touched in from..to. With no from, every tracked
// file counts as changed, because everything is new.
func (r *Repo) ChangedPaths(from, to string) ([]string, error) {
	if from == "" {
		out, err := r.run("ls-tree", "-r", "--name-only", to)
		if err != nil {
			return nil, err
		}
		return splitLines(out), nil
	}
	out, err := r.run("diff", "--name-only", from+".."+to)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// RemoteTagSHA looks a tag up on the remote. It returns ok false when the
// remote has no such tag. This is the idempotency anchor of `yasrt release`.
func (r *Repo) RemoteTagSHA(remote, tag string) (sha string, ok bool, err error) {
	out, err := r.run("ls-remote", "--tags", remote, "refs/tags/"+tag, "refs/tags/"+tag+"^{}")
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(out) == "" {
		return "", false, nil
	}
	// Prefer the dereferenced line: for an annotated tag it names the commit.
	for _, ln := range splitLines(out) {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		if strings.HasSuffix(f[1], "^{}") {
			return f[0], true, nil
		}
		sha = f[0]
	}
	return sha, sha != "", nil
}

// IsAncestor reports whether a is an ancestor of b. Used to tell "someone else
// pushed" apart from "our own release commit moved HEAD".
func (r *Repo) IsAncestor(a, b string) (bool, error) {
	_, _, err := r.runFull("merge-base", "--is-ancestor", a, b)
	if err == nil {
		return true, nil
	}
	// git exits 1 for "not an ancestor" and something else for a real failure.
	if strings.Contains(err.Error(), "exit status 1") {
		return false, nil
	}
	return false, err
}

// CreateAnnotatedTag tags a specific commit. sign requests an OpenPGP
// signature; the caller decides whether that is allowed to fail.
func (r *Repo) CreateAnnotatedTag(name, message, sha string, sign bool) error {
	args := []string{"tag", "-a", "-m", message}
	if sign {
		args = append(args, "-s")
	}
	args = append(args, name, sha)
	_, err := r.run(args...)
	return err
}

// TagExistsLocally reports whether the tag is already present in the work tree.
func (r *Repo) TagExistsLocally(name string) bool {
	_, err := r.run("rev-parse", "--verify", "refs/tags/"+name)
	return err == nil
}

// DeleteLocalTag removes a tag locally. Used to recover from a half-made tag
// before retrying, never to remove a tag that was already pushed.
func (r *Repo) DeleteLocalTag(name string) error {
	_, err := r.run("tag", "-d", name)
	return err
}

// Push sends refspecs to a remote.
func (r *Repo) Push(remote string, refspecs ...string) error {
	args := append([]string{"push", remote}, refspecs...)
	_, err := r.run(args...)
	return err
}

// Config sets a repository-local git configuration value.
func (r *Repo) Config(key, value string) error {
	_, err := r.run("config", "--local", key, value)
	return err
}

// Add stages paths. Paths that do not exist are skipped rather than failing:
// an asset list may legitimately name a file this release did not touch.
func (r *Repo) Add(paths ...string) error {
	var present []string
	for _, p := range paths {
		if _, err := os.Stat(r.dir + "/" + p); err == nil {
			present = append(present, p)
		}
	}
	if len(present) == 0 {
		return nil
	}
	_, err := r.run(append([]string{"add", "--"}, present...)...)
	return err
}

// HasStagedChanges reports whether anything is staged for commit.
func (r *Repo) HasStagedChanges() (bool, error) {
	_, _, err := r.runFull("diff", "--cached", "--quiet")
	if err == nil {
		return false, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) || strings.Contains(err.Error(), "exit status 1") {
		return true, nil
	}
	return false, err
}

// Commit records the staged tree. author, when set, must be in
// "Name <email>" form.
func (r *Repo) Commit(message, author string, sign bool) error {
	args := []string{"commit", "-m", message}
	if author != "" {
		args = append(args, "--author", author)
	}
	if sign {
		args = append(args, "-S")
	}
	_, err := r.run(args...)
	return err
}

// RemoteURL returns the configured URL of a remote.
func (r *Repo) RemoteURL(remote string) (string, error) {
	return r.run("remote", "get-url", remote)
}

// SetRemoteURL rewrites a remote's URL, which is how the job token is injected
// for a push. The value is registered as a secret first.
func (r *Repo) SetRemoteURL(remote, url string) error {
	_, err := r.run("remote", "set-url", remote, url)
	return err
}

// CurrentBranch returns the checked-out branch, or an error in detached HEAD.
func (r *Repo) CurrentBranch() (string, error) {
	out, err := r.run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if out == "HEAD" {
		return "", errors.New("detached HEAD: no branch to push to")
	}
	return out, nil
}

func splitLines(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for ln := range strings.SplitSeq(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return out
}
