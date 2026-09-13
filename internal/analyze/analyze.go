// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package analyze implements `yasrt next`: it reads a repository and decides
// whether, and to what, it should be released. It writes nothing.
package analyze

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ohartwig/yasrt/internal/config"
	"github.com/ohartwig/yasrt/internal/conventional"
	"github.com/ohartwig/yasrt/internal/deliver"
	"github.com/ohartwig/yasrt/internal/git"
	"github.com/ohartwig/yasrt/internal/rules"
	"github.com/ohartwig/yasrt/internal/semver"
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
	// Branch names the branch being released. It decides whether this is a
	// prerelease, per versioning.prereleases. Empty means: ask git.
	Branch string
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

	// Prerelease is the identifier this release carries, empty for a stable
	// release. Branch is the branch it was decided for.
	Prerelease string
	Branch     string
	// Maintenance is the range this branch releases within, empty when it is
	// not a maintenance branch.
	Maintenance string

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
	base, err := findBaseline(repo, cfg, ref, excluded)
	if err != nil {
		return nil, err
	}
	res.Warnings = base.Warnings

	res.Branch = resolveBranch(repo, opts.Branch)
	preID, isPrerelease := cfg.PrereleaseFor(res.Branch)
	if isPrerelease {
		res.Prerelease = preID
	}
	maintRange, isMaintenance := cfg.MaintenanceFor(res.Branch)
	if isMaintenance {
		res.Maintenance = maintRange.String()
		// A maintenance branch releases inside its own line of versions, so
		// anything outside it is not a predecessor — a 2.0.0 tag reachable
		// from 1.x is history, not a baseline.
		base = base.restrictTo(maintRange)
	}

	// The notes and the changed paths are measured from the last release of any
	// kind; the version core is computed from the last stable one. On a stable
	// branch those are the same tag.
	notesFrom, bumpFrom := base.StableTag, base.StableTag
	res.Previous, res.PreviousVersion, res.HasPrevious = base.StableTag, base.Stable, base.HasStable
	if isPrerelease {
		notesFrom = base.AnyTag
		res.Previous, res.PreviousVersion, res.HasPrevious = base.AnyTag, base.Any, base.HasAny
	}

	raw, err := repo.Log(notesFrom, ref)
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

	// A breaking change cannot leave the range: that is what makes this a
	// maintenance branch rather than merely an old one. The bump is lowered
	// rather than refused, so the fix still ships.
	if isMaintenance {
		capped := maintRange.Cap(res.Bump)
		if capped != res.Bump {
			log.Info("bump capped by the maintenance range",
				"range", maintRange.String(), "from", res.Bump.String(), "to", capped.String())
			res.Bump = capped
		}
	}

	// A prerelease accumulates everything since the last stable release, so the
	// core version follows from that wider range even though the notes do not.
	if isPrerelease && notesFrom != bumpFrom {
		wider, err := repo.Log(bumpFrom, ref)
		if err != nil {
			return nil, err
		}
		parsedWider := make([]conventional.Commit, 0, len(wider))
		for _, rc := range wider {
			parsedWider = append(parsedWider, conventional.Parse(rc.SHA, rc.ShortSHA, rc.AuthorName, rc.AuthorEmail, rc.Message))
		}
		coreDecision := rules.Evaluate(parsedWider, cfg)
		res.Bump = semver.Max(res.Bump, coreDecision.Bump)
		if res.Reason == "" {
			res.Reason = coreDecision.Reason
		}
	}

	// Overrides. Both count as forced, and neither skips deliverability.
	explicit := false
	if opts.Version != "" {
		v, err := semver.Parse(opts.Version)
		if err != nil {
			return nil, fmt.Errorf("--version: %w", err)
		}
		if res.HasPrevious && !res.PreviousVersion.Less(v) {
			return nil, fmt.Errorf("%w: %s is not higher than %s", ErrVersionNotHigher, v, res.PreviousVersion)
		}
		res.Version, explicit = v, true
		res.Reason = ReasonForced
		res.Bump = inferBump(res.PreviousVersion, v, res.HasPrevious)
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
		var core semver.Version
		if base.HasStable {
			core = base.Stable.Next(res.Bump, cfg.MajorOnZero())
		} else {
			// First release: the configured starting point, not a bump of it.
			v, err := semver.Parse(cfg.Versioning.Initial)
			if err != nil {
				return nil, err
			}
			core = v
		}
		if isPrerelease {
			core = nextPrerelease(core, preID, base.Prereleases)
		}
		res.Version = core
	}

	changed, err := repo.ChangedPaths(notesFrom, ref)
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

// baseline is what the repository already released, split by kind.
//
// Stable and prerelease baselines answer different questions. The core version
// of a prerelease is computed from the last *stable* release, because an rc
// accumulates everything since the last real release; the notes and the changed
// paths are computed from the last release of *any* kind, because rc.2 should
// describe what is new since rc.1 rather than repeat it.
type baseline struct {
	StableTag string
	Stable    semver.Version
	HasStable bool

	AnyTag string
	Any    semver.Version
	HasAny bool

	// Prereleases are the matching prerelease versions reachable from the ref,
	// used to find the next counter.
	Prereleases []semver.Version

	Warnings []string
}

// restrictTo drops everything outside a maintenance range, so that the branch
// is compared against its own line of versions rather than against whatever
// happens to be newest in the repository.
func (b baseline) restrictTo(r config.Range) baseline {
	out := baseline{Warnings: b.Warnings}
	keep := func(tag string, v semver.Version, stable bool) {
		if !r.Contains(v) {
			return
		}
		if !out.HasAny || out.Any.Less(v) {
			out.AnyTag, out.Any, out.HasAny = tag, v, true
		}
		if stable {
			if !out.HasStable || out.Stable.Less(v) {
				out.StableTag, out.Stable, out.HasStable = tag, v, true
			}
		}
	}
	if b.HasStable {
		keep(b.StableTag, b.Stable, true)
	}
	if b.HasAny {
		keep(b.AnyTag, b.Any, b.Any.Pre == "")
	}
	for _, v := range b.Prereleases {
		if r.Contains(v) {
			out.Prereleases = append(out.Prereleases, v)
		}
	}
	return out
}

// findBaseline collects the tags in tag_format that are reachable from ref.
// Reachability matters: a tag on an unmerged branch is not a predecessor.
func findBaseline(repo *git.Repo, cfg *config.Config, ref string, excludeTags []string) (baseline, error) {
	var b baseline

	merged, err := repo.MergedTags(ref)
	if err != nil {
		return b, err
	}
	for _, t := range merged {
		if slices.Contains(excludeTags, t) {
			continue
		}
		v, isOurs := cfg.VersionFromTag(t)
		if !isOurs {
			continue
		}
		if !b.HasAny || b.Any.Less(v) {
			b.AnyTag, b.Any, b.HasAny = t, v, true
		}
		if v.Pre == "" {
			if !b.HasStable || b.Stable.Less(v) {
				b.StableTag, b.Stable, b.HasStable = t, v, true
			}
			continue
		}
		b.Prereleases = append(b.Prereleases, v)
	}

	all, err := repo.AllTags()
	if err != nil {
		return b, err
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
		b.Warnings = append(b.Warnings, fmt.Sprintf(
			"%d tag(s) look like versions but do not match tag_format %q (ignored): %v",
			len(foreign), cfg.TagFormatString(), foreign))
	}
	return b, nil
}

// nextPrerelease turns a core version into the next counter for an identifier:
// 2.5.0 with rc becomes 2.5.0-rc.1, or rc.4 when rc.3 already exists.
func nextPrerelease(core semver.Version, identifier string, existing []semver.Version) semver.Version {
	highest := uint64(0)
	prefix := identifier + "."
	for _, v := range existing {
		if v.Major != core.Major || v.Minor != core.Minor || v.Patch != core.Patch {
			continue
		}
		rest, ok := strings.CutPrefix(v.Pre, prefix)
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(rest, 10, 64)
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	core.Pre = fmt.Sprintf("%s.%d", identifier, highest+1)
	return core
}

// resolveBranch decides which branch this analysis is for. In CI the checkout
// is usually detached, so the environment is the authority and git is the
// fallback rather than the other way round.
func resolveBranch(repo *git.Repo, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if b := os.Getenv("CI_COMMIT_BRANCH"); b != "" {
		return b
	}
	if b, err := repo.CurrentBranch(); err == nil {
		return b
	}
	return ""
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
