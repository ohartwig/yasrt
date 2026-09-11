// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package release implements `yasrt release`: everything that writes.
//
// The order of the steps is load-bearing. The tag is pushed before the release
// commit, so that a failure while committing the changelog leaves a complete
// release behind rather than half of one. The tag points at the commit that was
// built, never at the release commit, because the tag is a statement about
// provenance. Steps 3 to 6 are individually idempotent, so a re-run after a
// failure finds what already exists, skips it, and finishes the rest.
package release

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"git.ole-hartwig.eu/yasrt/cli/internal/analyze"
	"git.ole-hartwig.eu/yasrt/cli/internal/config"
	"git.ole-hartwig.eu/yasrt/cli/internal/git"
	"git.ole-hartwig.eu/yasrt/cli/internal/gitlab"
	"git.ole-hartwig.eu/yasrt/cli/internal/hooks"
	"git.ole-hartwig.eu/yasrt/cli/internal/render"
)

var (
	// ErrNotReleasing means the analysis did not ask for a release.
	ErrNotReleasing = errors.New("refusing to release: status is not `release`")
	// ErrCommitMoved means someone pushed between analysis and release. Tagging
	// now would tag something other than what was built.
	ErrCommitMoved = errors.New("HEAD moved since the analysis: refusing to tag something that was not built")
	// ErrTagMismatch means the tag already exists on a different commit.
	ErrTagMismatch = errors.New("tag already exists on a different commit")
	// ErrSigningRequired means sign: required with no usable key.
	ErrSigningRequired = errors.New("release_commit.sign is `required` but no usable signing key is available")
)

// StepStatus records what happened to one step of the release.
type StepStatus string

const (
	StepDone    StepStatus = "done"
	StepSkipped StepStatus = "skipped"
	StepFailed  StepStatus = "failed"
)

// Step is one entry in the report.
type Step struct {
	Name   string     `json:"name"`
	Status StepStatus `json:"status"`
	Detail string     `json:"detail,omitzero"`
}

// TriggerResult records a follow-up pipeline trigger. Failures here never fail
// the release: the release already happened.
type TriggerResult struct {
	Project  string `json:"project"`
	Ref      string `json:"ref"`
	Pipeline string `json:"pipeline,omitzero"`
	Error    string `json:"error,omitzero"`
}

// Report is the machine-readable outcome, written as release-report.json.
type Report struct {
	Version       string          `json:"version"`
	Tag           string          `json:"tag"`
	TagSHA        string          `json:"tag_sha"`
	ReleaseURL    string          `json:"release_url,omitzero"`
	ReleaseCommit string          `json:"release_commit,omitzero"`
	Signed        bool            `json:"signed"`
	Steps         []Step          `json:"steps"`
	Triggers      []TriggerResult `json:"triggers,omitzero"`
	Hooks         []hooks.Result  `json:"hooks,omitzero"`
	Notes         string          `json:"notes,omitzero"`
}

// Options configure a release run.
type Options struct {
	Repo   *git.Repo
	Config *config.Config
	Result *analyze.Result
	Client *gitlab.Client // nil skips the GitLab steps

	Remote     string // default "origin"
	Branch     string // branch to push the release commit to; default: current
	Token      string // CI_JOB_TOKEN, injected into the push URL
	ProjectURL string // used to render issue and MR links
	GPGKeyB64  string // optional signing key

	Now    time.Time
	DryRun bool
	Log    *slog.Logger
}

func (o *Options) remote() string {
	if o.Remote == "" {
		return "origin"
	}
	return o.Remote
}

// hookContext describes the release to an external hook. It deliberately
// carries no credentials: a hook that needs a token reads it from its own
// environment rather than being handed one.
func hookContext(res *analyze.Result, notes, projectURL string, dryRun bool) hooks.Context {
	return hooks.Context{
		Status:     string(res.Status),
		Version:    res.Version.String(),
		Tag:        res.Tag,
		Previous:   res.Previous,
		Bump:       res.Bump.String(),
		Reason:     res.Reason,
		Commit:     res.Commit,
		Notes:      notes,
		ProjectURL: projectURL,
		Breaking:   res.Decision.Breaking(),
		DryRun:     dryRun,
	}
}

func (o *Options) now() time.Time {
	if o.Now.IsZero() {
		return time.Now().UTC()
	}
	return o.Now
}

// Run executes the release. The returned report is filled in as far as the run
// got, so a caller can write it even when an error is returned.
func Run(ctx context.Context, o Options) (*Report, error) {
	log := o.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	res, cfg, repo := o.Result, o.Config, o.Repo
	rep := &Report{Version: res.Version.String(), Tag: res.Tag, TagSHA: res.Commit}

	if !res.Releasing() {
		return rep, fmt.Errorf("%w (status: %s)", ErrNotReleasing, res.Status)
	}
	if o.Token != "" {
		repo.AddSecret(o.Token)
	}

	// Idempotency anchor first: what the remote already knows about this tag
	// decides how strict the HEAD check below may be.
	remoteSHA, tagOnRemote, err := repo.RemoteTagSHA(o.remote(), res.Tag)
	if err != nil {
		return rep, err
	}
	if tagOnRemote && remoteSHA != res.Commit {
		return rep, fmt.Errorf("%w: %s points at %s, not at %s",
			ErrTagMismatch, res.Tag, short(remoteSHA), short(res.Commit))
	}

	// Precondition: what we are about to tag must still be HEAD, so that a push
	// between analysis and release cannot get something tagged that was never
	// built. The exception is a re-run: once the tag is published at exactly
	// this commit, our own release commit has legitimately moved HEAD forward,
	// and nothing new can be tagged anyway.
	head, err := repo.HeadSHA()
	if err != nil {
		return rep, err
	}
	if head != res.Commit {
		resumable := false
		if tagOnRemote {
			resumable, err = repo.IsAncestor(res.Commit, head)
			if err != nil {
				return rep, err
			}
		}
		if !resumable {
			return rep, fmt.Errorf("%w: analysed %s, HEAD is now %s",
				ErrCommitMoved, short(res.Commit), short(head))
		}
		log.Info("resuming a release whose tag is already published",
			"tag", res.Tag, "commit", short(res.Commit), "head", short(head))
	}

	// Signing must be decided before anything is pushed, so that `required`
	// fails while the repository is still untouched.
	sign, cleanupKey, err := setupSigning(repo, cfg, o.GPGKeyB64, log)
	if err != nil {
		return rep, err
	}
	defer cleanupKey()
	rep.Signed = sign

	// An annotated tag records a tagger, so the identity has to exist before
	// step 3, not merely before the release commit. A fresh CI container has
	// none, and git refuses to guess one from root@<container-id>.
	if err := configureIdentity(repo, cfg); err != nil {
		return rep, err
	}

	// Step 1: notes.
	in := render.Input{
		Version:    res.Version,
		Previous:   res.Previous,
		Tag:        res.Tag,
		Date:       o.now(),
		Decision:   res.Decision,
		Sections:   cfg.Changelog.Sections,
		ProjectURL: o.ProjectURL,
	}
	notes := render.Notes(in)
	rep.Notes = notes
	rep.Steps = append(rep.Steps, Step{Name: "notes", Status: StepDone})
	log.Info("release notes rendered", "version", res.Version.String(), "bytes", len(notes))

	hctx := hookContext(res, notes, o.ProjectURL, o.DryRun)

	// before_tag runs while nothing has been written yet, so a hook that says
	// no leaves the repository exactly as it found it.
	hres, hookErr := hooks.Run(ctx, repo.Dir(), hooks.BeforeTag, cfg.Hooks.For(hooks.BeforeTag), hctx, log)
	rep.Hooks = append(rep.Hooks, hres...)
	if hookErr != nil {
		rep.Steps = append(rep.Steps, Step{Name: "hook:before_tag", Status: StepFailed, Detail: hookErr.Error()})
		return rep, hookErr
	}
	if len(hres) > 0 {
		rep.Steps = append(rep.Steps, Step{Name: "hook:before_tag", Status: StepDone,
			Detail: fmt.Sprintf("%d hook(s)", len(hres))})
	}

	if o.DryRun {
		log.Info("dry run: stopping before the first write")
		rep.Steps = append(rep.Steps, Step{Name: "dry-run", Status: StepSkipped, Detail: "no writes performed"})
		return rep, nil
	}

	// Step 2: changelog. Only meaningful when it will be committed; without the
	// release commit the notes live in the GitLab release alone.
	changelogStaged := false
	if cfg.ReleaseCommitEnabled() {
		changed, err := writeChangelog(repo.Dir(), cfg.Changelog.File, in, notes)
		if err != nil {
			rep.Steps = append(rep.Steps, Step{Name: "changelog", Status: StepFailed, Detail: err.Error()})
			return rep, err
		}
		changelogStaged = changed
		st := StepDone
		detail := ""
		if !changed {
			st, detail = StepSkipped, "entry for this version already present"
		}
		rep.Steps = append(rep.Steps, Step{Name: "changelog", Status: st, Detail: detail})
	} else {
		rep.Steps = append(rep.Steps, Step{Name: "changelog", Status: StepSkipped, Detail: "release_commit disabled"})
	}

	pushURL, err := authenticatedURL(repo, o.remote(), o.Token)
	if err != nil {
		return rep, err
	}

	// Step 3: the tag, on the commit that was built.
	if tagOnRemote {
		rep.Steps = append(rep.Steps, Step{Name: "tag", Status: StepSkipped, Detail: "already on the remote at this commit"})
		log.Info("tag already published, continuing", "tag", res.Tag)
	} else {
		if repo.TagExistsLocally(res.Tag) {
			// A previous attempt got as far as creating it but not pushing it.
			if err := repo.DeleteLocalTag(res.Tag); err != nil {
				return rep, err
			}
		}
		msg := res.Tag
		if err := repo.CreateAnnotatedTag(res.Tag, msg, res.Commit, sign); err != nil {
			rep.Steps = append(rep.Steps, Step{Name: "tag", Status: StepFailed, Detail: err.Error()})
			return rep, err
		}
		if err := repo.Push(pushURL, "refs/tags/"+res.Tag); err != nil {
			rep.Steps = append(rep.Steps, Step{Name: "tag", Status: StepFailed, Detail: repo.Mask(err.Error())})
			return rep, fmt.Errorf("pushing the tag failed (see `yasrt check` for the push permission): %w", err)
		}
		rep.Steps = append(rep.Steps, Step{Name: "tag", Status: StepDone, Detail: res.Tag + " -> " + short(res.Commit)})
		log.Info("tag pushed", "tag", res.Tag, "commit", short(res.Commit))
	}

	hres, hookErr = hooks.Run(ctx, repo.Dir(), hooks.AfterTag, cfg.Hooks.For(hooks.AfterTag), hctx, log)
	rep.Hooks = append(rep.Hooks, hres...)
	if hookErr != nil {
		rep.Steps = append(rep.Steps, Step{Name: "hook:after_tag", Status: StepFailed, Detail: hookErr.Error()})
		return rep, hookErr
	}
	if len(hres) > 0 {
		rep.Steps = append(rep.Steps, Step{Name: "hook:after_tag", Status: StepDone,
			Detail: fmt.Sprintf("%d hook(s)", len(hres))})
	}

	// Step 4: the release commit, after the tag on purpose.
	if cfg.ReleaseCommitEnabled() {
		st, detail, sha, err := commitChangelog(repo, cfg, res, o, sign, changelogStaged, notes, log)
		rep.Steps = append(rep.Steps, Step{Name: "release-commit", Status: st, Detail: detail})
		if err != nil {
			return rep, err
		}
		rep.ReleaseCommit = sha
	} else {
		rep.Steps = append(rep.Steps, Step{Name: "release-commit", Status: StepSkipped, Detail: "disabled by configuration"})
	}

	// Step 5: the GitLab release.
	if cfg.GitLabReleaseEnabled() && o.Client != nil {
		url, st, detail, err := publishRelease(ctx, o, cfg, res, notes, log)
		rep.Steps = append(rep.Steps, Step{Name: "gitlab-release", Status: st, Detail: detail})
		if err != nil {
			return rep, err
		}
		rep.ReleaseURL = url
	} else {
		rep.Steps = append(rep.Steps, Step{Name: "gitlab-release", Status: StepSkipped, Detail: "disabled or no API client"})
	}

	// Step 6: follow-up triggers. Never fatal.
	rep.Triggers = runTriggers(ctx, o, cfg, res, log)
	rep.Steps = append(rep.Steps, Step{Name: "triggers", Status: StepDone,
		Detail: fmt.Sprintf("%d configured", len(cfg.AfterRelease.Triggers))})

	// Step 7: after_release hooks. The release has already happened, so a
	// failure here is reported rather than fatal, unless a hook opts in.
	hres, hookErr = hooks.Run(ctx, repo.Dir(), hooks.AfterRelease, cfg.Hooks.For(hooks.AfterRelease), hctx, log)
	rep.Hooks = append(rep.Hooks, hres...)
	if len(hres) > 0 {
		st := StepDone
		if hookErr != nil {
			st = StepFailed
		}
		rep.Steps = append(rep.Steps, Step{Name: "hook:after_release", Status: st,
			Detail: fmt.Sprintf("%d hook(s)", len(hres))})
	}
	if hookErr != nil {
		return rep, hookErr
	}

	return rep, nil
}

// writeChangelog prepends the release block, reporting false when an entry for
// this version is already present, which is how a re-run stays idempotent.
func writeChangelog(dir, file string, in render.Input, notes string) (bool, error) {
	path := filepath.Join(dir, file)
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	// An entry for this version already present means a previous run got here.
	if strings.Contains(string(existing), "## ["+in.Version.String()+"]") {
		return false, nil
	}
	out := render.PrependChangelog(string(existing), in, notes)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(out), 0o644)
}

func commitChangelog(repo *git.Repo, cfg *config.Config, res *analyze.Result, o Options,
	sign, changed bool, notes string, log *slog.Logger) (StepStatus, string, string, error) {

	if err := repo.Add(cfg.ReleaseCommit.Assets...); err != nil {
		return StepFailed, err.Error(), "", err
	}
	staged, err := repo.HasStagedChanges()
	if err != nil {
		return StepFailed, err.Error(), "", err
	}
	if !staged {
		log.Info("nothing to commit for the release", "changed", changed)
		return StepSkipped, "no changes to commit", "", nil
	}

	msg := config.Vars{Version: res.Version, Tag: res.Tag, Notes: notes}.Expand(cfg.ReleaseCommit.Message)
	if err := repo.Commit(msg, cfg.ReleaseCommit.Author, sign); err != nil {
		return StepFailed, err.Error(), "", err
	}
	sha, err := repo.HeadSHA()
	if err != nil {
		return StepFailed, err.Error(), "", err
	}

	branch := o.Branch
	if branch == "" {
		branch, err = repo.CurrentBranch()
		if err != nil {
			return StepFailed, err.Error(), sha, err
		}
	}
	pushURL, err := authenticatedURL(repo, o.remote(), o.Token)
	if err != nil {
		return StepFailed, err.Error(), sha, err
	}
	if err := repo.Push(pushURL, "HEAD:refs/heads/"+branch); err != nil {
		masked := repo.Mask(err.Error())
		return StepFailed, masked, sha, fmt.Errorf("pushing the release commit failed: %s", masked)
	}
	log.Info("release commit pushed", "sha", short(sha), "branch", branch, "signed", sign)
	subject, _, _ := strings.Cut(msg, "\n")
	return StepDone, subject, sha, nil
}

func publishRelease(ctx context.Context, o Options, cfg *config.Config, res *analyze.Result,
	notes string, log *slog.Logger) (string, StepStatus, string, error) {

	if existing, ok, err := o.Client.GetRelease(ctx, res.Tag); err != nil {
		return "", StepFailed, err.Error(), err
	} else if ok {
		log.Info("gitlab release already exists, skipping", "tag", res.Tag)
		return existing.URL(), StepSkipped, "already exists", nil
	}

	rel, err := o.Client.CreateRelease(ctx, gitlab.CreateReleaseRequest{
		TagName:     res.Tag,
		Name:        config.Expand(cfg.GitLabRelease.Name, res.Version, res.Tag),
		Description: notes,
	})
	if err != nil {
		return "", StepFailed, err.Error(), err
	}
	log.Info("gitlab release created", "tag", res.Tag, "url", rel.URL())

	for _, a := range cfg.GitLabRelease.Assets {
		l := gitlab.Link{
			Name:     config.Expand(a.Name, res.Version, res.Tag),
			URL:      config.Expand(a.URL, res.Version, res.Tag),
			LinkType: a.LinkType,
		}
		if err := o.Client.AddReleaseLink(ctx, res.Tag, l); err != nil {
			// A missing link is not worth discarding a published release over.
			log.Warn("release link failed", "name", l.Name, "err", err)
		}
	}
	return rel.URL(), StepDone, rel.URL(), nil
}

func runTriggers(ctx context.Context, o Options, cfg *config.Config, res *analyze.Result,
	log *slog.Logger) []TriggerResult {

	var out []TriggerResult
	for _, t := range cfg.AfterRelease.Triggers {
		r := TriggerResult{Project: t.Project, Ref: t.Ref}
		if o.Client == nil {
			r.Error = "no API client"
			out = append(out, r)
			continue
		}
		vars := make(map[string]string, len(t.Variables)+2)
		for k, v := range t.Variables {
			vars[k] = expandEnv(config.Expand(v, res.Version, res.Tag))
		}
		p, err := o.Client.TriggerPipeline(ctx, t.Project, t.Ref, vars)
		if err != nil {
			// Non-fatal by design: the release is already published.
			r.Error = err.Error()
			log.Warn("follow-up trigger failed", "project", t.Project, "err", err)
		} else {
			r.Pipeline = p.WebURL
			log.Info("follow-up pipeline triggered", "project", t.Project, "pipeline", p.WebURL)
		}
		out = append(out, r)
	}
	return out
}

// expandEnv resolves ${CI_PROJECT_PATH} style references in trigger variables.
func expandEnv(s string) string {
	return os.Expand(s, func(k string) string { return os.Getenv(k) })
}

func configureIdentity(repo *git.Repo, cfg *config.Config) error {
	name, email := parseAuthor(cfg.ReleaseCommit.Author)
	if name == "" {
		name = firstNonEmpty(os.Getenv("GITLAB_USER_NAME"), "yasrt")
	}
	if email == "" {
		email = firstNonEmpty(os.Getenv("GITLAB_USER_EMAIL"), "yasrt@localhost")
	}
	if err := repo.Config("user.name", name); err != nil {
		return err
	}
	return repo.Config("user.email", email)
}

func parseAuthor(a string) (name, email string) {
	a = strings.TrimSpace(a)
	if a == "" {
		return "", ""
	}
	if i := strings.Index(a, "<"); i >= 0 {
		name = strings.TrimSpace(a[:i])
		email = strings.TrimSuffix(strings.TrimSpace(a[i+1:]), ">")
		return name, email
	}
	return a, ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// authenticatedURL returns a push URL carrying the job token. The token is
// registered as a secret first, so it cannot appear in an error message.
func authenticatedURL(repo *git.Repo, remote, token string) (string, error) {
	raw, err := repo.RemoteURL(remote)
	if err != nil {
		return "", err
	}
	if token == "" || !strings.HasPrefix(raw, "https://") {
		return raw, nil
	}
	rest := strings.TrimPrefix(raw, "https://")
	if i := strings.IndexByte(rest, '@'); i >= 0 {
		rest = rest[i+1:] // replace any credentials already present
	}
	url := "https://gitlab-ci-token:" + token + "@" + rest
	repo.AddSecret(url)
	return url, nil
}

// Signing key formats. Which one a key is gets detected from the material
// itself rather than configured: an OpenSSH private key and an armoured PGP
// block are unmistakable, and one fewer knob is one fewer thing to get wrong.
const (
	sshKeyHeader = "-----BEGIN OPENSSH PRIVATE KEY-----"
	// The armour header yasrt matches in order to RECOGNISE a PGP key, not a
	// key: forty characters of delimiter and no key material. Suppressed rather
	// than obfuscated, because a constant spelled out is what makes the
	// detection readable.
	//
	// The suppression is bare rather than rule-scoped, and trailing rather than
	// above: semgrep honours it only on the finding's own line or the one
	// immediately preceding, and the rule-scoped form did not take here even
	// with the id copied from the SARIF output. On a line that is one constant
	// string, suppressing every rule is a blast radius of one line.
	pgpKeyHeader = "-----BEGIN PGP PRIVATE KEY BLOCK-----" // nosemgrep
)

// setupSigning prepares the optional signing key and tells git to use it.
// `auto` degrades to unsigned; `required` fails here, before anything is
// written. The returned cleanup removes any key material written to disk.
func setupSigning(repo *git.Repo, cfg *config.Config, keyB64 string, log *slog.Logger) (bool, func(), error) {
	noop := func() {}

	// Deciding not to sign has to be stated, not merely left unsaid: a global
	// or system git config with commit.gpgsign or tag.gpgsign true would
	// otherwise try to sign with a key this job does not have, and a hardware
	// key would sit waiting for a touch that never comes.
	unsigned := func() (bool, func(), error) {
		for _, k := range []string{"commit.gpgsign", "tag.gpgsign"} {
			if err := repo.Config(k, "false"); err != nil {
				return false, noop, err
			}
		}
		return false, noop, nil
	}

	switch cfg.ReleaseCommit.Sign {
	case config.SignOff:
		return unsigned()
	case config.SignAuto, config.SignRequired:
	}

	required := cfg.ReleaseCommit.Sign == config.SignRequired

	if strings.TrimSpace(keyB64) == "" {
		if required {
			return false, noop, fmt.Errorf("%w: no key was provided", ErrSigningRequired)
		}
		log.Info("no signing key present, continuing unsigned")
		return unsigned()
	}

	key, err := decodeKey(keyB64)
	if err != nil {
		if required {
			return false, noop, fmt.Errorf("%w: %w", ErrSigningRequired, err)
		}
		log.Warn("signing key unusable, continuing unsigned", "err", err)
		return unsigned()
	}

	var cleanup func()
	switch {
	case strings.Contains(key, sshKeyHeader):
		cleanup, err = configureSSHSigning(repo, key)
	case strings.Contains(key, pgpKeyHeader):
		cleanup, err = configureGPGSigning(repo, key)
	default:
		err = errors.New("key is neither an OpenSSH private key nor an armoured PGP block")
	}
	if err != nil {
		if required {
			return false, noop, fmt.Errorf("%w: %w", ErrSigningRequired, err)
		}
		log.Warn("signing key unusable, continuing unsigned", "err", err)
		if cleanup != nil {
			cleanup()
		}
		return unsigned()
	}
	log.Info("signing enabled", "format", formatOf(key))
	return true, cleanup, nil
}

func formatOf(key string) string {
	if strings.Contains(key, sshKeyHeader) {
		return "ssh"
	}
	return "openpgp"
}

// decodeKey accepts the key base64-encoded, which is how it survives GitLab's
// variable masking — armour is multi-line and gets mangled otherwise — but also
// accepts raw armour, so a locally exported key works without ceremony.
func decodeKey(v string) (string, error) {
	v = strings.TrimSpace(v)
	if strings.Contains(v, "-----BEGIN") {
		return v, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(v), ""))
	if err != nil {
		return "", fmt.Errorf("decoding the signing key: %w", err)
	}
	return string(raw), nil
}

// configureSSHSigning writes the key to a private file and points git at it.
// GitLab verifies SSH signatures, and the estate already trusts SSH keys for
// human commits through .gitsigners, so this is the format with a future.
func configureSSHSigning(repo *git.Repo, key string) (func(), error) {
	dir, err := os.MkdirTemp("", "yasrt-signing-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	path := filepath.Join(dir, "signing_key")
	// ssh-keygen refuses a key file that others can read, and so does git.
	if err := os.WriteFile(path, []byte(ensureTrailingNewline(key)), 0o600); err != nil {
		cleanup()
		return nil, err
	}
	for _, kv := range [][2]string{
		{"gpg.format", "ssh"},
		{"user.signingkey", path},
	} {
		if err := repo.Config(kv[0], kv[1]); err != nil {
			cleanup()
			return nil, err
		}
	}
	return cleanup, nil
}

// configureGPGSigning imports an armoured key into the ambient keyring and
// selects it. The keyring is the job's own and disappears with the container.
func configureGPGSigning(repo *git.Repo, key string) (func(), error) {
	imp := exec.Command("gpg", "--batch", "--import")
	imp.Stdin = strings.NewReader(key)
	if out, err := imp.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("importing the signing key: %w: %s", err, out)
	}
	keyID, err := firstSecretKeyID()
	if err != nil {
		return nil, err
	}
	for _, kv := range [][2]string{
		{"gpg.format", "openpgp"},
		{"user.signingkey", keyID},
	} {
		if err := repo.Config(kv[0], kv[1]); err != nil {
			return nil, err
		}
	}
	return func() {}, nil
}

func firstSecretKeyID() (string, error) {
	out, err := exec.Command("gpg", "--list-secret-keys", "--with-colons").Output()
	if err != nil {
		return "", fmt.Errorf("listing secret keys: %w", err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if f := strings.Split(line, ":"); len(f) > 4 && f[0] == "sec" {
			return f[4], nil
		}
	}
	return "", errors.New("no secret key found after import")
}

func ensureTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

// WriteReport saves the report as a job artefact.
func WriteReport(path string, rep *Report) error {
	b, err := json.Marshal(rep, json.Deterministic(true))
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
