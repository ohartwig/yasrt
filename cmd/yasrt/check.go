// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json/v2"
	"flag"
	"fmt"
	"os"
	"strings"

	"git.ole-hartwig.eu/devops/yasrt/internal/config"
	"git.ole-hartwig.eu/devops/yasrt/internal/deliver"
	"git.ole-hartwig.eu/devops/yasrt/internal/git"
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
	)
	fs := flag.NewFlagSet("yasrt check", flag.ContinueOnError)
	common.register(fs)
	fs.StringVar(&dir, "dir", ".", "repository directory")
	fs.StringVar(&remote, "remote", "origin", "git remote to probe")
	fs.BoolVar(&probePush, "push", false,
		"also prove that pushing works by creating and deleting "+checkRef+" on the remote")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "yasrt check — validate the configuration and probe the environment.\n\n")
		fs.PrintDefaults()
	}
	if err := parse(fs, args); err != nil {
		return err
	}

	log := common.logger(os.Getenv("CI_JOB_TOKEN"))
	var out []checkResult
	add := func(name string, ok, fatal bool, detail string) {
		out = append(out, checkResult{Name: name, OK: ok, Detail: detail, Fatal: fatal && !ok})
	}

	// Configuration.
	cfg, cfgErr := config.Load(common.config)
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
	token := os.Getenv("CI_JOB_TOKEN")
	add("CI_JOB_TOKEN", token != "", true, presence(token != ""))
	apiURL, projectID := os.Getenv("CI_API_V4_URL"), os.Getenv("CI_PROJECT_ID")
	add("gitlab api", apiURL != "" && projectID != "", false,
		fmt.Sprintf("CI_API_V4_URL=%s CI_PROJECT_ID=%s", presence(apiURL != ""), presence(projectID != "")))

	if url, err := repo.RemoteURL(remote); err != nil {
		add("remote", false, true, err.Error())
	} else {
		add("remote", true, true, git.MaskURLCredentials(url))
	}

	// Push permission. This is the one probe that writes, so it is opt-in: the
	// rest of `check` is safe to run anywhere.
	if probePush {
		out = append(out, probePushPermission(repo, remote, token))
	} else {
		add("push permission", true, false,
			"not probed; re-run with --push to prove the token may push tags and branches")
	}

	return report(common, out, log)
}

// probePushPermission pushes a throwaway ref and deletes it again. It answers
// the question that otherwise only surfaces during a real release: may this
// identity write to the repository at all?
func probePushPermission(repo *git.Repo, remote, token string) checkResult {
	url, err := repo.RemoteURL(remote)
	if err != nil {
		return checkResult{Name: "push permission", Detail: err.Error(), Fatal: true}
	}
	if token != "" && strings.HasPrefix(url, "https://") {
		rest := strings.TrimPrefix(url, "https://")
		if i := strings.IndexByte(rest, '@'); i >= 0 {
			rest = rest[i+1:]
		}
		url = "https://gitlab-ci-token:" + token + "@" + rest
		repo.AddSecret(url)
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
