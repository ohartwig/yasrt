// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package render_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/yasrt/internal/config"
	"github.com/ohartwig/yasrt/internal/conventional"
	"github.com/ohartwig/yasrt/internal/forge"
	"github.com/ohartwig/yasrt/internal/render"
	"github.com/ohartwig/yasrt/internal/rules"
	"github.com/ohartwig/yasrt/internal/semver"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares against testdata/<name>.golden, rewriting it under -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/render -update)", err)
	}
	if got != string(want) {
		t.Errorf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func commit(sha, msg string) conventional.Commit {
	return conventional.Parse(sha+"0000000000000000000000000000000", sha, "Ada", "ada@example.invalid", msg)
}

func input(commits ...conventional.Commit) render.Input {
	return render.Input{
		Version:  semver.Version{Major: 3, Minor: 4, Patch: 0},
		Previous: "3.3.2",
		Tag:      "3.4.0",
		Date:     time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
		Decision: rules.Decision{Counted: commits},
		Sections: config.DefaultSections(),
		URLs:     forge.URLs{Kind: forge.GitLab, Base: "https://git.ole-hartwig.eu/yasrt/cli"},
	}
}

func TestNotesSectionsInFixedOrder(t *testing.T) {
	in := input(
		commit("aaa1111", "chore: tidy up"),
		commit("bbb2222", "feat(config): derive tag format from product"),
		commit("ccc3333", "fix(parser): keep multi-line bodies"),
		commit("ddd4444", "docs: explain the loop guards"),
		commit("eee5555", "feat: add the check subcommand"),
	)
	golden(t, "notes_sections", render.Notes(in))
}

func TestNotesOmitsEmptySections(t *testing.T) {
	out := render.Notes(input(commit("aaa1111", "fix: only a fix")))
	if strings.Contains(out, "Features") || strings.Contains(out, "Chores") {
		t.Errorf("empty sections must not be rendered:\n%s", out)
	}
	golden(t, "notes_single_section", out)
}

func TestNotesBreakingBlockComesFirst(t *testing.T) {
	in := input(
		commit("aaa1111", "feat!: drop the preset mode"),
		commit("bbb2222", "fix: unrelated fix"),
		commit("ccc3333", "refactor: rework loader\n\nBREAKING CHANGE: .releaserc.yml is no longer read"),
	)
	out := render.Notes(in)
	if !strings.HasPrefix(out, "### "+render.BreakingTitle) {
		t.Errorf("breaking block must lead:\n%s", out)
	}
	golden(t, "notes_breaking", out)
}

func TestNotesReferencesBecomeLinks(t *testing.T) {
	in := input(
		commit("aaa1111", "fix: correct the guard\n\nCloses: #42\nRefs: !17"),
		commit("bbb2222", "feat: something\n\nRefs: #7, #8"),
	)
	golden(t, "notes_references", render.Notes(in))
}

func TestNotesWithoutProjectURLRendersPlainReferences(t *testing.T) {
	in := input(commit("aaa1111", "fix: x\n\nCloses: #42"))
	in.URLs = forge.URLs{}
	out := render.Notes(in)
	if strings.Contains(out, "](") {
		t.Errorf("without a project URL there is nothing to link to:\n%s", out)
	}
	if !strings.Contains(out, "#42") {
		t.Errorf("the reference itself must survive:\n%s", out)
	}
}

func TestNotesProseNumbersAreNotReferences(t *testing.T) {
	in := input(commit("aaa1111", "fix: handle up to #5 retries\n\nThe body mentions #99 in prose."))
	out := render.Notes(in)
	if strings.Contains(out, "/-/issues/99") {
		t.Errorf("prose must not be turned into links:\n%s", out)
	}
}

func TestNotesEntryWithoutScope(t *testing.T) {
	out := render.Notes(input(commit("aaa1111", "feat: no scope here")))
	if !strings.Contains(out, "* no scope here (") {
		t.Errorf("got:\n%s", out)
	}
}

func TestNotesEmptyDecision(t *testing.T) {
	out := render.Notes(input())
	if strings.TrimSpace(out) != "" {
		t.Errorf("nothing counted means nothing rendered, got %q", out)
	}
}

func TestChangelogCreatedWhenMissing(t *testing.T) {
	in := input(commit("aaa1111", "feat: first"))
	golden(t, "changelog_new", render.PrependChangelog("", in, render.Notes(in)))
}

func TestChangelogPrependsUnderTitle(t *testing.T) {
	existing := "# Changelog\n\nAll notable changes.\n\n## [3.3.2] (2026-08-01)\n\n### :bug: Fixes\n\n* old fix (999zzzz)\n"
	in := input(commit("aaa1111", "feat: something new"))
	got := render.PrependChangelog(existing, in, render.Notes(in))
	if !strings.HasPrefix(got, "# Changelog\n") {
		t.Errorf("the document title must stay first:\n%s", got)
	}
	if strings.Index(got, "[3.4.0]") > strings.Index(got, "[3.3.2]") {
		t.Errorf("the new release must precede the old one:\n%s", got)
	}
	golden(t, "changelog_prepend", got)
}

func TestChangelogWithoutTitle(t *testing.T) {
	existing := "## [3.3.2] (2026-08-01)\n\n* old thing\n"
	in := input(commit("aaa1111", "fix: newer thing"))
	got := render.PrependChangelog(existing, in, render.Notes(in))
	if !strings.HasPrefix(got, "## [3.4.0]") {
		t.Errorf("without a title the entry goes straight to the top:\n%s", got)
	}
}

// A configured title heads a new or title-less file. A file that already has
// a title keeps its own: the file belongs to the repository.
func TestChangelogTitle(t *testing.T) {
	in := input(commit("aaa1111", "fix: newer thing"))
	in.Title = "Changelog"

	if got := render.PrependChangelog("", in, render.Notes(in)); !strings.HasPrefix(got, "# Changelog\n\n## [3.4.0]") {
		t.Errorf("new file:\n%s", got)
	}
	got := render.PrependChangelog("## [3.3.2] (2026-08-01)\n\n* old thing\n", in, render.Notes(in))
	if !strings.HasPrefix(got, "# Changelog\n\n## [3.4.0]") || !strings.Contains(got, "\n## [3.3.2]") {
		t.Errorf("title-less file:\n%s", got)
	}
	got = render.PrependChangelog("# History\n\n## [3.3.2] (2026-08-01)\n", in, render.Notes(in))
	if !strings.HasPrefix(got, "# History\n") || strings.Contains(got, "# Changelog") {
		t.Errorf("an existing title must win:\n%s", got)
	}
	in.Title = ""
	if got := render.PrependChangelog("", in, render.Notes(in)); strings.HasPrefix(got, "# ") {
		t.Errorf("no title unless configured:\n%s", got)
	}
}

func TestChangelogHeading(t *testing.T) {
	got := render.ChangelogHeading(semver.Version{Major: 1, Minor: 2, Patch: 3},
		time.Date(2026, 9, 11, 13, 30, 0, 0, time.UTC))
	if got != "## [1.2.3] (2026-09-11)" {
		t.Errorf("got %q", got)
	}
}

// A release withdrawn after its tag was cut is cut again under the same
// version by the next push. Its block is already at the top of the file:
// replaced, not repeated - with or without a title, and only when it is the
// first block, never one further down.
func TestChangelogReplacesTheBlockOfARecutVersion(t *testing.T) {
	in := input(commit("aaa1111", "feat: something new"), commit("bbb2222", "fix: found on the retry"))
	stale := "## [3.4.0](https://git.ole-hartwig.eu/yasrt/cli/-/compare/3.4.0...3.3.2) (2026-09-13)\n\n### :sparkles: Features\n\n* something new (aaa1111)\n\n"
	older := "## [3.3.2] (2026-08-01)\n\n* old thing\n"
	for name, existing := range map[string]string{
		"without title": stale + older,
		"with title":    "# Changelog\n\n" + stale + older,
	} {
		got := render.PrependChangelog(existing, in, render.Notes(in))
		if strings.Count(got, "## [3.4.0]") != 1 {
			t.Errorf("%s: the block must appear once:\n%s", name, got)
		}
		if !strings.Contains(got, "found on the retry") || strings.Contains(got, "2026-09-13") {
			t.Errorf("%s: the new block must replace the stale one:\n%s", name, got)
		}
		if strings.Count(got, "## [3.3.2]") != 1 || !strings.Contains(got, "* old thing") {
			t.Errorf("%s: the older block must survive:\n%s", name, got)
		}
	}
	// The version further down is history, not a retry.
	got := render.PrependChangelog("## [3.5.0] (2026-09-14)\n\n* later\n\n"+stale+older, in, render.Notes(in))
	if strings.Count(got, "## [3.4.0]") != 2 {
		t.Errorf("a block below the top is kept:\n%s", got)
	}
}
