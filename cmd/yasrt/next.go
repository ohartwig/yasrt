// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"git.ole-hartwig.eu/yasrt/cli/internal/analyze"
	"git.ole-hartwig.eu/yasrt/cli/internal/config"
	"git.ole-hartwig.eu/yasrt/cli/internal/git"
	"git.ole-hartwig.eu/yasrt/cli/internal/hooks"
	"git.ole-hartwig.eu/yasrt/cli/internal/output"
	"git.ole-hartwig.eu/yasrt/cli/internal/semver"
)

func cmdNext(args []string) error {
	var (
		common     commonFlags
		outPath    string
		ref        string
		force      string
		explicitV  string
		ignoreDeli bool
		failOnSkip bool
		dir        string
	)
	fs := flag.NewFlagSet("yasrt next", flag.ContinueOnError)
	common.register(fs)
	fs.StringVar(&outPath, "output", "", "write the result as dotenv to this path (e.g. .release.env)")
	fs.StringVar(&ref, "ref", "", "analyse this revision instead of HEAD")
	fs.StringVar(&force, "force", "", "force a bump: major, minor or patch")
	fs.StringVar(&explicitV, "version", "", "release exactly this version")
	fs.BoolVar(&ignoreDeli, "ignore-deliverability", false, "release even if only non-release paths changed")
	fs.BoolVar(&failOnSkip, "fail-on-skip", false, "exit 3 on no-bump and 4 on not-deliverable")
	fs.StringVar(&dir, "dir", ".", "repository directory")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "yasrt next — analyse the repository. Writes nothing.\n\n")
		fs.PrintDefaults()
	}
	if err := parse(fs, args); err != nil {
		return err
	}
	if force != "" && explicitV != "" {
		return codeErr(exitUsage, "--force and --version are mutually exclusive")
	}
	// Argument validation belongs here rather than in the analysis, so that a
	// typo exits 2 like every other usage error instead of 1.
	if force != "" {
		b, err := semver.ParseBump(force)
		if err != nil || b == semver.None {
			return codeErr(exitUsage, "--force needs major, minor or patch, not %q", force)
		}
	}
	if explicitV != "" {
		if _, err := semver.Parse(explicitV); err != nil {
			return codeErr(exitUsage, "--version: %v", err)
		}
	}

	log := common.logger()

	cfg, err := config.Load(common.config)
	if err != nil {
		return err
	}
	repo, err := git.Open(dir)
	if err != nil {
		return err
	}

	res, err := analyze.Run(repo, cfg, analyze.Options{
		Ref:                  ref,
		Force:                force,
		Version:              explicitV,
		IgnoreDeliverability: ignoreDeli,
	}, log)
	if err != nil {
		return err
	}

	for _, w := range res.Warnings {
		log.Warn(w)
	}

	env := output.BuildEnv(res)
	if outPath != "" {
		if err := output.WriteFile(outPath, env); err != nil {
			return err
		}
		log.Info("result written", "path", outPath, "status", string(res.Status))
	}

	// after_analysis hooks observe the decision; they cannot change it. `next`
	// stays read-only, and a hook that could rewrite the answer would make the
	// handshake with the build job meaningless.
	if hs := cfg.Hooks.For(hooks.AfterAnalysis); len(hs) > 0 {
		hctx := hooks.Context{
			Status:   string(res.Status),
			Version:  res.Version.String(),
			Tag:      res.Tag,
			Previous: res.Previous,
			Bump:     res.Bump.String(),
			Reason:   res.Reason,
			Commit:   res.Commit,
			Breaking: res.Decision.Breaking(),
		}
		if _, err := hooks.Run(context.Background(), repo.Dir(), hooks.AfterAnalysis, hs, hctx, log); err != nil {
			return err
		}
	}

	if common.json {
		if err := output.WriteJSON(os.Stdout, output.BuildSummary(res)); err != nil {
			return err
		}
	} else {
		fmt.Println(output.Human(res))
	}

	if failOnSkip {
		switch res.Status {
		case analyze.StatusNoBump:
			return &exitCodeError{code: exitNoBump, err: errSilent}
		case analyze.StatusNotDeliverable:
			return &exitCodeError{code: exitNotDeliverable, err: errSilent}
		}
	}
	return nil
}
