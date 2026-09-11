// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package render produces release notes and the CHANGELOG entry.
//
// Rendering is pure: it takes a decision and returns text. That is what makes
// the golden tests meaningful, and it is why nothing here touches git.
package render

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"git.ole-hartwig.eu/yasrt/cli/internal/config"
	"git.ole-hartwig.eu/yasrt/cli/internal/conventional"
	"git.ole-hartwig.eu/yasrt/cli/internal/rules"
	"git.ole-hartwig.eu/yasrt/cli/internal/semver"
)

// BreakingTitle heads the block that precedes every other section, because a
// breaking change is the one thing a reader must not scroll past.
const BreakingTitle = ":boom: BREAKING CHANGES"

// Input is everything the renderer needs.
type Input struct {
	Version semver.Version
	// Previous and Tag are the predecessor and current tag as written in the
	// repository, used for the comparison link in the heading.
	Previous string
	Tag      string
	Date     time.Time
	Decision rules.Decision
	Sections []config.Section
	// ProjectURL is the web URL of the project, used to turn #123 and !45 into
	// links. Without it, references are rendered as plain text.
	ProjectURL string
}

// refRE finds issue (#12) and merge request (!34) references.
var refRE = regexp.MustCompile(`([#!])(\d+)`)

// Notes renders the release body: the breaking-change block, then the
// configured sections in their fixed order, each omitted when empty.
func Notes(in Input) string {
	var b strings.Builder

	if notes := in.Decision.Breaking(); len(notes) > 0 {
		fmt.Fprintf(&b, "### %s\n\n", BreakingTitle)
		for _, n := range notes {
			fmt.Fprintf(&b, "* %s\n", strings.ReplaceAll(strings.TrimSpace(n), "\n", " "))
		}
		b.WriteString("\n")
	}

	byType := map[string][]conventional.Commit{}
	for _, c := range in.Decision.Counted {
		byType[c.Type] = append(byType[c.Type], c)
	}

	for _, sec := range in.Sections {
		commits := byType[strings.ToLower(sec.Type)]
		if len(commits) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n", sec.Title)
		for _, c := range commits {
			b.WriteString(entry(c, in.ProjectURL))
		}
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// entry renders one commit line in the shape conventional-changelog produces,
// because that is what every CHANGELOG.md in this estate already contains:
//
//   - **scope:** description ([abc1234](<url>/-/commit/<sha>))
//
// Matching it matters more than preferring another shape: a migrated
// repository's changelog would otherwise change format halfway down the file.
func entry(c conventional.Commit, projectURL string) string {
	var b strings.Builder
	b.WriteString("* ")
	if c.Scope != "" {
		fmt.Fprintf(&b, "**%s:** ", c.Scope)
	}
	b.WriteString(c.Description)
	if c.ShortSHA != "" {
		if projectURL != "" {
			fmt.Fprintf(&b, " ([%s](%s/commit/%s))",
				c.ShortSHA, strings.TrimRight(projectURL, "/"), c.SHA)
		} else {
			fmt.Fprintf(&b, " (%s)", c.ShortSHA)
		}
	}
	if refs := references(c, projectURL); refs != "" {
		fmt.Fprintf(&b, " (%s)", refs)
	}
	b.WriteString("\n")
	return b.String()
}

// references collects issue and merge-request references from the footers.
// Only footers are scanned: prose in the body mentioning a number is not a
// reference, and turning it into a link would be a lie.
func references(c conventional.Commit, projectURL string) string {
	seen := map[string]bool{}
	var out []string
	for _, f := range c.Footers {
		if isBreaking(f.Token) {
			continue
		}
		for _, m := range refRE.FindAllStringSubmatch(f.Value, -1) {
			token := m[1] + m[2]
			if seen[token] {
				continue
			}
			seen[token] = true
			out = append(out, link(m[1], m[2], projectURL))
		}
	}
	return strings.Join(out, ", ")
}

func isBreaking(tok string) bool {
	up := strings.ToUpper(tok)
	return up == "BREAKING CHANGE" || up == "BREAKING-CHANGE"
}

func link(kind, number, projectURL string) string {
	text := kind + number
	if projectURL == "" {
		return text
	}
	base := strings.TrimRight(projectURL, "/")
	switch kind {
	// Without the /-/ infix, matching the links the estate's existing
	// changelogs carry. GitLab serves both forms; consistency inside a file
	// that is about to gain entries from a different tool matters more.
	case "#":
		return fmt.Sprintf("[%s](%s/issues/%s)", text, base, number)
	case "!":
		return fmt.Sprintf("[%s](%s/merge_requests/%s)", text, base, number)
	}
	return text
}

// ChangelogHeading introduces one release, in the shape the estate's existing
// changelogs use: the version links to the comparison against its predecessor,
// and the date is parenthesised.
//
//	## [1.1.1](<url>/-/compare/1.1.0...1.1.1) (2026-08-24)
//
// Without a project URL or a predecessor there is nothing to compare against,
// and the heading degrades to the plain form rather than linking nowhere.
func ChangelogHeading(v semver.Version, date time.Time) string {
	return headingFor(v, date, "", "", "")
}

func headingFor(v semver.Version, date time.Time, projectURL, previousTag, tag string) string {
	day := date.Format(time.DateOnly)
	if projectURL == "" || previousTag == "" || tag == "" {
		return fmt.Sprintf("## [%s] (%s)", v, day)
	}
	return fmt.Sprintf("## [%s](%s/compare/%s...%s) (%s)",
		v, strings.TrimRight(projectURL, "/"), previousTag, tag, day)
}

// PrependChangelog inserts a release block at the top of an existing changelog,
// below any document title, and creates the document when it is missing.
func PrependChangelog(existing string, in Input, notes string) string {
	block := headingFor(in.Version, in.Date, in.ProjectURL, in.Previous, in.Tag) +
		"\n\n" + strings.TrimRight(notes, "\n") + "\n"

	// No document title: the estate's changelogs start straight at the first
	// release heading, and adding one would put a line above every existing
	// file's history at the moment it migrates.
	if strings.TrimSpace(existing) == "" {
		return block
	}

	head, rest := splitTitle(existing)
	if head == "" {
		return block + "\n" + strings.TrimLeft(existing, "\n")
	}
	return head + "\n" + block + "\n" + strings.TrimLeft(rest, "\n")
}

// splitTitle separates a leading "# Title" plus any prose before the first
// release heading, so a new entry lands under the title rather than above it.
func splitTitle(doc string) (head, rest string) {
	lines := strings.Split(doc, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "# ") {
		return "", doc
	}
	for i, ln := range lines {
		if i > 0 && strings.HasPrefix(ln, "## ") {
			return strings.TrimRight(strings.Join(lines[:i], "\n"), "\n") + "\n", strings.Join(lines[i:], "\n")
		}
	}
	return strings.TrimRight(doc, "\n") + "\n", ""
}
