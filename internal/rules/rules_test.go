// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package rules

import (
	"strings"
	"testing"

	"github.com/ohartwig/yasrt/internal/config"
	"github.com/ohartwig/yasrt/internal/conventional"
	"github.com/ohartwig/yasrt/internal/semver"
)

func cfg(t *testing.T, src string) *config.Config {
	t.Helper()
	c, err := config.Parse(strings.NewReader(src), "test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func commits(msgs ...string) []conventional.Commit {
	out := make([]conventional.Commit, 0, len(msgs))
	for i, m := range msgs {
		out = append(out, conventional.Parse(
			strings.Repeat("a", 39)+string(rune('0'+i)), "abc123"+string(rune('0'+i)),
			"Ada", "ada@example.invalid", m))
	}
	return out
}

func TestHighestBumpWins(t *testing.T) {
	c := cfg(t, "product: image\n")
	d := Evaluate(commits("fix: a", "feat: b", "docs: c"), c)
	if d.Bump != semver.Minor {
		t.Errorf("Bump = %v, want minor", d.Bump)
	}
	if d.Reason != "feat" {
		t.Errorf("Reason = %q", d.Reason)
	}
	if len(d.Counted) != 3 {
		t.Errorf("docs should still be counted for the notes: %d", len(d.Counted))
	}
}

func TestBreakingWins(t *testing.T) {
	c := cfg(t, "product: image\n")
	d := Evaluate(commits("fix: a", "feat!: b"), c)
	if d.Bump != semver.Major {
		t.Errorf("Bump = %v", d.Bump)
	}
	if d.Reason != "breaking" {
		t.Errorf("Reason = %q, want breaking", d.Reason)
	}
	if got := d.Breaking(); len(got) != 1 {
		t.Errorf("Breaking notes = %q", got)
	}
}

func TestSeveralBreakingChangesProduceOneBumpAndAllNotes(t *testing.T) {
	c := cfg(t, "product: image\n")
	d := Evaluate(commits(
		"feat!: one",
		"refactor: two\n\nBREAKING CHANGE: second note",
	), c)
	if d.Bump != semver.Major {
		t.Errorf("Bump = %v", d.Bump)
	}
	if got := d.Breaking(); len(got) != 2 {
		t.Errorf("all breaking notes must survive, got %q", got)
	}
}

func TestUnmatchedTypeDoesNotRelease(t *testing.T) {
	c := cfg(t, "product: image\n")
	d := Evaluate(commits("docs: a", "style: b", "ci: c"), c)
	if d.Bump != semver.None {
		t.Errorf("Bump = %v, want none", d.Bump)
	}
	if d.Reason != "" {
		t.Errorf("Reason = %q", d.Reason)
	}
}

// The defaults follow semantic-release's preset: build and chore ship
// nothing, revert is a patch. An organisation that disagrees says so in its
// shared defaults; the rules list replaces, it does not merge.
func TestDefaultRulesMatchSemanticRelease(t *testing.T) {
	c := cfg(t, "product: image\n")
	if d := Evaluate(commits("build: bump base image", "chore: tidy"), c); d.Bump != semver.None {
		t.Errorf("build/chore: Bump = %v, want none", d.Bump)
	}
	if d := Evaluate(commits("revert: undo the thing"), c); d.Bump != semver.Patch {
		t.Errorf("revert: Bump = %v, want patch", d.Bump)
	}
}

func TestNonConformingCommitsAreSeparated(t *testing.T) {
	c := cfg(t, "product: image\n")
	d := Evaluate(commits("Merge branch 'x'", "feat: real", "Update README"), c)
	if d.Bump != semver.Minor {
		t.Errorf("Bump = %v", d.Bump)
	}
	if len(d.NonConforming) != 2 {
		t.Errorf("NonConforming = %d", len(d.NonConforming))
	}
	if len(d.Counted) != 1 {
		t.Errorf("non-conforming commits must not reach the notes: %d", len(d.Counted))
	}
}

// Loop guard 2: our own release commit can never produce a release.
func TestReleaseScopeIsIgnored(t *testing.T) {
	c := cfg(t, "product: image\n")
	d := Evaluate(commits("chore(release): 1.2.3"), c)
	if d.Bump != semver.None {
		t.Fatalf("Bump = %v — the release commit must never trigger a release", d.Bump)
	}
	if len(d.Ignored) != 1 || d.Ignored[0].Reason != IgnoredByRelease {
		t.Errorf("Ignored = %+v", d.Ignored)
	}
}

// The scope alone is not reserved: work on a release feature is work.
func TestReleaseScopeOnItsOwnCounts(t *testing.T) {
	c := cfg(t, "product: image\n")
	d := Evaluate(commits("feat(release): triggers expand the ref"), c)
	if d.Bump != semver.Minor {
		t.Fatalf("Bump = %v, want minor: feat(release) is not the release commit", d.Bump)
	}
	if len(d.Ignored) != 0 {
		t.Errorf("Ignored = %+v", d.Ignored)
	}
}

func TestIgnoreByAuthor(t *testing.T) {
	c := cfg(t, "product: package\nignore:\n  authors: [renovate-bot]\n")
	cs := []conventional.Commit{
		conventional.Parse("a", "a", "Renovate Bot", "renovate-bot@example.com", "chore(deps): bump x"),
		conventional.Parse("b", "b", "Ada", "ada@example.invalid", "fix: real work"),
	}
	d := Evaluate(cs, c)
	if d.Bump != semver.Patch {
		t.Errorf("Bump = %v", d.Bump)
	}
	if len(d.Ignored) != 1 || d.Ignored[0].Reason != IgnoredByAuthor {
		t.Errorf("Ignored = %+v", d.Ignored)
	}
	// Without the ignore entry the same commit would have released.
	plain := cfg(t, "product: package\nrules:\n  - type: chore\n    release: patch\n")
	if Evaluate(cs[:1], plain).Bump != semver.Patch {
		t.Error("chore maps to patch when a rule says so")
	}
	// By default it does not: that matches semantic-release's preset.
	if Evaluate(cs[:1], cfg(t, "product: package\n")).Bump != semver.None {
		t.Error("chore must not release by default")
	}
}

func TestIgnoreByTrailer(t *testing.T) {
	c := cfg(t, "product: image\n")
	d := Evaluate(commits("feat: something\n\n[skip release]"), c)
	if d.Bump != semver.None {
		t.Errorf("Bump = %v, want none", d.Bump)
	}
	if len(d.Ignored) != 1 || d.Ignored[0].Reason != IgnoredByTrailer {
		t.Errorf("Ignored = %+v", d.Ignored)
	}
}

func TestFirstMatchingRuleWinsPerCommit(t *testing.T) {
	// A rule list where a broad rule precedes a narrow one: the broad one wins,
	// because matching stops at the first hit.
	c := cfg(t, `
product: image
rules:
  - type: chore
    release: patch
  - type: chore
    scope: deps
    release: minor
`)
	d := Evaluate(commits("chore(deps): bump"), c)
	if d.Bump != semver.Patch {
		t.Errorf("Bump = %v, want patch (first match wins)", d.Bump)
	}
}

func TestScopedRule(t *testing.T) {
	c := cfg(t, `
product: image
rules:
  - type: chore
    scope: deps
    release: minor
  - type: chore
    release: patch
`)
	if got := Evaluate(commits("chore(deps): bump"), c).Bump; got != semver.Minor {
		t.Errorf("scoped rule: %v", got)
	}
	if got := Evaluate(commits("chore: tidy"), c).Bump; got != semver.Patch {
		t.Errorf("unscoped chore: %v", got)
	}
}

func TestBreakingInZeroVersionRuleStillMajorBump(t *testing.T) {
	// rules decide the bump; major_on_zero is applied later, when the version
	// is computed. This test pins that separation.
	c := cfg(t, "product: image\nversioning:\n  major_on_zero: false\n")
	if got := Evaluate(commits("feat!: x"), c).Bump; got != semver.Major {
		t.Errorf("Bump = %v, want major before version arithmetic", got)
	}
}
