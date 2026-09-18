// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

// Package conventional parses Conventional Commits messages.
//
// This is deliberately a small, own implementation rather than a preset
// dependency: yasrt needs the header, the breaking-change markers and the
// footers, and nothing else. A message that does not conform is not an error —
// it simply carries no release intent, which the caller treats as "no bump".
package conventional

import (
	"regexp"
	"strings"
)

// headerRE matches "type(scope)!: description". The scope is optional, the
// bang is optional, and the separator is exactly ": " per the specification.
var headerRE = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)(?:\(([^()]*)\))?(!)?: (.+)$`)

// footerRE matches a footer line: "Token: value" or "Token #value". The space
// form of BREAKING CHANGE is the one exception to the no-spaces token rule and
// is handled separately below.
var footerRE = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9-]*)(?:: | #)(.*)$`)

const (
	breakingSpace = "BREAKING CHANGE"
	breakingDash  = "BREAKING-CHANGE"
)

// Footer is one trailer of a commit message.
type Footer struct {
	Token string
	Value string
}

// Commit is a parsed commit. Conventional reports whether the header actually
// matched; when it did not, Type, Scope and Description are empty and the whole
// subject is preserved verbatim.
type Commit struct {
	SHA         string
	ShortSHA    string
	AuthorName  string
	AuthorEmail string

	Raw     string // the complete message, for marker searches
	Subject string // first line, verbatim
	Body    string // between subject and footers

	Conventional bool
	Type         string
	Scope        string
	Description  string

	Breaking      bool
	BreakingNotes []string
	Footers       []Footer
}

// Parse splits a commit message. It never fails: an unparseable message yields
// a Commit with Conventional false.
func Parse(sha, shortSHA, authorName, authorEmail, message string) Commit {
	c := Commit{
		SHA:         sha,
		ShortSHA:    shortSHA,
		AuthorName:  authorName,
		AuthorEmail: authorEmail,
		Raw:         message,
	}

	message = strings.ReplaceAll(message, "\r\n", "\n")
	lines := strings.Split(message, "\n")
	if len(lines) > 0 {
		c.Subject = strings.TrimRight(lines[0], " \t")
	}

	if m := headerRE.FindStringSubmatch(c.Subject); m != nil {
		c.Conventional = true
		c.Type = strings.ToLower(m[1])
		c.Scope = strings.TrimSpace(m[2])
		c.Description = strings.TrimSpace(m[4])
		if m[3] == "!" {
			c.Breaking = true
		}
	}

	body, footers := splitBodyAndFooters(lines[min(1, len(lines)):])
	c.Body = body
	c.Footers = footers

	for _, f := range footers {
		if isBreakingToken(f.Token) {
			c.Breaking = true
			if v := strings.TrimSpace(f.Value); v != "" {
				c.BreakingNotes = append(c.BreakingNotes, v)
			}
		}
	}
	// A bang with no BREAKING footer still needs something to show in the
	// notes; the description is the only honest candidate.
	if c.Breaking && len(c.BreakingNotes) == 0 && c.Description != "" {
		c.BreakingNotes = append(c.BreakingNotes, c.Description)
	}

	return c
}

func isBreakingToken(tok string) bool {
	up := strings.ToUpper(tok)
	return up == breakingSpace || up == breakingDash
}

// splitBodyAndFooters walks the message after the subject. The footer section
// starts at the first footer-looking line that follows a blank line; from there
// on, a line that does not look like a footer continues the previous one.
func splitBodyAndFooters(lines []string) (string, []Footer) {
	start := -1
	prevBlank := true // the line right after the subject counts as preceded by a break
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			prevBlank = true
			continue
		}
		if prevBlank && looksLikeFooter(ln) {
			start = i
			break
		}
		prevBlank = false
	}
	if start == -1 {
		return strings.TrimSpace(strings.Join(lines, "\n")), nil
	}

	body := strings.TrimSpace(strings.Join(lines[:start], "\n"))

	var footers []Footer
	for _, ln := range lines[start:] {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if tok, val, ok := parseFooterLine(ln); ok {
			footers = append(footers, Footer{Token: tok, Value: strings.TrimSpace(val)})
			continue
		}
		if n := len(footers); n > 0 {
			footers[n-1].Value = strings.TrimSpace(footers[n-1].Value + "\n" + strings.TrimSpace(ln))
		}
	}
	return body, footers
}

func looksLikeFooter(ln string) bool {
	_, _, ok := parseFooterLine(ln)
	return ok
}

func parseFooterLine(ln string) (token, value string, ok bool) {
	for _, b := range []string{breakingSpace, breakingDash} {
		if rest, found := strings.CutPrefix(ln, b+": "); found {
			return b, rest, true
		}
	}
	if m := footerRE.FindStringSubmatch(ln); m != nil {
		return m[1], m[2], true
	}
	return "", "", false
}

// Footer returns the first value for a token, case-insensitively.
func (c Commit) Footer(token string) (string, bool) {
	for _, f := range c.Footers {
		if strings.EqualFold(f.Token, token) {
			return f.Value, true
		}
	}
	return "", false
}

// HasMarker reports whether the message carries a bracketed marker such as
// "[skip release]", anywhere, or a footer whose token matches it once dashes
// and spaces are treated alike. Both spellings occur in the wild.
func (c Commit) HasMarker(marker string) bool {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return false
	}
	if strings.Contains(strings.ToLower(c.Raw), "["+strings.ToLower(marker)+"]") {
		return true
	}
	norm := func(s string) string {
		return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "-", " "), "_", " "))
	}
	want := norm(marker)
	for _, f := range c.Footers {
		if norm(f.Token) == want {
			return true
		}
	}
	return false
}
