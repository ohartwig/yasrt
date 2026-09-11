// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package analyze implements `yasrt next`: it reads a repository and decides
// whether, and to what, it should be released. It writes nothing.
package analyze

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"

	"git.ole-hartwig.eu/yasrt/cli/internal/config"
	"git.ole-hartwig.eu/yasrt/cli/internal/conventional"
	"git.ole-hartwig.eu/yasrt/cli/internal/deliver"
	"git.ole-hartwig.eu/yasrt/cli/internal/git"
	"git.ole-hartwig.eu/yasrt/cli/internal/rules"
	"git.ole-hartwig.eu/yasrt/cli/internal/semver"
)

// Status is the outcome of an analysis. Every run produces exactly one.
type Status string

const (
	StatusRelease         Status = "release"
	StatusNoBump          Status = "no-bump"
	StatusNotDeliverable  Status = "not-deliverable"
	StatusAlreadyReleased Status = "already-released"
)

// ReasonForced marks a result that came from --force or --version rather than
// from the commits.
const ReasonForced = "forced"

var (
	// ErrShallow is fatal: a truncated history silently changes every answer.
	ErrShallow = errors.New(
		"shallow clone: yasrt needs the full history and all tags (set GIT_DEPTH: 0 and GIT_STRATEGY: clone)")
	// ErrVersionNotHigher is a usage error, not an analysis failure.
	ErrVersionNotHigher = errors.New("--version must be higher than the last release")
	// ErrNoCommits means the repository has no history to analyse.
	ErrNoCommits = errors.New("repository has no commits")
)

// Options carries the command-line overrides.
type Options struct {
	Ref                  string // defaults to HEAD
	Force                string // major | minor | patch
	Version              string // explicit x.y.z
	IgnoreDeliverability bool
	// IgnoreExistingTag skips the already-released short circuit. `release`
	// needs it: when it resumes after a partial failure the tag is already
	// published, but the notes still have to be rendered from the same range.
	IgnoreExistingTag bool
}

// Result is everything `next` learned. It is also the input to `release`.
type Result struct {
	Status   Status
	Version  semver.Version
	Tag      string
	Previous string // the previous tag as it is written in the repository
	Bump     semver.Bump
	Reason   string
	Commit   string

	PreviousVersion semver.Version
	HasPrevious     bool

	Decision rules.Decision
	Delivery deliver.Result
	Warnings []string
}

// Releasing reports whether this result asks for a release.
func (r *Result) Releasing() bool { return r.Status == StatusRelease }

// looseSemver spots tags that look like versions but do not match tag_format,
// which is almost always a misconfiguration worth naming out loud.
var looseSemver = regexp.MustCompile(`^v?\d+\.\d+\.\d+`)

// Run performs the analysis described in SPEC §6.1.
func Run(repo *git.Repo, cfg *config.Config, opts Options, log *slog.Logger) (*Result, error) {
	if shallow, err := repo.IsShallow(); err != nil {
		return nil, err
	} else if shallow {
		return nil, ErrShallow
	}
	if !repo.HasCommits() {
		return nil, ErrNoCommits
	}

	ref := opts.Ref
	if ref == "" {
		ref = "HEAD"
	}
	head, err := repo.ResolveSHA(ref)
	if err != nil {
		return nil, err
	}
	res := &Result{Commit: head}

	// Idempotency anchor: HEAD already carries a tag in our format.
	pointing, err := repo.TagsPointingAt(head)
	if err != nil {
		return nil, err
	}
	for _, tag := range pointing {
		if opts.IgnoreExistingTag {
			break
		}
		if v, ok := cfg.VersionFromTag(tag); ok {
			res.Status = StatusAlreadyReleased
			res.Version, res.Tag = v, tag
			res.Previous, res.PreviousVersion, res.HasPrevious = tag, v, true
			log.Debug("head already tagged", "tag", tag)
			return res, nil
		}
	}

	// When resuming, the tag this run is publishing sits on HEAD already. It is
	// this release, not the one before it, so it must not become the baseline.
	var excluded []string
	if opts.IgnoreExistingTag {
		excluded = pointing
	}
	lastTag, lastVersion, hasPrevious, warnings, err := lastRelease(repo, cfg, ref, excluded)
	if err != nil {
		return nil, err
	}
	res.Previous, res.PreviousVersion, res.HasPrevious = lastTag, lastVersion, hasPrevious
	res.Warnings = warnings

	raw, err := repo.Log(lastTag, ref)
	if err != nil {
		return nil, err
	}
	parsed := make([]conventional.Commit, 0, len(raw))
	for _, rc := range raw {
		parsed = append(parsed, conventional.Parse(rc.SHA, rc.ShortSHA, rc.AuthorName, rc.AuthorEmail, rc.Message))
	}
	res.Decision = rules.Evaluate(parsed, cfg)
	logDecision(log, res.Decision)

	res.Bump = res.Decision.Bump
	res.Reason = res.Decision.Reason

	// Overrides. Both count as forced, and neither skips deliverability.
	explicit := false
	if opts.Version != "" {
		v, err := semver.Parse(opts.Version)
		if err != nil {
			return nil, fmt.Errorf("--version: %w", err)
		}
		if hasPrevious && !lastVersion.Less(v) {
			return nil, fmt.Errorf("%w: %s is not higher than %s", ErrVersionNotHigher, v, lastVersion)
		}
		res.Version, explicit = v, true
		res.Reason = ReasonForced
		res.Bump = inferBump(lastVersion, v, hasPrevious)
	} else if opts.Force != "" {
		b, err := semver.ParseBump(opts.Force)
		if err != nil {
			return nil, fmt.Errorf("--force: %w", err)
		}
		if b == semver.None {
			return nil, errors.New("--force needs major, minor or patch")
		}
		res.Bump, res.Reason = b, ReasonForced
	}

	if res.Bump == semver.None && !explicit {
		res.Status = StatusNoBump
		return res, nil
	}

	if !explicit {
		if hasPrevious {
			res.Version = lastVersion.Next(res.Bump, cfg.MajorOnZero())
		} else {
			// First release: the configured starting point, not a bump of it.
			v, err := semver.Parse(cfg.Versioning.Initial)
			if err != nil {
				return nil, err
			}
			res.Version = v
		}
	}

	changed, err := repo.ChangedPaths(lastTag, ref)
	if err != nil {
		return nil, err
	}
	patterns := cfg.NonReleasePaths()
	res.Delivery = deliver.Evaluate(changed, patterns)
	log.Debug("deliverability",
		"changed", len(changed), "delivering", len(res.Delivery.Delivering),
		"excluded", len(res.Delivery.Excluded))

	if !res.Delivery.Deliverable && !opts.IgnoreDeliverability {
		res.Status = StatusNotDeliverable
		res.Version = semver.Version{}
		return res, nil
	}

	res.Status = StatusRelease
	res.Tag = cfg.Tag(res.Version)
	return res, nil
}

// lastRelease finds the highest tag in tag_format that is reachable from ref.
// Reachability matters: a tag on an unmerged branch is not a predecessor.
func lastRelease(repo *git.Repo, cfg *config.Config, ref string, excludeTags []string) (tag string, v semver.Version, ok bool, warnings []string, err error) {
	merged, err := repo.MergedTags(ref)
	if err != nil {
		return "", semver.Version{}, false, nil, err
	}
	var matched int
	for _, t := range merged {
		if slices.Contains(excludeTags, t) {
			continue
		}
		cand, isOurs := cfg.VersionFromTag(t)
		if !isOurs {
			continue
		}
		matched++
		if !ok || v.Less(cand) {
			tag, v, ok = t, cand, true
		}
	}

	all, err := repo.AllTags()
	if err != nil {
		return "", semver.Version{}, false, nil, err
	}
	var foreign []string
	for _, t := range all {
		if _, isOurs := cfg.VersionFromTag(t); isOurs {
			continue
		}
		if looseSemver.MatchString(t) {
			foreign = append(foreign, t)
		}
	}
	if len(foreign) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d tag(s) look like versions but do not match tag_format %q (ignored): %v",
			len(foreign), cfg.TagFormatString(), foreign))
	}
	return tag, v, ok, warnings, nil
}

// inferBump reports which component an explicit --version moved, so that
// RELEASE_BUMP stays meaningful for consumers that switch on it.
func inferBump(from, to semver.Version, hasPrevious bool) semver.Bump {
	if !hasPrevious {
		return semver.Major
	}
	switch {
	case to.Major != from.Major:
		return semver.Major
	case to.Minor != from.Minor:
		return semver.Minor
	default:
		return semver.Patch
	}
}

func logDecision(log *slog.Logger, d rules.Decision) {
	for _, c := range d.NonConforming {
		log.Debug("commit does not follow Conventional Commits, no release intent",
			"sha", c.ShortSHA, "subject", c.Subject)
	}
	for _, ig := range d.Ignored {
		log.Debug("commit ignored", "sha", ig.Commit.ShortSHA, "reason", string(ig.Reason),
			"subject", ig.Commit.Subject)
	}
	log.Debug("analysed range", "counted", len(d.Counted), "ignored", len(d.Ignored),
		"non_conforming", len(d.NonConforming), "bump", d.Bump.String())
}
