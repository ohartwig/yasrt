// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package rules turns a range of parsed commits into a single release size.
//
// Two independent things happen here. Filtering removes commits that must not
// influence a release at all — our own release commit above all, which is the
// second of the three loop guards. Evaluation then maps what remains through
// the configured rules: first rule that matches a commit wins for that commit,
// and the highest bump across all commits wins overall.
package rules

import (
	"strings"

	"git.ole-hartwig.eu/yasrt/cli/internal/config"
	"git.ole-hartwig.eu/yasrt/cli/internal/conventional"
	"git.ole-hartwig.eu/yasrt/cli/internal/semver"
)

// IgnoreReason says why a commit was excluded, for the debug log.
type IgnoreReason string

const (
	IgnoredByScope   IgnoreReason = "scope"
	IgnoredByAuthor  IgnoreReason = "author"
	IgnoredByTrailer IgnoreReason = "trailer"
)

// Ignored pairs a commit with the reason it was dropped.
type Ignored struct {
	Commit conventional.Commit
	Reason IgnoreReason
}

// Decision is the outcome for a whole range.
type Decision struct {
	Bump semver.Bump
	// Reason is the commit type that justified the winning bump, or "breaking"
	// when a breaking change did. Empty when nothing releases.
	Reason string

	// Counted are the conventional, non-ignored commits. Only these appear in
	// the release notes.
	Counted []conventional.Commit
	// Ignored were filtered out before evaluation.
	Ignored []Ignored
	// NonConforming did not parse as Conventional Commits. They count as "no
	// release" and are deliberately absent from the notes.
	NonConforming []conventional.Commit
}

// Breaking returns the breaking-change notes across all counted commits.
func (d Decision) Breaking() []string {
	var out []string
	for _, c := range d.Counted {
		out = append(out, c.BreakingNotes...)
	}
	return out
}

// Evaluate filters and scores a range. Commits are expected newest first.
func Evaluate(commits []conventional.Commit, cfg *config.Config) Decision {
	var d Decision

	for _, c := range commits {
		if reason, ok := ignoreReason(c, cfg); ok {
			d.Ignored = append(d.Ignored, Ignored{Commit: c, Reason: reason})
			continue
		}
		if !c.Conventional {
			d.NonConforming = append(d.NonConforming, c)
			continue
		}
		d.Counted = append(d.Counted, c)
	}

	for _, c := range d.Counted {
		bump := match(c, cfg.Rules)
		if bump <= d.Bump {
			continue
		}
		d.Bump = bump
		d.Reason = c.Type
		if c.Breaking {
			d.Reason = "breaking"
		}
	}
	return d
}

// match applies the rules to one commit: first match wins.
func match(c conventional.Commit, rs []config.Rule) semver.Bump {
	for _, r := range rs {
		if r.Breaking != nil && *r.Breaking != c.Breaking {
			continue
		}
		if r.Type != "" && !strings.EqualFold(r.Type, c.Type) {
			continue
		}
		if r.Scope != "" && !strings.EqualFold(r.Scope, c.Scope) {
			continue
		}
		b, err := semver.ParseBump(r.Release)
		if err != nil {
			return semver.None // validation already rejected this; be safe
		}
		return b
	}
	return semver.None
}

func ignoreReason(c conventional.Commit, cfg *config.Config) (IgnoreReason, bool) {
	for _, s := range cfg.Ignore.Scopes {
		if c.Scope != "" && strings.EqualFold(s, c.Scope) {
			return IgnoredByScope, true
		}
	}
	for _, a := range cfg.Ignore.Authors {
		if matchesAuthor(a, c.AuthorName, c.AuthorEmail) {
			return IgnoredByAuthor, true
		}
	}
	for _, tr := range cfg.Ignore.Trailers {
		if c.HasMarker(tr) {
			return IgnoredByTrailer, true
		}
	}
	return "", false
}

// matchesAuthor accepts the display name, the full address, or the local part
// of the address. "renovate-bot" should catch renovate-bot@example.com without
// anybody having to write the domain into every config file.
func matchesAuthor(want, name, email string) bool {
	if want == "" {
		return false
	}
	if strings.EqualFold(want, name) || strings.EqualFold(want, email) {
		return true
	}
	local, _, found := strings.Cut(email, "@")
	return found && strings.EqualFold(want, local)
}
