// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package forge

import (
	"fmt"
	"os"
	"strings"
)

// Environment is what a CI system tells a job about where it is running.
type Environment struct {
	Kind Kind
	// APIURL is the API root: .../api/v4 on GitLab, .../api/v3 or
	// https://api.github.com on GitHub, .../api/v1 on Forgejo.
	APIURL string
	// Repo identifies the project: a numeric id or path on GitLab,
	// owner/name elsewhere.
	Repo string
	// ProjectURL is the web address of the repository, used for the links in
	// release notes.
	ProjectURL string
	Token      string
	// Branch is the branch being built, DefaultBranch the repository's own.
	Branch        string
	DefaultBranch string
}

// Getenv is the environment lookup, injected so detection can be tested
// without touching the process environment.
type Getenv func(string) string

// OSGetenv reads the real environment.
func OSGetenv(k string) string { return os.Getenv(k) }

// Detect works out which platform the job is running on.
//
// The order matters. Forgejo's Actions runner sets GITHUB_* variables for
// compatibility, so a naive GITHUB_ACTIONS check would call every Forgejo job
// GitHub. Forgejo is therefore identified first, by its own marker or by a
// server URL that is not github.com — and getting that wrong would send a
// release to the wrong API with a token that does not belong to it.
func Detect(env Getenv) (Environment, bool) {
	if v := env("GITLAB_CI"); v == "true" || env("CI_API_V4_URL") != "" {
		return Environment{
			Kind:          GitLab,
			APIURL:        env("CI_API_V4_URL"),
			Repo:          env("CI_PROJECT_ID"),
			ProjectURL:    env("CI_PROJECT_URL"),
			Token:         env("CI_JOB_TOKEN"),
			Branch:        env("CI_COMMIT_BRANCH"),
			DefaultBranch: env("CI_DEFAULT_BRANCH"),
		}, true
	}

	server := strings.TrimRight(env("GITHUB_SERVER_URL"), "/")
	repo := env("GITHUB_REPOSITORY")
	forgejo := env("FORGEJO_ACTIONS") == "true" || env("GITEA_ACTIONS") == "true" ||
		(server != "" && !isGitHubDotCom(server))

	if env("GITHUB_ACTIONS") == "true" || repo != "" {
		kind := GitHub
		api := env("GITHUB_API_URL")
		if forgejo {
			kind = Forgejo
			if api == "" && server != "" {
				api = server + "/api/v1"
			}
		} else if api == "" {
			api = "https://api.github.com"
		}
		if server == "" {
			server = "https://github.com"
		}
		return Environment{
			Kind:          kind,
			APIURL:        api,
			Repo:          repo,
			ProjectURL:    server + "/" + repo,
			Token:         firstNonEmpty(env("FORGEJO_TOKEN"), env("GITHUB_TOKEN")),
			Branch:        branchFromRef(env("GITHUB_REF")),
			DefaultBranch: env("GITHUB_BASE_REF"),
		}, true
	}
	return Environment{}, false
}

func isGitHubDotCom(server string) bool {
	s := strings.ToLower(server)
	return s == "https://github.com" || s == "http://github.com" ||
		strings.HasSuffix(s, "://github.com")
}

// branchFromRef turns refs/heads/main into main. A tag ref yields nothing,
// which is correct: a tag pipeline is not on a branch.
func branchFromRef(ref string) string {
	if rest, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return rest
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// New builds a client for the detected environment. An explicit kind overrides
// detection, for the case where a job runs somewhere the markers do not say.
func New(e Environment, override string) (Client, Environment, error) {
	if override != "" {
		k, err := ParseKind(override)
		if err != nil {
			return nil, e, err
		}
		e.Kind = k
	}
	if e.APIURL == "" || e.Repo == "" || e.Token == "" {
		return nil, e, fmt.Errorf(
			"%s: need an API URL, a repository and a token (have url=%v repo=%v token=%v)",
			e.Kind, e.APIURL != "", e.Repo != "", e.Token != "")
	}
	switch e.Kind {
	case GitLab:
		return newGitLab(e), e, nil
	case GitHub, Forgejo:
		return newGitHubish(e), e, nil
	}
	return nil, e, fmt.Errorf("unsupported forge %q", e.Kind)
}
