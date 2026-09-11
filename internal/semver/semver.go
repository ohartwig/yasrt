// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package semver implements the small subset of Semantic Versioning that yasrt
// needs: parse, compare and bump MAJOR.MINOR.PATCH. Prerelease identifiers are
// parsed and preserved so that ordering stays correct in repositories that
// still carry -rc tags, but yasrt does not produce them yet (plan.md, P10).
package semver

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Version is a semantic version. Build metadata is deliberately not modelled:
// nothing in this estate uses it, and ignoring it keeps comparison total.
type Version struct {
	Major, Minor, Patch uint64
	Pre                 string // without the leading '-'; empty for a release
}

var ErrSyntax = errors.New("not a semantic version")

// Parse reads "1.2.3" or "1.2.3-rc.1". A leading "v" is not accepted here;
// stripping tag prefixes is the caller's job, because the prefix is part of
// tag_format rather than of the version itself.
func Parse(s string) (Version, error) {
	var v Version
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("%q: %w", s, ErrSyntax)
	}
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return v, fmt.Errorf("%q: %w", s, ErrSyntax)
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return v, fmt.Errorf("%q: %w", s, ErrSyntax)
		}
		switch i {
		case 0:
			v.Major = n
		case 1:
			v.Minor = n
		case 2:
			v.Patch = n
		}
	}
	v.Pre = pre
	return v, nil
}

func (v Version) String() string {
	s := strconv.FormatUint(v.Major, 10) + "." +
		strconv.FormatUint(v.Minor, 10) + "." +
		strconv.FormatUint(v.Patch, 10)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare orders two versions: -1, 0 or 1. A prerelease sorts before its
// release, per SemVer §11.
func (v Version) Compare(o Version) int {
	if c := cmpUint(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmpUint(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmpUint(v.Patch, o.Patch); c != 0 {
		return c
	}
	switch {
	case v.Pre == "" && o.Pre == "":
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	}
	return comparePre(v.Pre, o.Pre)
}

func (v Version) Less(o Version) bool { return v.Compare(o) < 0 }

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func comparePre(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.ParseUint(as[i], 10, 64)
		bn, berr := strconv.ParseUint(bs[i], 10, 64)
		switch {
		case aerr == nil && berr == nil:
			if c := cmpUint(an, bn); c != 0 {
				return c
			}
		case aerr == nil:
			return -1 // numeric identifiers sort before alphanumeric
		case berr == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return cmpUint(uint64(len(as)), uint64(len(bs)))
}

// Bump is the size of a release step.
type Bump int

const (
	None Bump = iota
	Patch
	Minor
	Major
)

func (b Bump) String() string {
	switch b {
	case Patch:
		return "patch"
	case Minor:
		return "minor"
	case Major:
		return "major"
	}
	return "none"
}

// ParseBump reads the value of --force and of a rule's `release:` field.
func ParseBump(s string) (Bump, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "patch":
		return Patch, nil
	case "minor":
		return Minor, nil
	case "major":
		return Major, nil
	case "", "none", "false":
		return None, nil
	}
	return None, fmt.Errorf("unknown release type %q (want major, minor or patch)", s)
}

// Max returns the larger of two bumps, which is how a commit range collapses to
// a single release: the highest bump any single commit asks for wins.
func Max(a, b Bump) Bump {
	if b > a {
		return b
	}
	return a
}

// Next applies a bump. With majorOnZero false, a major bump below 1.0.0 is
// lowered to a minor one: 0.x is understood to be unstable, so a breaking
// change there does not deserve 1.0.0 by accident.
func (v Version) Next(b Bump, majorOnZero bool) Version {
	if b == Major && v.Major == 0 && !majorOnZero {
		b = Minor
	}
	out := Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch}
	switch b {
	case Major:
		out.Major, out.Minor, out.Patch = v.Major+1, 0, 0
	case Minor:
		out.Minor, out.Patch = v.Minor+1, 0
	case Patch:
		// A prerelease bumping to patch releases the version it prefixes:
		// 1.2.0-rc.1 + patch is 1.2.0, not 1.2.1.
		if v.Pre == "" {
			out.Patch = v.Patch + 1
		}
	}
	return out
}
