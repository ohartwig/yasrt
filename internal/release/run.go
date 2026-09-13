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
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ohartwig/yasrt/internal/analyze"
	"github.com/ohartwig/yasrt/internal/config"
	"github.com/ohartwig/yasrt/internal/forge"
	"github.com/ohartwig/yasrt/internal/git"
	"github.com/ohartwig/yasrt/internal/hooks"
	"github.com/ohartwig/yasrt/internal/render"
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
	// Forge publishes the release. nil skips the publishing steps.
	Forge forge.Client
	// URLs builds the links in the notes; its shapes differ per platform.
	URLs forge.URLs

	Remote    string // default "origin"
	Branch    string // branch to push the release commit to; default: current
	Token     string // CI token, injected into the push URL
	GPGKeyB64 string // optional signing key

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

// forgeKind is the platform the push credentials must suit. GitLab is the
// fallback because that is where this tool started.
func (o *Options) forgeKind() forge.Kind {
	if o.Forge != nil {
		return o.Forge.Kind()
	}
	if o.URLs.Kind != "" {
		return o.URLs.Kind
	}
	return forge.GitLab
}

func (o *Options) now() time.Time {
	if o.Now.IsZero() {
		return time.Now().UTC()
	}
	return o.Now
}

// Run executes the release. The returned report is filled in as far as the run
// got, so a caller can write it even when an error is returned.
//
// When the run fails, the on_failure hooks are told what failed before the
// error is returned. They cannot rescue the release; they exist so that the
// failure reaches a person.
func Run(ctx context.Context, o Options) (*Report, error) {
	log := o.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	rep, err := run(ctx, o, log)
	if err == nil || o.Config == nil {
		return rep, err
	}
	hs := o.Config.Hooks.For(hooks.OnFailure)
	if len(hs) == 0 {
		return rep, err
	}
	hctx := hookContext(o.Result, rep.Notes, o.URLs.Base, o.DryRun)
	hctx.Error = err.Error()
	hctx.FailedStep = rep.failedStep()
	hres, hookErr := hooks.Run(ctx, o.Repo.Dir(), hooks.OnFailure, hs, hctx, o.Repo.Mask, log)
	rep.Hooks = append(rep.Hooks, hres...)
	if hookErr != nil {
		log.Warn("on_failure hook failed too", "err", hookErr)
	}
	return rep, err
}

// failedStep names the last step that failed, for the on_failure hooks.
func (r *Report) failedStep() string {
	for i := len(r.Steps) - 1; i >= 0; i-- {
		if r.Steps[i].Status == StepFailed {
			return r.Steps[i].Name
		}
	}
	return ""
}

func run(ctx context.Context, o Options, log *slog.Logger) (*Report, error) {
	res, cfg, repo := o.Result, o.Config, o.Repo
	rep := &Report{Version: res.Version.String(), Tag: res.Tag, TagSHA: res.Commit}

	if !res.Releasing() {
		return rep, fmt.Errorf("%w (status: %s)", ErrNotReleasing, res.Status)
	}
	if o.Token != "" {
		repo.AddSecret(o.Token)
	}
	if k := strings.TrimSpace(o.GPGKeyB64); k != "" {
		repo.AddSecret(k)
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
		Version:  res.Version,
		Previous: res.Previous,
		Tag:      res.Tag,
		Date:     o.now(),
		Decision: res.Decision,
		Sections: cfg.Changelog.Sections,
		URLs:     o.URLs,
		Title:    cfg.Changelog.Title,
	}
	notes := render.Notes(in)
	rep.Notes = notes
	rep.Steps = append(rep.Steps, Step{Name: "notes", Status: StepDone})
	log.Info("release notes rendered", "version", res.Version.String(), "bytes", len(notes))

	hctx := hookContext(res, notes, o.URLs.Base, o.DryRun)

	// before_tag runs while nothing has been written yet, so a hook that says
	// no leaves the repository exactly as it found it.
	hres, hookErr := hooks.Run(ctx, repo.Dir(), hooks.BeforeTag, cfg.Hooks.For(hooks.BeforeTag), hctx, repo.Mask, log)
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

	pushURL, err := pushURL(repo, o.remote(), o.Token, o.forgeKind())
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

	hres, hookErr = hooks.Run(ctx, repo.Dir(), hooks.AfterTag, cfg.Hooks.For(hooks.AfterTag), hctx, repo.Mask, log)
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
		st, detail, sha, err := commitChangelog(repo, cfg, res, o, in, sign, changelogStaged, notes, log)
		rep.Steps = append(rep.Steps, Step{Name: "release-commit", Status: st, Detail: detail})
		if err != nil {
			return rep, err
		}
		rep.ReleaseCommit = sha
	} else {
		rep.Steps = append(rep.Steps, Step{Name: "release-commit", Status: StepSkipped, Detail: "disabled by configuration"})
	}

	// Step 5: the GitLab release.
	if cfg.GitLabReleaseEnabled() && o.Forge != nil {
		url, st, detail, err := publishRelease(ctx, o, cfg, res, notes, log)
		rep.Steps = append(rep.Steps, Step{Name: "release", Status: st, Detail: detail})
		if err != nil {
			return rep, err
		}
		rep.ReleaseURL = url
	} else {
		rep.Steps = append(rep.Steps, Step{Name: "release", Status: StepSkipped, Detail: "disabled or no forge client"})
	}

	// Step 6: follow-up triggers. Never fatal.
	rep.Triggers = runTriggers(ctx, o, cfg, res, log)
	rep.Steps = append(rep.Steps, Step{Name: "triggers", Status: StepDone,
		Detail: fmt.Sprintf("%d configured", len(cfg.AfterRelease.Triggers))})

	// Step 7: after_release hooks. The release has already happened, so a
	// failure here is reported rather than fatal, unless a hook opts in.
	hres, hookErr = hooks.Run(ctx, repo.Dir(), hooks.AfterRelease, cfg.Hooks.For(hooks.AfterRelease), hctx, repo.Mask, log)
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
