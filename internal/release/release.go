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
	"slices"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"git.ole-hartwig.eu/yasrt/cli/internal/analyze"
	"git.ole-hartwig.eu/yasrt/cli/internal/config"
	"git.ole-hartwig.eu/yasrt/cli/internal/forge"
	"git.ole-hartwig.eu/yasrt/cli/internal/git"
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
	hres, hookErr := hooks.Run(ctx, o.Repo.Dir(), hooks.OnFailure, hs, hctx, log)
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

	pushURL, err := authenticatedURL(repo, o.remote(), o.Token, o.forgeKind())
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

// commitChangelog makes the release commit and pushes it. The push is the one
// step that races with the rest of the world: on a repository where Renovate
// merges several times an hour, the default branch has often moved on by the
// time this runs. semantic-release refuses in that case ("local branch is
// behind the remote one") and releases nothing; the shadow measured that on
// 13 of about 450 pushes in one day. Here the tag is already published, so
// refusing would leave a release without its changelog. Instead the commit is
// rebuilt on the branch as it is now -- it only ever touches the changelog
// and the configured assets -- and pushed again, a few times if need be.
func commitChangelog(repo *git.Repo, cfg *config.Config, res *analyze.Result, o Options,
	in render.Input, sign, changed bool, notes string, log *slog.Logger) (StepStatus, string, string, error) {

	branch := o.Branch
	if branch == "" {
		var err error
		branch, err = repo.CurrentBranch()
		if err != nil {
			return StepFailed, err.Error(), "", err
		}
	}
	pushURL, err := authenticatedURL(repo, o.remote(), o.Token, o.forgeKind())
	if err != nil {
		return StepFailed, err.Error(), "", err
	}
	msg := config.Vars{Version: res.Version, Tag: res.Tag, Notes: notes}.Expand(cfg.ReleaseCommit.Message)

	const attempts = 3
	for attempt := 1; ; attempt++ {
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
		if err := repo.Commit(msg, cfg.ReleaseCommit.Author, sign); err != nil {
			return StepFailed, err.Error(), "", err
		}
		sha, err := repo.HeadSHA()
		if err != nil {
			return StepFailed, err.Error(), "", err
		}

		pushErr := repo.Push(pushURL, "HEAD:refs/heads/"+branch)
		if pushErr == nil {
			log.Info("release commit pushed", "sha", short(sha), "branch", branch, "signed", sign, "attempt", attempt)
			subject, _, _ := strings.Cut(msg, "\n")
			return StepDone, subject, sha, nil
		}
		masked := repo.Mask(pushErr.Error())
		if !git.IsRejectedPush(pushErr) || attempt == attempts {
			return StepFailed, masked, sha, fmt.Errorf("pushing the release commit failed: %s", masked)
		}

		// The branch moved. Start again from where it is now -- but only if
		// the released commit is still part of it; a rewritten branch is not
		// something to quietly build a changelog on.
		remoteHead, err := repo.FetchRef(pushURL, branch)
		if err != nil {
			return StepFailed, repo.Mask(err.Error()), sha, err
		}
		contained, err := repo.IsAncestor(res.Commit, remoteHead)
		if err != nil {
			return StepFailed, err.Error(), sha, err
		}
		if !contained {
			err := fmt.Errorf("%s no longer contains the released commit %s (now at %s); not rebuilding the release commit on a rewritten branch",
				branch, short(res.Commit), short(remoteHead))
			return StepFailed, err.Error(), sha, err
		}
		log.Warn("branch moved on while releasing; rebuilding the release commit on its new head",
			"branch", branch, "was", short(res.Commit), "now", short(remoteHead), "attempt", attempt)
		if err := repo.ResetHard(remoteHead); err != nil {
			return StepFailed, err.Error(), sha, err
		}
		changed, err = writeChangelog(repo.Dir(), cfg.Changelog.File, in, notes)
		if err != nil {
			return StepFailed, err.Error(), sha, err
		}
	}
}

func publishRelease(ctx context.Context, o Options, cfg *config.Config, res *analyze.Result,
	notes string, log *slog.Logger) (string, StepStatus, string, error) {

	if existing, ok, err := o.Forge.GetRelease(ctx, res.Tag); err != nil {
		return "", StepFailed, err.Error(), err
	} else if ok {
		log.Info("release already exists, skipping", "tag", res.Tag, "forge", string(o.Forge.Kind()))
		return existing.URL, StepSkipped, "already exists", nil
	}

	vars := config.Vars{Version: res.Version, Tag: res.Tag}
	var links []forge.Link
	var uploads []forge.Upload
	for _, a := range cfg.GitLabRelease.Assets {
		if a.IsUpload() {
			ups, err := expandUploads(o.Repo.Dir(), a, vars)
			if err != nil {
				return "", StepFailed, err.Error(), err
			}
			uploads = append(uploads, ups...)
			continue
		}
		links = append(links, forge.Link{
			Name:     vars.Expand(a.Name),
			URL:      vars.Expand(a.URL),
			LinkType: a.LinkType,
		})
	}

	// GitLab stores uploads independently of the release, so they go first
	// and become links on it. A failed upload therefore aborts before the
	// release exists, and a re-run starts clean.
	if o.Forge.SupportsLinks() {
		for _, up := range uploads {
			l, err := o.Forge.UploadAsset(ctx, nil, up)
			if err != nil {
				return "", StepFailed, err.Error(), err
			}
			log.Info("asset uploaded", "name", up.Name, "url", l.URL)
			links = append(links, l)
		}
	}

	body := notes
	if !o.Forge.SupportsLinks() {
		// Nowhere to attach them, so they go into the body rather than being
		// lost. Stated in the log, not silently.
		if md := forge.LinksAsMarkdown(links); md != "" {
			body += md
			log.Info("this forge has no release-link endpoint; links appended to the body",
				"forge", string(o.Forge.Kind()), "links", len(links))
		}
	}

	rel, err := o.Forge.CreateRelease(ctx, res.Tag, vars.Expand(cfg.GitLabRelease.Name), body)
	if err != nil {
		return "", StepFailed, err.Error(), err
	}
	log.Info("release published", "tag", res.Tag, "url", rel.URL, "forge", string(o.Forge.Kind()))

	if o.Forge.SupportsLinks() && len(links) > 0 {
		if err := o.Forge.AddLinks(ctx, res.Tag, links); err != nil {
			// A missing link is not worth discarding a published release over.
			log.Warn("release links failed", "err", err)
		}
	}

	// GitHub and Forgejo attach files to the release object, so these can
	// only happen now. A failure here is fatal so the pipeline goes red, but
	// the release stays: a re-run finds it and skips, so attach the missing
	// file by hand rather than expecting a retry to do it.
	if !o.Forge.SupportsLinks() {
		for _, up := range uploads {
			l, err := o.Forge.UploadAsset(ctx, rel, up)
			if err != nil {
				return rel.URL, StepFailed, err.Error(), err
			}
			log.Info("asset attached", "name", up.Name, "url", l.URL)
		}
	}
	return rel.URL, StepDone, rel.URL, nil
}

// expandUploads resolves one path asset to the files it names. A glob that
// matches nothing is an error: an asset that was configured and is missing is
// a broken build, not an empty set.
func expandUploads(dir string, a config.ReleaseLink, vars config.Vars) ([]forge.Upload, error) {
	pattern := vars.Expand(a.Path)
	matches, err := doublestar.Glob(os.DirFS(dir), pattern, doublestar.WithFilesOnly())
	if err != nil {
		return nil, fmt.Errorf("gitlab_release.assets: path %q: %w", a.Path, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("gitlab_release.assets: path %q matches no file", a.Path)
	}
	slices.Sort(matches)
	out := make([]forge.Upload, 0, len(matches))
	for _, m := range matches {
		name := filepath.Base(m)
		if len(matches) == 1 && a.Name != "" {
			name = vars.Expand(a.Name)
		}
		out = append(out, forge.Upload{
			Name:     name,
			Path:     filepath.Join(dir, filepath.FromSlash(m)),
			Package:  vars.Expand(a.Package),
			Version:  vars.Version.String(),
			LinkType: a.LinkType,
		})
	}
	return out, nil
}

func runTriggers(ctx context.Context, o Options, cfg *config.Config, res *analyze.Result,
	log *slog.Logger) []TriggerResult {

	if len(cfg.AfterRelease.Triggers) == 0 {
		return nil
	}
	var out []TriggerResult
	triggerer, canTrigger := o.Forge.(pipelineTriggerer)
	for _, t := range cfg.AfterRelease.Triggers {
		r := TriggerResult{Project: t.Project, Ref: t.Ref}
		switch {
		case o.Forge == nil:
			r.Error = "no forge client"
		case !canTrigger:
			// Only GitLab has the endpoint yasrt posts to. Dispatching a
			// workflow elsewhere is a different call with different
			// permissions, and guessing at it would be worse than saying so.
			r.Error = fmt.Sprintf("%s has no pipeline-trigger endpoint", o.Forge.Kind())
			log.Warn("follow-up trigger not delivered", "forge", string(o.Forge.Kind()), "project", t.Project)
		default:
			vars := make(map[string]string, len(t.Variables))
			for k, v := range t.Variables {
				vars[k] = expandEnv(config.Vars{Version: res.Version, Tag: res.Tag}.Expand(v))
			}
			url, err := triggerer.TriggerPipeline(ctx, t.Project, t.Ref, vars)
			if err != nil {
				// Non-fatal by design: the release is already published.
				r.Error = err.Error()
				log.Warn("follow-up trigger failed", "project", t.Project, "err", err)
			} else {
				r.Pipeline = url
				log.Info("follow-up pipeline triggered", "project", t.Project, "pipeline", url)
			}
		}
		out = append(out, r)
	}
	return out
}

// pipelineTriggerer is implemented only where the platform has an endpoint for
// starting a pipeline in another project.
type pipelineTriggerer interface {
	TriggerPipeline(ctx context.Context, project, ref string, vars map[string]string) (string, error)
}

// expandEnv resolves ${CI_PROJECT_PATH} style references in trigger variables.
func expandEnv(s string) string {
	return os.Expand(s, func(k string) string { return os.Getenv(k) })
}

// configureIdentity writes the release author into the repository's own
// config, so that a fresh CI container -- which has no identity at all --
// can make the tag and the commit. The author is configuration, not
// environment: release_commit.author has a default, so there is always one.
func configureIdentity(repo *git.Repo, cfg *config.Config) error {
	name, email := parseAuthor(cfg.ReleaseCommit.Author)
	dn, de := parseAuthor(config.DefaultReleaseAuthor)
	if name == "" {
		name = dn
	}
	if email == "" {
		email = de
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
func authenticatedURL(repo *git.Repo, remote, token string, kind forge.Kind) (string, error) {
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
	url := "https://" + forge.PushCredentials(kind, token) + "@" + rest
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
// GitLab, GitHub and Forgejo all verify SSH signatures, and organisations
// that already trust SSH keys for human commits need no second key type.
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
