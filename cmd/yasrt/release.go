// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"git.ole-hartwig.eu/yasrt/cli/internal/analyze"
	"git.ole-hartwig.eu/yasrt/cli/internal/config"
	"git.ole-hartwig.eu/yasrt/cli/internal/git"
	"git.ole-hartwig.eu/yasrt/cli/internal/gitlab"
	"git.ole-hartwig.eu/yasrt/cli/internal/output"
	"git.ole-hartwig.eu/yasrt/cli/internal/release"
	"git.ole-hartwig.eu/yasrt/cli/internal/semver"
)

func cmdRelease(args []string) error {
	var (
		common     commonFlags
		input      string
		reportPath string
		remote     string
		branch     string
		gpgVar     string
		dryRun     bool
		dir        string
	)
	fs := flag.NewFlagSet("yasrt release", flag.ContinueOnError)
	common.register(fs)
	fs.StringVar(&input, "input", ".release.env", "dotenv file produced by `yasrt next`")
	fs.StringVar(&reportPath, "report", "release-report.json", "where to write the run report")
	fs.StringVar(&remote, "remote", "origin", "git remote to push to")
	fs.StringVar(&branch, "branch", envOr("CI_DEFAULT_BRANCH", ""), "branch for the release commit")
	fs.StringVar(&gpgVar, "gpg-key-var", "GPG_SEM_REL_B64", "environment variable holding the base64 signing key")
	fs.BoolVar(&dryRun, "dry-run", false, "render everything, write nothing")
	fs.StringVar(&dir, "dir", ".", "repository directory")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "yasrt release — publish the release decided by `yasrt next`.\n\n")
		fs.PrintDefaults()
	}
	if err := parse(fs, args); err != nil {
		return err
	}

	token := os.Getenv("CI_JOB_TOKEN")
	log := common.logger(token, os.Getenv(gpgVar))

	cfg, err := config.Load(common.config)
	if err != nil {
		return err
	}
	repo, err := git.Open(dir)
	if err != nil {
		return err
	}

	res, err := loadResult(repo, cfg, input, log)
	if err != nil {
		return err
	}

	var client *gitlab.Client
	apiURL, projectID := os.Getenv("CI_API_V4_URL"), os.Getenv("CI_PROJECT_ID")
	if apiURL != "" && projectID != "" && token != "" {
		client = gitlab.New(apiURL, projectID, token)
	} else {
		log.Warn("no GitLab API credentials in the environment; skipping the release and trigger steps",
			"have_api_url", apiURL != "", "have_project_id", projectID != "", "have_token", token != "")
	}

	rep, runErr := release.Run(context.Background(), release.Options{
		Repo:       repo,
		Config:     cfg,
		Result:     res,
		Client:     client,
		Remote:     remote,
		Branch:     branch,
		Token:      token,
		ProjectURL: os.Getenv("CI_PROJECT_URL"),
		GPGKeyB64:  os.Getenv(gpgVar),
		DryRun:     dryRun,
		Log:        log,
	})

	// The report is written even on failure: it is how a human sees how far the
	// run got before it stopped.
	if reportPath != "" && rep != nil && !dryRun {
		if err := release.WriteReport(reportPath, rep); err != nil {
			log.Warn("could not write the report", "path", reportPath, "err", err)
		}
	}
	if runErr != nil {
		return runErr
	}

	if common.json {
		return release.WriteReport("/dev/stdout", rep)
	}
	fmt.Printf("released %s as %s\n", rep.Version, rep.Tag)
	if rep.ReleaseURL != "" {
		fmt.Println(rep.ReleaseURL)
	}
	return nil
}

// loadResult reconstructs the analysis. The version, tag and commit come from
// the handshake written by `next`, because that is what the build used; the
// commit range is re-read from git, because the notes cannot be carried in a
// dotenv file.
func loadResult(repo *git.Repo, cfg *config.Config, input string, log *slog.Logger) (*analyze.Result, error) {
	env, fromFile := readHandshake(input, log)

	opts := analyze.Options{IgnoreExistingTag: true}
	if c := env[output.KeyCommit]; c != "" {
		opts.Ref = c
	}
	res, err := analyze.Run(repo, cfg, opts, log)
	if err != nil {
		return nil, err
	}

	if len(env) == 0 {
		log.Warn("no handshake from `yasrt next` found; analysing afresh. " +
			"The result may differ from what the build job used.")
		return res, nil
	}

	status := analyze.Status(env[output.KeyStatus])
	if status == "" {
		status = res.Status
	}
	res.Status = status

	if v := env[output.KeyVersion]; v != "" {
		parsed, err := semver.Parse(v)
		if err != nil {
			return nil, fmt.Errorf("%s in the handshake: %w", output.KeyVersion, err)
		}
		res.Version = parsed
	}
	if t := env[output.KeyTag]; t != "" {
		res.Tag = t
	} else if res.Status == analyze.StatusRelease {
		res.Tag = cfg.Tag(res.Version)
	}
	if p := env[output.KeyPrevious]; p != "" {
		res.Previous = p
	}
	if r := env[output.KeyReason]; r != "" {
		res.Reason = r
	}
	if b := env[output.KeyBump]; b != "" {
		if parsed, err := semver.ParseBump(b); err == nil {
			res.Bump = parsed
		}
	}
	if c := env[output.KeyCommit]; c != "" {
		res.Commit = c
	}
	if fromFile {
		log.Info("handshake loaded", "source", input, "status", string(res.Status), "tag", res.Tag)
	} else {
		log.Info("handshake loaded from the environment", "status", string(res.Status), "tag", res.Tag)
	}
	return res, nil
}

// readHandshake prefers the dotenv file, falls back to the environment (which
// is where GitLab puts a dotenv report in a later job), and returns nothing
// when neither is present.
func readHandshake(path string, log *slog.Logger) (map[string]string, bool) {
	if path != "" {
		if kv, err := output.ReadDotenv(path); err == nil && kv[output.KeyStatus] != "" {
			return kv, true
		} else if err != nil && !os.IsNotExist(err) {
			log.Warn("could not read the handshake file", "path", path, "err", err)
		}
	}
	if os.Getenv(output.KeyStatus) == "" {
		return nil, false
	}
	kv := map[string]string{}
	for _, k := range []string{
		output.KeyStatus, output.KeyVersion, output.KeyTag,
		output.KeyPrevious, output.KeyBump, output.KeyReason, output.KeyCommit,
	} {
		if v := os.Getenv(k); v != "" {
			kv[k] = v
		}
	}
	return kv, false
}
