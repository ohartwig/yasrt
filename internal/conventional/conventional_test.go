// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package conventional

import (
	"slices"
	"testing"
)

func parse(msg string) Commit {
	return Parse("abc123def", "abc123d", "Ada", "ada@example.invalid", msg)
}

func TestHeader(t *testing.T) {
	for _, tc := range []struct {
		name             string
		msg              string
		conv             bool
		typ, scope, desc string
		breaking         bool
	}{
		{name: "plain", msg: "feat: add thing", conv: true, typ: "feat", desc: "add thing"},
		{name: "scoped", msg: "fix(parser): trim footers", conv: true, typ: "fix", scope: "parser", desc: "trim footers"},
		{name: "bang", msg: "feat!: drop legacy API", conv: true, typ: "feat", desc: "drop legacy API", breaking: true},
		{name: "scoped bang", msg: "refactor(core)!: rename", conv: true, typ: "refactor", scope: "core", desc: "rename", breaking: true},
		{name: "uppercase type normalised", msg: "FEAT: shout", conv: true, typ: "feat", desc: "shout"},
		{name: "empty scope", msg: "chore(): nothing", conv: true, typ: "chore", desc: "nothing"},

		{name: "no colon", msg: "feat add thing", conv: false},
		{name: "no space after colon", msg: "feat:add thing", conv: false},
		{name: "merge commit", msg: "Merge branch 'main' into feature", conv: false},
		{name: "squash without convention", msg: "Update README", conv: false},
		{name: "empty description", msg: "feat: ", conv: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := parse(tc.msg)
			if c.Conventional != tc.conv {
				t.Fatalf("Conventional = %v, want %v", c.Conventional, tc.conv)
			}
			if !tc.conv {
				return
			}
			if c.Type != tc.typ || c.Scope != tc.scope || c.Description != tc.desc {
				t.Errorf("got type=%q scope=%q desc=%q; want %q %q %q",
					c.Type, c.Scope, c.Description, tc.typ, tc.scope, tc.desc)
			}
			if c.Breaking != tc.breaking {
				t.Errorf("Breaking = %v, want %v", c.Breaking, tc.breaking)
			}
		})
	}
}

func TestSubjectPreservedForNonConventional(t *testing.T) {
	c := parse("Update README\n\nsome body")
	if c.Subject != "Update README" {
		t.Errorf("Subject = %q", c.Subject)
	}
	if c.Body != "some body" {
		t.Errorf("Body = %q", c.Body)
	}
}

func TestBreakingFooter(t *testing.T) {
	c := parse("feat: new config format\n\nRewrites the loader.\n\nBREAKING CHANGE: .releaserc.yml is no longer read.")
	if !c.Breaking {
		t.Fatal("expected breaking")
	}
	if !slices.Contains(c.BreakingNotes, ".releaserc.yml is no longer read.") {
		t.Errorf("BreakingNotes = %q", c.BreakingNotes)
	}
	if c.Body != "Rewrites the loader." {
		t.Errorf("Body = %q", c.Body)
	}
}

func TestBreakingDashSpelling(t *testing.T) {
	c := parse("fix: x\n\nBREAKING-CHANGE: gone")
	if !c.Breaking || len(c.BreakingNotes) != 1 {
		t.Fatalf("Breaking=%v notes=%q", c.Breaking, c.BreakingNotes)
	}
}

func TestBangWithoutFooterUsesDescription(t *testing.T) {
	c := parse("feat!: remove the preset mode")
	if !c.Breaking {
		t.Fatal("expected breaking")
	}
	if len(c.BreakingNotes) != 1 || c.BreakingNotes[0] != "remove the preset mode" {
		t.Errorf("BreakingNotes = %q", c.BreakingNotes)
	}
}

func TestBangAndFooterTogether(t *testing.T) {
	c := parse("feat!: new format\n\nBREAKING CHANGE: config moved")
	if len(c.BreakingNotes) != 1 || c.BreakingNotes[0] != "config moved" {
		t.Errorf("want only the footer note, got %q", c.BreakingNotes)
	}
}

func TestFooters(t *testing.T) {
	c := parse("fix: thing\n\nbody paragraph\nsecond line\n\nRefs: #42\nReviewed-by: Ada\nCloses #7")
	if c.Body != "body paragraph\nsecond line" {
		t.Errorf("Body = %q", c.Body)
	}
	if len(c.Footers) != 3 {
		t.Fatalf("Footers = %+v", c.Footers)
	}
	if v, ok := c.Footer("refs"); !ok || v != "#42" {
		t.Errorf("Refs = %q %v", v, ok)
	}
	if v, ok := c.Footer("Closes"); !ok || v != "7" {
		t.Errorf("Closes = %q %v", v, ok)
	}
}

func TestFooterContinuation(t *testing.T) {
	c := parse("feat: x\n\nBREAKING CHANGE: first line\n  continued here")
	if len(c.BreakingNotes) != 1 {
		t.Fatalf("notes = %q", c.BreakingNotes)
	}
	if c.BreakingNotes[0] != "first line\ncontinued here" {
		t.Errorf("note = %q", c.BreakingNotes[0])
	}
}

func TestMultiParagraphBodyIsNotAFooter(t *testing.T) {
	c := parse("feat: x\n\nfirst para\n\nsecond para that is prose, not a footer\n\nRefs: #1")
	if c.Body != "first para\n\nsecond para that is prose, not a footer" {
		t.Errorf("Body = %q", c.Body)
	}
	if len(c.Footers) != 1 {
		t.Errorf("Footers = %+v", c.Footers)
	}
}

func TestHasMarker(t *testing.T) {
	for _, tc := range []struct {
		msg    string
		marker string
		want   bool
	}{
		{"chore: bump\n\n[skip release]", "skip release", true},
		{"chore: bump\n\n[SKIP RELEASE]", "skip release", true},
		{"chore: bump\n\n[release skip]", "release skip", true},
		{"chore: bump", "skip release", false},
		{"chore: bump\n\nSkip-Release: true", "skip release", true},
		{"chore: bump\n\nmentions skip release without brackets", "skip release", false},
	} {
		if got := parse(tc.msg).HasMarker(tc.marker); got != tc.want {
			t.Errorf("HasMarker(%q) on %q = %v, want %v", tc.marker, tc.msg, got, tc.want)
		}
	}
}
