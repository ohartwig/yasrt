// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package forge abstracts the hosting platform a release is published to.
//
// Three are supported: GitLab, GitHub and Forgejo. semantic-release maintains
// official plugins for the first two and the community maintains one for
// Gitea/Forgejo, so this is the same ground the chain being replaced covers.
//
// The abstraction is deliberately thin. Only four things actually differ —
// how the CI environment identifies itself, how a request authenticates, how
// the release endpoint is shaped, and how the web URLs for commits, comparisons
// and issues are built. Everything else about a release is the same everywhere,
// and pretending otherwise would buy generality nobody asked for.
package forge

import (
	"context"
	"fmt"
	"strings"
)

// Kind names a hosting platform.
type Kind string

const (
	GitLab  Kind = "gitlab"
	GitHub  Kind = "github"
	Forgejo Kind = "forgejo"
)

// Kinds lists every supported platform.
func Kinds() []Kind { return []Kind{GitLab, GitHub, Forgejo} }

// ParseKind reads a configured platform name. Gitea is accepted as a synonym
// for Forgejo: the API is the one Forgejo inherited.
func ParseKind(s string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "gitlab":
		return GitLab, nil
	case "github":
		return GitHub, nil
	case "forgejo", "gitea":
		return Forgejo, nil
	}
	return "", fmt.Errorf("unknown forge %q (want gitlab, github or forgejo)", s)
}

// Release is the subset of a published release yasrt reads back.
type Release struct {
	TagName string
	Name    string
	URL     string
}

// Link is a URL attached to a release.
//
// Only GitLab models these as first-class release assets. GitHub and Forgejo
// attach uploaded files instead and have no equivalent for a bare link, so
// there they are appended to the release body — stated plainly rather than
// silently dropped.
type Link struct {
	Name     string
	URL      string
	LinkType string
}

// Client publishes releases on one platform.
type Client interface {
	Kind() Kind
	// GetRelease reports ok false when the release does not exist yet, which
	// is the normal case rather than an error.
	GetRelease(ctx context.Context, tag string) (rel *Release, ok bool, err error)
	CreateRelease(ctx context.Context, tag, name, body string) (*Release, error)
	// AddLinks attaches asset links where the platform supports them, and
	// reports what it could not do rather than failing the release.
	AddLinks(ctx context.Context, tag string, links []Link) error
	// SupportsLinks is false where links must go into the body instead.
	SupportsLinks() bool
	// SupportsTriggers is false where after_release.triggers cannot be
	// delivered; only GitLab has the pipeline-trigger endpoint yasrt uses.
	SupportsTriggers() bool
}

// URLs builds the web addresses that appear in release notes.
//
// The shapes differ in exactly one interesting place: what a merge request is
// called. GitLab says merge_requests, GitHub pull, Forgejo pulls.
type URLs struct {
	Kind Kind
	Base string
}

func (u URLs) base() string { return strings.TrimRight(u.Base, "/") }

func (u URLs) Commit(sha string) string {
	if u.Base == "" {
		return ""
	}
	return u.base() + "/commit/" + sha
}

func (u URLs) Compare(from, to string) string {
	if u.Base == "" {
		return ""
	}
	return fmt.Sprintf("%s/compare/%s...%s", u.base(), from, to)
}

func (u URLs) Issue(number string) string {
	if u.Base == "" {
		return ""
	}
	return u.base() + "/issues/" + number
}

// Change is the platform's word for a proposed change: a merge request on
// GitLab, a pull request elsewhere.
func (u URLs) Change(number string) string {
	if u.Base == "" {
		return ""
	}
	switch u.Kind {
	case GitHub:
		return u.base() + "/pull/" + number
	case Forgejo:
		return u.base() + "/pulls/" + number
	default:
		return u.base() + "/merge_requests/" + number
	}
}

// PushCredentials returns the user:password pair that carries a token in a
// push URL. Each platform expects its own username, and using the wrong one
// fails as an authentication error rather than as a clear message.
func PushCredentials(k Kind, token string) string {
	switch k {
	case GitHub:
		return "x-access-token:" + token
	case Forgejo:
		// Forgejo accepts the token as the username with any password.
		return token + ":x-oauth-basic"
	default:
		return "gitlab-ci-token:" + token
	}
}
