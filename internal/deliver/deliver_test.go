// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package deliver

import (
	"slices"
	"testing"
)

var imagePaths = []string{"CHANGELOG.md", "README.md", "docs/**", "lefthook.yml"}
var packagePaths = []string{"CHANGELOG.md", "README.md", "docs/**", ".gitlab-ci.yml", "lefthook.yml", ".gitlab/**"}

func TestMatches(t *testing.T) {
	for _, tc := range []struct {
		path string
		pats []string
		want bool
	}{
		{"CHANGELOG.md", imagePaths, true},
		{"docs/guide.md", imagePaths, true},
		{"docs/deep/nested/page.md", imagePaths, true},
		{"Containerfile", imagePaths, false},
		{"src/main.go", imagePaths, false},
		{"documentation.md", imagePaths, false}, // docs/** must not match a prefix
		// doublestar defines docs/** to include docs itself, and yasrt follows
		// the library rather than inventing its own dialect.
		{"docs", imagePaths, true},

		// The image/package asymmetry: CI config builds an image.
		{".gitlab-ci.yml", imagePaths, false},
		{".gitlab-ci.yml", packagePaths, true},
		{".gitlab/CODEOWNERS", packagePaths, true},
		{".gitlab/CODEOWNERS", imagePaths, false},

		// A bare directory name covers what is under it.
		{"docs/x.md", []string{"docs"}, true},
		{"docsy/x.md", []string{"docs"}, false},

		{"anything", nil, false},
	} {
		if got := Matches(tc.path, tc.pats); got != tc.want {
			t.Errorf("Matches(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestEvaluate(t *testing.T) {
	t.Run("only excluded paths is not deliverable", func(t *testing.T) {
		r := Evaluate([]string{"docs/a.md", "README.md"}, imagePaths)
		if r.Deliverable {
			t.Error("docs-only change should not be deliverable")
		}
		if len(r.Excluded) != 2 || len(r.Delivering) != 0 {
			t.Errorf("%+v", r)
		}
	})

	t.Run("one real change is enough", func(t *testing.T) {
		// This is the I-081 case: a genuine change bundled with docs must ship.
		r := Evaluate([]string{"docs/a.md", "src/main.go"}, imagePaths)
		if !r.Deliverable {
			t.Fatal("a source change alongside docs must be deliverable")
		}
		if !slices.Equal(r.Delivering, []string{"src/main.go"}) {
			t.Errorf("Delivering = %q", r.Delivering)
		}
	})

	t.Run("ci change ships an image but not a package", func(t *testing.T) {
		if !Evaluate([]string{".gitlab-ci.yml"}, imagePaths).Deliverable {
			t.Error("for an image, the pipeline builds the deliverable")
		}
		if Evaluate([]string{".gitlab-ci.yml"}, packagePaths).Deliverable {
			t.Error("for a package, the pipeline is not shipped")
		}
	})

	t.Run("empty range is not deliverable", func(t *testing.T) {
		if Evaluate(nil, imagePaths).Deliverable {
			t.Error("nothing changed means nothing to ship")
		}
	})

	t.Run("no patterns means everything ships", func(t *testing.T) {
		r := Evaluate([]string{"docs/a.md"}, nil)
		if !r.Deliverable {
			t.Error("without exclusions every change is a delivered change")
		}
	})
}

func TestValidatePatterns(t *testing.T) {
	if err := ValidatePatterns([]string{"docs/**", "*.md", "a/{b,c}/d"}); err != nil {
		t.Errorf("valid patterns rejected: %v", err)
	}
	if err := ValidatePatterns([]string{"["}); err == nil {
		t.Error("expected an error for a malformed pattern")
	}
}
