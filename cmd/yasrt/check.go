// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json/v2"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ohartwig/yasrt/internal/config"
	"github.com/ohartwig/yasrt/internal/deliver"
	"github.com/ohartwig/yasrt/internal/forge"
	"github.com/ohartwig/yasrt/internal/git"
	"github.com/ohartwig/yasrt/internal/gitlabprobe"
	"github.com/ohartwig/yasrt/internal/semver"
)

// checkRef is the throwaway ref used to prove that pushing works. It is deleted
// immediately, whether the push succeeded or not.
const checkRef = "refs/yasrt/check"

type checkResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitzero"`
	// Fatal marks a finding that will stop a real release.
	Fatal bool `json:"fatal,omitzero"`
}

func cmdCheck(args []string) error {
	var (
		common    commonFlags
		dir       string
		probePush bool
		remote    string
		forgeKind string
	)
	fs := flag.NewFlagSet("yasrt check", flag.ContinueOnError)
	common.register(fs)
	fs.StringVar(&dir, "dir", ".", "repository directory")
	fs.StringVar(&remote, "remote", "origin", "git remote to probe")
	fs.StringVar(&forgeKind, "forge", os.Getenv("YASRT_FORGE"),
		"gitlab, github or forgejo; detected from the CI environment when empty")
	fs.BoolVar(&probePush, "push", false,
		"also prove that pushing works by creating and deleting "+checkRef+" on the remote")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "yasrt check — validate the configuration and probe the environment.\n\n")
		fs.PrintDefaults()
	}
	if err := parse(fs, args); err != nil {
		return err
	}

	env, detected := forge.Detect(forge.OSGetenv)
	if forgeKind != "" {
		k, err := forge.ParseKind(forgeKind)
		if err != nil {
			return err
		}
		env.Kind = k
	}
	log := common.logger(env.Token)
	var out []checkResult
	add := func(name string, ok, fatal bool, detail string) {
		out = append(out, checkResult{Name: name, OK: ok, Detail: detail, Fatal: fatal && !ok})
	}

	// Configuration.
	cfg, cfgErr := config.LoadLayered(common.layers(), common.config)
	if cfgErr != nil {
		add("config", false, true, cfgErr.Error())
	} else {
		add("config", true, true, fmt.Sprintf("product=%s tag_format=%s", cfg.Product, cfg.TagFormatString()))
		if err := deliver.ValidatePatterns(cfg.NonReleasePaths()); err != nil {
			add("non_release_paths", false, true, err.Error())
		} else {
			add("non_release_paths", true, false, strings.Join(cfg.NonReleasePaths(), " "))
		}
		if cfg.ReleaseCommitEnabled() {
			add("release_commit", true, false, "enabled: a CHANGELOG commit will be pushed to the default branch")
		} else {
			add("release_commit", true, false, "disabled: the changelog lives only in the GitLab release")
		}
	}

	// Repository.
	repo, repoErr := git.Open(dir)
	if repoErr != nil {
		add("git", false, true, repoErr.Error())
		return report(common, out, log)
	}
	add("git", true, true, repo.Dir())

	if shallow, err := repo.IsShallow(); err != nil {
		add("history", false, true, err.Error())
	} else if shallow {
		add("history", false, true, "shallow clone: set GIT_DEPTH: 0 and GIT_STRATEGY: clone")
	} else {
		add("history", true, true, "full history available")
	}

	if tags, err := repo.AllTags(); err != nil {
		add("tags", false, true, err.Error())
	} else if len(tags) == 0 {
		add("tags", true, false, "no tags yet; the first release will use versioning.initial")
	} else {
		matched := 0
		if cfg != nil {
			for _, t := range tags {
				if _, ok := cfg.VersionFromTag(t); ok {
					matched++
				}
			}
		}
		add("tags", true, false, fmt.Sprintf("%d tag(s), %d matching tag_format", len(tags), matched))
	}

	// Credentials and remote.
	token := env.Token
	switch {
	case detected || forgeKind != "":
		add("forge", true, false, fmt.Sprintf("%s (repo %s)", env.Kind, presence(env.Repo != "")))
	default:
		add("forge", false, false, "no CI environment recognised; pass --forge outside a pipeline")
	}
	add("token", token != "", true, presence(token != ""))
	add("forge api", env.APIURL != "" && env.Repo != "", false,
		fmt.Sprintf("api=%s repo=%s", presence(env.APIURL != ""), presence(env.Repo != "")))

	if url, err := repo.RemoteURL(remote); err != nil {
		add("remote", false, true, err.Error())
	} else {
		add("remote", true, true, git.MaskURLCredentials(url))
	}

	// The release-authority invariant. This is the check that answers, per
	// repository, the question the design otherwise leaves open. Only GitLab
	// exposes the two rule sets it compares; elsewhere the answer is "not
	// probed", never "fine".
	switch {
	case env.Kind != forge.GitLab && env.Kind != "":
		add("release authority", true, false, fmt.Sprintf(
			"not probed on %s: check by hand that the workflow token may create tags matching %s "+
				"(GitHub: a ruleset or tag protection that admits GITHUB_TOKEN with contents: write; "+
				"Forgejo: tag protection that admits the token's user)", env.Kind, cfgTagFormat(cfg)))
	case env.APIURL != "" && env.Repo != "" && token != "" && cfg != nil:
		out = append(out, probeReleaseAuthority(
			gitlabprobe.New(env.APIURL, env.Repo, token), cfg,
			firstNonEmptyStr(env.DefaultBranch, "main")))
	default:
		add("release authority", true, false,
			"not probed; needs CI_API_V4_URL, CI_PROJECT_ID and CI_JOB_TOKEN")
	}

	// Push permission. This is the one probe that writes, so it is opt-in: the
	// rest of `check` is safe to run anywhere.
	if probePush {
		out = append(out, probePushPermission(repo, remote, env.Kind, token))
	} else {
		add("push permission", true, false,
			"not probed; re-run with --push to prove the token may push tags and branches")
	}

	return report(common, out, log)
}

// probePushPermission pushes a throwaway ref and deletes it again. It answers
// the question that otherwise only surfaces during a real release: may this
// identity write to the repository at all?
func probePushPermission(repo *git.Repo, remote string, kind forge.Kind, token string) checkResult {
	url, err := repo.RemoteURL(remote)
	if err != nil {
		return checkResult{Name: "push permission", Detail: err.Error(), Fatal: true}
	}
	if token != "" && strings.HasPrefix(url, "https://") {
		rest := strings.TrimPrefix(url, "https://")
		if i := strings.IndexByte(rest, '@'); i >= 0 {
			repo.AddSecret(rest[:i])
			rest = rest[i+1:]
		}
		url = "https://" + rest
		repo.UseCredential(forge.PushCredential(kind, token))
	}

	head, err := repo.HeadSHA()
	if err != nil {
		return checkResult{Name: "push permission", Detail: err.Error(), Fatal: true}
	}
	pushErr := repo.Push(url, head+":"+checkRef)
	// Always attempt the cleanup, including after a failure.
	_ = repo.Push(url, ":"+checkRef)

	if pushErr != nil {
		msg := repo.Mask(pushErr.Error())
		if strings.Contains(msg, "403") || strings.Contains(strings.ToLower(msg), "denied") {
			msg += " — enable the project setting “Allow Git push requests to the repository”, " +
				"and make sure the triggering identity may create protected tags"
		}
		return checkResult{Name: "push permission", Detail: msg, Fatal: true}
	}
	return checkResult{Name: "push permission", OK: true,
		Detail: "the token may push; note that protected-tag creation is a separate permission"}
}

// probeReleaseAuthority compares who may merge into the default branch with who
// may create the release tag.
//
// A CI_JOB_TOKEN push acts as the user who triggered the pipeline, and on the
// default branch that is whoever merged. So the tag push succeeds exactly when
// the weakest role allowed to merge is also allowed to create the tag. If
// merging is Maintainer-only, a Maintainer-only protected tag is consistent and
// nothing needs weakening; if Developers may merge but not tag, every release
// they trigger dies at the tag push, and the repository is misconfigured rather
// than yasrt being at fault.
func probeReleaseAuthority(c *gitlabprobe.Client, cfg *config.Config, defaultBranch string) checkResult {
	ctx := context.Background()
	const name = "release authority"

	branches, err := c.ListProtectedBranches(ctx)
	if err != nil {
		// Never fall through to "unprotected" here: not being able to look is
		// not the same as there being nothing to see.
		return checkResult{Name: name, Fatal: true, Detail: "could not read the branch protection rules: " + err.Error()}
	}
	var branch *gitlabprobe.ProtectedBranch
	for i := range branches {
		if tagRuleMatches(branches[i].Name, defaultBranch) {
			branch = &branches[i]
			break
		}
	}
	if branch == nil {
		return checkResult{Name: name, OK: true, Detail: fmt.Sprintf(
			"%s is not protected, so anyone who can push can release", defaultBranch)}
	}
	mergeLevel := branch.LowestMergeLevel()

	tags, err := c.ListProtectedTags(ctx)
	if err != nil {
		return checkResult{Name: name, Fatal: true, Detail: "could not read the protected-tag rules: " + err.Error()}
	}
	rule, matched := matchingTagRule(tags, cfg)
	if !matched {
		return checkResult{Name: name, OK: true, Detail: fmt.Sprintf(
			"no protected-tag rule matches %s; tag creation is unrestricted", cfg.TagFormatString())}
	}
	createLevel := rule.LowestCreateLevel()

	if createLevel > mergeLevel {
		return checkResult{Name: name, Fatal: true, Detail: fmt.Sprintf(
			"%s may merge into %s but only %s may create tags matching %q — a release triggered by %s will fail at the tag push. "+
				"Either restrict merging to %s, or allow %s to create that tag.",
			mergeLevel, defaultBranch, createLevel, rule.Name, mergeLevel, createLevel, mergeLevel)}
	}
	return checkResult{Name: name, OK: true, Detail: fmt.Sprintf(
		"%s may merge into %s and %s may create %q — consistent",
		mergeLevel, defaultBranch, createLevel, rule.Name)}
}

// matchingTagRule finds the protected-tag rule that governs the tags yasrt
// creates. GitLab rules are wildcards; the tag format's fixed prefix is enough
// to tell which one applies.
func matchingTagRule(tags []gitlabprobe.ProtectedTag, cfg *config.Config) (gitlabprobe.ProtectedTag, bool) {
	sample := cfg.Tag(semver.Version{Major: 9, Minor: 9, Patch: 9})
	var best gitlabprobe.ProtectedTag
	found := false
	for _, t := range tags {
		if !tagRuleMatches(t.Name, sample) {
			continue
		}
		// The most restrictive matching rule is the one that will bite.
		if !found || t.LowestCreateLevel() > best.LowestCreateLevel() {
			best, found = t, true
		}
	}
	return best, found
}

func tagRuleMatches(pattern, tag string) bool {
	prefix, suffix, isWildcard := strings.Cut(pattern, "*")
	if !isWildcard {
		return pattern == tag
	}
	return strings.HasPrefix(tag, prefix) && strings.HasSuffix(tag, suffix)
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func cfgTagFormat(cfg *config.Config) string {
	if cfg == nil {
		return "tag_format"
	}
	return cfg.TagFormatString()
}

func presence(ok bool) string {
	if ok {
		return "set"
	}
	return "missing"
}

func report(common commonFlags, results []checkResult, log interface{ Warn(string, ...any) }) error {
	fatal := 0
	for _, r := range results {
		if r.Fatal {
			fatal++
		}
	}

	if common.json {
		b, err := json.Marshal(struct {
			OK      bool          `json:"ok"`
			Fatal   int           `json:"fatal"`
			Results []checkResult `json:"results"`
		}{OK: fatal == 0, Fatal: fatal, Results: results})
		if err != nil {
			return err
		}
		fmt.Printf("%s\n", b)
	} else {
		for _, r := range results {
			mark := "ok  "
			switch {
			case r.Fatal:
				mark = "FAIL"
			case !r.OK:
				mark = "warn"
			}
			fmt.Printf("%s %-18s %s\n", mark, r.Name, r.Detail)
		}
	}

	if fatal > 0 {
		return &exitCodeError{code: exitError, err: errSilent}
	}
	return nil
}
