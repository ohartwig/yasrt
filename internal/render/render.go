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
	Version  semver.Version
	Previous string
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
			fmt.Fprintf(&b, "- %s\n", strings.ReplaceAll(strings.TrimSpace(n), "\n", " "))
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

// entry renders one commit line: "- scope: description (sha) (refs)".
func entry(c conventional.Commit, projectURL string) string {
	var b strings.Builder
	b.WriteString("- ")
	if c.Scope != "" {
		b.WriteString(c.Scope)
		b.WriteString(": ")
	}
	b.WriteString(c.Description)
	if c.ShortSHA != "" {
		fmt.Fprintf(&b, " (%s)", c.ShortSHA)
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
	case "#":
		return fmt.Sprintf("[%s](%s/-/issues/%s)", text, base, number)
	case "!":
		return fmt.Sprintf("[%s](%s/-/merge_requests/%s)", text, base, number)
	}
	return text
}

// ChangelogHeading is the block that introduces one release in CHANGELOG.md.
func ChangelogHeading(v semver.Version, date time.Time) string {
	return fmt.Sprintf("## [%s] - %s", v, date.Format(time.DateOnly))
}

// PrependChangelog inserts a release block at the top of an existing changelog,
// below any document title, and creates the document when it is missing.
func PrependChangelog(existing string, in Input, notes string) string {
	block := ChangelogHeading(in.Version, in.Date) + "\n\n" + strings.TrimRight(notes, "\n") + "\n"

	if strings.TrimSpace(existing) == "" {
		return "# Changelog\n\n" + block
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
