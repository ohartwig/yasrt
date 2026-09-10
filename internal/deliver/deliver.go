// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

// Package deliver answers one question: did this commit range change anything
// that ends up in the artefact?
//
// The range matters as much as the patterns. The npm component this replaces
// compared the push range, so a fix bundled with a CI change never shipped
// (improvement item I-081). yasrt compares lastTag..HEAD, the range that is
// actually unreleased.
package deliver

import (
	"slices"

	"github.com/bmatcuk/doublestar/v4"
)

// Result describes why a range is or is not deliverable.
type Result struct {
	Deliverable bool
	// Delivering lists the changed paths that are not excluded. Empty when the
	// range is not deliverable, and the reason a human wants to see when it is.
	Delivering []string
	// Excluded lists the changed paths that matched a non-release pattern.
	Excluded []string
}

// Evaluate reports whether any changed path falls outside the non-release
// patterns. A range that changed nothing is not deliverable: there is nothing
// to ship.
func Evaluate(changed, nonReleasePatterns []string) Result {
	res := Result{}
	for _, p := range changed {
		if Matches(p, nonReleasePatterns) {
			res.Excluded = append(res.Excluded, p)
			continue
		}
		res.Delivering = append(res.Delivering, p)
	}
	res.Deliverable = len(res.Delivering) > 0
	return res
}

// Matches reports whether a repo-relative path matches any pattern. Patterns
// use doublestar semantics, so ** crosses directory separators.
func Matches(path string, patterns []string) bool {
	return slices.ContainsFunc(patterns, func(pat string) bool {
		ok, err := doublestar.Match(pat, path)
		if err != nil {
			// A malformed pattern must not silently swallow a release.
			return false
		}
		if ok {
			return true
		}
		// "docs" should also cover everything beneath it, which is what a
		// human means when they write a bare directory name.
		if !hasMeta(pat) {
			nested, err := doublestar.Match(pat+"/**", path)
			return err == nil && nested
		}
		return false
	})
}

func hasMeta(pat string) bool {
	for _, r := range pat {
		switch r {
		case '*', '?', '[', '{':
			return true
		}
	}
	return false
}

// ValidatePatterns reports the first syntactically invalid pattern, so that
// `yasrt check` can complain before a release depends on it.
func ValidatePatterns(patterns []string) error {
	for _, pat := range patterns {
		if !doublestar.ValidatePattern(pat) {
			return &InvalidPatternError{Pattern: pat}
		}
	}
	return nil
}

type InvalidPatternError struct{ Pattern string }

func (e *InvalidPatternError) Error() string {
	return "invalid glob pattern: " + e.Pattern
}
