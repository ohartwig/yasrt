// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package release

import (
	"fmt"
	"log/slog"
	"strings"

	"git.ole-hartwig.eu/yasrt/cli/internal/analyze"
	"git.ole-hartwig.eu/yasrt/cli/internal/config"
	"git.ole-hartwig.eu/yasrt/cli/internal/forge"
	"git.ole-hartwig.eu/yasrt/cli/internal/git"
	"git.ole-hartwig.eu/yasrt/cli/internal/render"
)

// commitChangelog makes the release commit and pushes it. The push is the one
// step that races with the rest of the world: on a repository where Renovate
// merges several times an hour, the default branch has often moved on by the
// time this runs. semantic-release refuses in that case ("local branch is
// behind the remote one") and releases nothing; the shadow measured that on
// 13 of about 450 pushes in one day. Here the tag is already published, so
// refusing would leave a release without its changelog. Instead the commit is
// rebuilt on the branch as it is now -- it only ever touches the changelog
// and the configured assets -- and pushed again, a few times if need be.
func commitChangelog(repo *git.Repo, cfg *config.Config, res *analyze.Result, o Options,
	in render.Input, sign, changed bool, notes string, log *slog.Logger) (StepStatus, string, string, error) {

	branch := o.Branch
	if branch == "" {
		var err error
		branch, err = repo.CurrentBranch()
		if err != nil {
			return StepFailed, err.Error(), "", err
		}
	}
	pushURL, err := pushURL(repo, o.remote(), o.Token, o.forgeKind())
	if err != nil {
		return StepFailed, err.Error(), "", err
	}
	msg := config.Vars{Version: res.Version, Tag: res.Tag, Notes: notes}.Expand(cfg.ReleaseCommit.Message)

	const attempts = 3
	for attempt := 1; ; attempt++ {
		if err := repo.Add(cfg.ReleaseCommit.Assets...); err != nil {
			return StepFailed, err.Error(), "", err
		}
		staged, err := repo.HasStagedChanges()
		if err != nil {
			return StepFailed, err.Error(), "", err
		}
		if !staged {
			log.Info("nothing to commit for the release", "changed", changed)
			return StepSkipped, "no changes to commit", "", nil
		}
		if err := repo.Commit(msg, cfg.ReleaseCommit.Author, sign); err != nil {
			return StepFailed, err.Error(), "", err
		}
		sha, err := repo.HeadSHA()
		if err != nil {
			return StepFailed, err.Error(), "", err
		}

		pushErr := repo.Push(pushURL, "HEAD:refs/heads/"+branch)
		if pushErr == nil {
			log.Info("release commit pushed", "sha", short(sha), "branch", branch, "signed", sign, "attempt", attempt)
			subject, _, _ := strings.Cut(msg, "\n")
			return StepDone, subject, sha, nil
		}
		masked := repo.Mask(pushErr.Error())
		if !git.IsRejectedPush(pushErr) || attempt == attempts {
			return StepFailed, masked, sha, fmt.Errorf("pushing the release commit failed: %s", masked)
		}

		// The branch moved. Start again from where it is now -- but only if
		// the released commit is still part of it; a rewritten branch is not
		// something to quietly build a changelog on.
		remoteHead, err := repo.FetchRef(pushURL, branch)
		if err != nil {
			return StepFailed, repo.Mask(err.Error()), sha, err
		}
		contained, err := repo.IsAncestor(res.Commit, remoteHead)
		if err != nil {
			return StepFailed, err.Error(), sha, err
		}
		if !contained {
			err := fmt.Errorf("%s no longer contains the released commit %s (now at %s); not rebuilding the release commit on a rewritten branch",
				branch, short(res.Commit), short(remoteHead))
			return StepFailed, err.Error(), sha, err
		}
		log.Warn("branch moved on while releasing; rebuilding the release commit on its new head",
			"branch", branch, "was", short(res.Commit), "now", short(remoteHead), "attempt", attempt)
		if err := repo.ResetHard(remoteHead); err != nil {
			return StepFailed, err.Error(), sha, err
		}
		changed, err = writeChangelog(repo.Dir(), cfg.Changelog.File, in, notes)
		if err != nil {
			return StepFailed, err.Error(), sha, err
		}
	}
}

// configureIdentity writes the release author into the repository's own
// config, so that a fresh CI container -- which has no identity at all --
// can make the tag and the commit. The author is configuration, not
// environment: release_commit.author has a default, so there is always one.
func configureIdentity(repo *git.Repo, cfg *config.Config) error {
	name, email := parseAuthor(cfg.ReleaseCommit.Author)
	dn, de := parseAuthor(config.DefaultReleaseAuthor)
	if name == "" {
		name = dn
	}
	if email == "" {
		email = de
	}
	if err := repo.Config("user.name", name); err != nil {
		return err
	}
	return repo.Config("user.email", email)
}

func parseAuthor(a string) (name, email string) {
	a = strings.TrimSpace(a)
	if a == "" {
		return "", ""
	}
	if i := strings.Index(a, "<"); i >= 0 {
		name = strings.TrimSpace(a[:i])
		email = strings.TrimSuffix(strings.TrimSpace(a[i+1:]), ">")
		return name, email
	}
	return a, ""
}

// pushURL returns the remote's URL with any embedded credentials removed and
// the token registered as a git credential instead. A token in the URL is a
// token in the argument list, which every process in the container can read;
// the credential helper hands it over through the environment.
func pushURL(repo *git.Repo, remote, token string, kind forge.Kind) (string, error) {
	raw, err := repo.RemoteURL(remote)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(raw, "https://") {
		return raw, nil
	}
	rest := strings.TrimPrefix(raw, "https://")
	if i := strings.IndexByte(rest, '@'); i >= 0 {
		// Credentials the runner put there belong to the runner's own clone;
		// masking them is enough, the helper carries ours.
		repo.AddSecret(rest[:i])
		rest = rest[i+1:]
	}
	if token != "" {
		repo.UseCredential(forge.PushCredential(kind, token))
	}
	return "https://" + rest, nil
}
