// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

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

// TagConflictPolicy says what `release` does when the tag it was asked to
// publish already exists on another commit: two merges in close succession
// both ran `next` before either had tagged, and both arrived at the same
// number.
type TagConflictPolicy string

const (
	// TagConflictFail stops with ErrTagMismatch (exit 5). The default, because
	// a build in the same pipeline may carry the number from `next`, and a tag
	// that disagrees with the artefact is worse than no tag.
	TagConflictFail TagConflictPolicy = "fail"
	// TagConflictRenumber fetches the remote's tags, analyses the same commit
	// again and releases the number that comes out -- or nothing, when the
	// commits left over do not bump. Safe where the artefact is built from the
	// tag (a tag pipeline), so no build has used the stale number.
	TagConflictRenumber TagConflictPolicy = "renumber"
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
	// Skipped says why nothing was released although the run succeeded: the
	// tag was taken by a sibling pipeline and, renumbered, nothing was left.
	Skipped string `json:"skipped,omitzero"`
	// LostTo is the commit that holds Tag when this run failed with
	// ErrTagMismatch: a sibling pipeline cut the same number first. The tag
	// is that pipeline's, not ours -- a job that withdraws a release on
	// failure must leave it alone (release-tools, 2026-10-16: the losing
	// pipeline's rollback deleted the winner's tag).
	LostTo string `json:"lost_to,omitzero"`
	// TagResumed is set when the tag was already on the remote at the
	// released commit: a previous run of this release pushed it and failed
	// later. The tag is this release's all the same, and a withdrawal must
	// treat it as cut by this run (AUD-030-F03).
	TagResumed bool `json:"tag_resumed,omitzero"`
}

// TagResumedDetail is the detail of the tag step when a resumed run found
// its tag already on the remote; the withdrawal script keys on it too.
const TagResumedDetail = "already on the remote at this commit"

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

	// OnTagConflict decides between failing and renumbering when the tag is
	// taken. Empty means TagConflictFail.
	OnTagConflict TagConflictPolicy

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

// hookContext describes the release to an external hook. It carries no
// credentials, and the hook's environment carries none either unless the
// hook names a variable in `env` (hooks.Environment).
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
	rep, err := run(ctx, &o, log)
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

// lostTo records that the tag belongs to another commit and returns the
// ErrTagMismatch to stop on. The report then says so twice: in LostTo and as
// a failed "tag" step, so that no reader mistakes the sibling's tag for ours.
func (r *Report) lostTo(tag, remoteSHA, want, suffix string) error {
	r.LostTo = remoteSHA
	r.Steps = append(r.Steps, Step{Name: "tag", Status: StepFailed,
		Detail: fmt.Sprintf("%s exists at %s, not at %s: cut by a sibling pipeline, left alone", tag, short(remoteSHA), short(want))})
	return fmt.Errorf("%w: %s points at %s, not at %s%s",
		ErrTagMismatch, tag, short(remoteSHA), short(want), suffix)
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

func run(ctx context.Context, o *Options, log *slog.Logger) (*Report, error) {
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
		if o.OnTagConflict != TagConflictRenumber {
			return rep, rep.lostTo(res.Tag, remoteSHA, res.Commit, "")
		}
		// A sibling pipeline released first. The number is gone; the commits
		// are not. Ask the analysis again with the remote's tags in view and
		// release what it says -- or nothing, when what is left does not bump.
		if err := repo.FetchTags(o.remote()); err != nil {
			return rep, err
		}
		again, err := analyze.Run(repo, cfg, analyze.Options{Ref: res.Commit, Branch: res.Branch}, log)
		if err != nil {
			return rep, err
		}
		taken := fmt.Sprintf("%s was taken by %s", res.Tag, short(remoteSHA))
		if !again.Releasing() {
			rep.Skipped = fmt.Sprintf("%s; at %s nothing is left to release (%s)", taken, short(res.Commit), again.Reason)
			rep.Steps = append(rep.Steps, Step{Name: "renumber", Status: StepSkipped, Detail: rep.Skipped})
			log.Info("tag taken, nothing left to release", "tag", res.Tag, "by", short(remoteSHA), "status", string(again.Status))
			return rep, nil
		}
		log.Info("tag taken, releasing the next number instead", "taken", res.Tag, "by", short(remoteSHA), "now", again.Tag)
		rep.Steps = append(rep.Steps, Step{Name: "renumber", Status: StepDone, Detail: taken + "; releasing " + again.Tag + " instead"})
		res, o.Result = again, again
		rep.Version, rep.Tag, rep.TagSHA = res.Version.String(), res.Tag, res.Commit
		remoteSHA, tagOnRemote, err = repo.RemoteTagSHA(o.remote(), res.Tag)
		if err != nil {
			return rep, err
		}
		if tagOnRemote && remoteSHA != res.Commit {
			return rep, rep.lostTo(res.Tag, remoteSHA, res.Commit, " (after renumbering)")
		}
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
	// fails while the repository is still untouched. The key itself is not
	// written yet: before_tag hooks run first, and a hook does not get to
	// read it (AUD-030-F02).
	if err := checkSigning(cfg, o.GPGKeyB64); err != nil {
		return rep, err
	}
	registerKeySecrets(repo, o.GPGKeyB64)
	cleanupKey := func() {}
	defer func() { cleanupKey() }()

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

	sign, cleanup, err := setupSigning(repo, cfg, o.GPGKeyB64, log)
	if err != nil {
		return rep, err
	}
	cleanupKey = cleanup
	rep.Signed = sign

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
		rep.Steps = append(rep.Steps, Step{Name: "tag", Status: StepSkipped, Detail: TagResumedDetail})
		rep.TagResumed = true
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

	// after_tag hooks run between the tag and the release commit, both
	// signed. The key is taken off disk for them and written again after
	// (AUD-030-F02).
	afterTag := cfg.Hooks.For(hooks.AfterTag)
	if len(afterTag) > 0 && sign {
		cleanupKey()
		cleanupKey = func() {}
	}
	hres, hookErr = hooks.Run(ctx, repo.Dir(), hooks.AfterTag, afterTag, hctx, repo.Mask, log)
	rep.Hooks = append(rep.Hooks, hres...)
	if hookErr != nil {
		rep.Steps = append(rep.Steps, Step{Name: "hook:after_tag", Status: StepFailed, Detail: hookErr.Error()})
		return rep, hookErr
	}
	if len(afterTag) > 0 && sign {
		resigned, cleanup, err := setupSigning(repo, cfg, o.GPGKeyB64, log)
		if err != nil {
			return rep, err
		}
		cleanupKey = cleanup
		if !resigned {
			return rep, fmt.Errorf("%w: the key could not be set up again after the after_tag hooks", ErrSigningRequired)
		}
	}
	if len(hres) > 0 {
		rep.Steps = append(rep.Steps, Step{Name: "hook:after_tag", Status: StepDone,
			Detail: fmt.Sprintf("%d hook(s)", len(hres))})
	}

	// Step 4: the release commit, after the tag on purpose.
	if cfg.ReleaseCommitEnabled() {
		st, detail, sha, err := commitChangelog(repo, cfg, res, *o, in, sign, changelogStaged, notes, log)
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
		url, st, detail, err := publishRelease(ctx, *o, cfg, res, notes, log)
		rep.Steps = append(rep.Steps, Step{Name: "release", Status: st, Detail: detail})
		if err != nil {
			return rep, err
		}
		rep.ReleaseURL = url
	} else {
		rep.Steps = append(rep.Steps, Step{Name: "release", Status: StepSkipped, Detail: "disabled or no forge client"})
	}

	// Step 6: follow-up triggers. Never fatal.
	rep.Triggers = runTriggers(ctx, *o, cfg, res, log)
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
