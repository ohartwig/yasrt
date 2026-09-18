// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package analyze_test

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/ohartwig/yasrt/internal/analyze"
	"github.com/ohartwig/yasrt/internal/config"
	"github.com/ohartwig/yasrt/internal/git"
	"github.com/ohartwig/yasrt/internal/semver"
	"github.com/ohartwig/yasrt/internal/testrepo"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func cfg(t *testing.T, src string) *config.Config {
	t.Helper()
	c, err := config.Parse(strings.NewReader(src), "test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func run(t *testing.T, tr *testrepo.Repo, c *config.Config, opts analyze.Options) *analyze.Result {
	t.Helper()
	r, err := git.Open(tr.Dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := analyze.Run(r, c, opts, discard)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return res
}

func runErr(t *testing.T, tr *testrepo.Repo, c *config.Config, opts analyze.Options) error {
	t.Helper()
	r, err := git.Open(tr.Dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = analyze.Run(r, c, opts, discard)
	if err == nil {
		t.Fatal("expected an error")
	}
	return err
}

func TestFirstReleaseUsesInitialVersion(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "package main", "feat: first thing")

	res := run(t, tr, cfg(t, "product: image\n"), analyze.Options{})
	if res.Status != analyze.StatusRelease {
		t.Fatalf("status = %s", res.Status)
	}
	if res.Version.String() != "1.0.0" {
		t.Errorf("version = %s, want the configured initial", res.Version)
	}
	if res.Previous != "" || res.HasPrevious {
		t.Errorf("there is no predecessor: %q", res.Previous)
	}
	if res.Tag != "1.0.0" {
		t.Errorf("tag = %q", res.Tag)
	}
}

func TestInitialVersionIsConfigurable(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "x", "feat: first")
	res := run(t, tr, cfg(t, "product: image\nversioning:\n  initial: \"0.1.0\"\n"), analyze.Options{})
	if res.Version.String() != "0.1.0" {
		t.Errorf("version = %s", res.Version)
	}
}

func TestBumpFromLastTag(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.2.3")
	tr.CommitFile("src/other.go", "2", "fix: a bug")

	res := run(t, tr, cfg(t, "product: image\n"), analyze.Options{})
	if res.Status != analyze.StatusRelease {
		t.Fatalf("status = %s", res.Status)
	}
	if res.Version.String() != "1.2.4" || res.Previous != "1.2.3" {
		t.Errorf("version=%s previous=%s", res.Version, res.Previous)
	}
	if res.Bump != semver.Patch || res.Reason != "fix" {
		t.Errorf("bump=%v reason=%q", res.Bump, res.Reason)
	}
}

func TestAlreadyReleased(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.0.0")

	res := run(t, tr, cfg(t, "product: image\n"), analyze.Options{})
	if res.Status != analyze.StatusAlreadyReleased {
		t.Fatalf("status = %s", res.Status)
	}
	if res.Version.String() != "1.0.0" {
		t.Errorf("version = %s", res.Version)
	}
}

func TestNoBump(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.0.0")
	tr.CommitFile("src/main.go", "2", "style: reformat")

	res := run(t, tr, cfg(t, "product: image\n"), analyze.Options{})
	if res.Status != analyze.StatusNoBump {
		t.Fatalf("status = %s", res.Status)
	}
	if res.Version != (semver.Version{}) || res.Tag != "" {
		t.Errorf("no-bump must leave version and tag empty: %+v", res)
	}
}

func TestNotDeliverable(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.0.0")
	tr.CommitFile("docs/guide.md", "d", "feat: document the thing")

	res := run(t, tr, cfg(t, "product: image\n"), analyze.Options{})
	if res.Status != analyze.StatusNotDeliverable {
		t.Fatalf("status = %s", res.Status)
	}
	if res.Version != (semver.Version{}) {
		t.Errorf("version must be empty: %s", res.Version)
	}
	if len(res.Delivery.Excluded) != 1 {
		t.Errorf("delivery = %+v", res.Delivery)
	}
}

// The I-081 case: a genuine fix bundled with a CI change must ship.
func TestFixBundledWithCIChangeStillReleasesForAPackage(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("v1.0.0")
	tr.Write("src/main.go", "2")
	tr.Write(".gitlab-ci.yml", "stages: [test]")
	tr.Git("add", "-A")
	tr.Git("commit", "-m", "fix: correct the thing and its pipeline")

	res := run(t, tr, cfg(t, "product: package\n"), analyze.Options{})
	if res.Status != analyze.StatusRelease {
		t.Fatalf("status = %s — a fix bundled with CI config must still ship", res.Status)
	}
	if res.Tag != "v1.0.1" {
		t.Errorf("tag = %q", res.Tag)
	}
}

// An image repository releases for a pipeline-only chore -- SPEC §5.1 makes
// the pipeline part of the deliverable -- but says so: the image is in all
// likelihood the one already published, and a bot should have typed the bump
// ci(deps). A package repository has no such release and no such warning.
func TestPipelineOnlyChoreWarnsForAnImage(t *testing.T) {
	rules := "rules:\n  - { type: chore, release: patch }\n  - { type: fix, release: patch }\n"
	tr := testrepo.New(t)
	tr.CommitFile("Containerfile", "FROM a", "feat: first")
	tr.Tag("1.0.0")
	tr.CommitFile(".gitlab-ci.yml", "include: [x]", "chore(deps): update container-scanning digest")

	res := run(t, tr, cfg(t, "product: image\n"+rules), analyze.Options{})
	if res.Status != analyze.StatusRelease || res.Tag != "1.0.1" {
		t.Fatalf("status = %s tag = %q: the pipeline is part of an image", res.Status, res.Tag)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "ci(deps)") {
		t.Errorf("warnings = %q, want the pipeline-only warning", res.Warnings)
	}

	// A fix beside the chore is a real release: no warning.
	tr.CommitFile("Containerfile", "FROM b", "fix: rebuild on the new base")
	res = run(t, tr, cfg(t, "product: image\n"+rules), analyze.Options{})
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %q, want none once the image itself changed", res.Warnings)
	}

	// The same commits in a package repository do not release at all.
	tr2 := testrepo.New(t)
	tr2.CommitFile("src/a.go", "1", "feat: first")
	tr2.Tag("v1.0.0")
	tr2.CommitFile(".gitlab-ci.yml", "include: [x]", "chore(deps): update container-scanning digest")
	res = run(t, tr2, cfg(t, "product: package\n"+rules), analyze.Options{})
	if res.Status != analyze.StatusNotDeliverable || len(res.Warnings) != 0 {
		t.Errorf("package: status = %s warnings = %q", res.Status, res.Warnings)
	}
}

// The deliverability window: a docs-only push after an unreleased feat must not
// bury the feat. The old component compared the push range and did exactly that.
func TestUnreleasedFeatSurvivesADocsOnlyPush(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.0.0")
	tr.CommitFile("src/feature.go", "f", "feat: real feature")
	tr.CommitFile("docs/guide.md", "d", "docs: describe it")

	res := run(t, tr, cfg(t, "product: image\n"), analyze.Options{})
	if res.Status != analyze.StatusRelease {
		t.Fatalf("status = %s — the unreleased feat is still unreleased", res.Status)
	}
	if res.Version.String() != "1.1.0" {
		t.Errorf("version = %s", res.Version)
	}
}

func TestTagFormatSelectsOwnTagsOnly(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("v1.0.0") // foreign for an image repo
	tr.CommitFile("src/b.go", "2", "fix: second")

	res := run(t, tr, cfg(t, "product: image\n"), analyze.Options{})
	if res.HasPrevious {
		t.Errorf("v-prefixed tag must not count for an image repo, got previous=%q", res.Previous)
	}
	if len(res.Warnings) == 0 {
		t.Error("a version-shaped tag outside tag_format deserves a warning")
	}
	if res.Version.String() != "1.0.0" {
		t.Errorf("version = %s, want the initial version", res.Version)
	}
}

func TestUnmergedTagIsNotAPredecessor(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.0.0")
	tr.Git("checkout", "-q", "-b", "side")
	tr.CommitFile("src/side.go", "s", "feat: side")
	tr.Tag("2.0.0")
	tr.Git("checkout", "-q", "main")
	tr.CommitFile("src/main2.go", "2", "fix: on main")

	res := run(t, tr, cfg(t, "product: image\n"), analyze.Options{})
	if res.Previous != "1.0.0" {
		t.Errorf("previous = %q, want 1.0.0", res.Previous)
	}
	if res.Version.String() != "1.0.1" {
		t.Errorf("version = %s", res.Version)
	}
}

func TestReleaseCommitDoesNotTriggerARelease(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.0.0")
	tr.CommitFile("CHANGELOG.md", "# Changelog\n", "chore(release): 1.0.0")

	res := run(t, tr, cfg(t, "product: image\n"), analyze.Options{})
	if res.Status == analyze.StatusRelease {
		t.Fatal("the release commit must never produce another release")
	}
}

func TestMajorOnZero(t *testing.T) {
	build := func(t *testing.T) *testrepo.Repo {
		tr := testrepo.New(t)
		tr.CommitFile("src/main.go", "1", "feat: first")
		tr.Tag("0.4.2")
		tr.CommitFile("src/b.go", "2", "feat!: breaking change")
		return tr
	}
	if got := run(t, build(t), cfg(t, "product: image\n"), analyze.Options{}).Version.String(); got != "1.0.0" {
		t.Errorf("major_on_zero true: %s", got)
	}
	if got := run(t, build(t), cfg(t, "product: image\nversioning:\n  major_on_zero: false\n"),
		analyze.Options{}).Version.String(); got != "0.5.0" {
		t.Errorf("major_on_zero false: %s", got)
	}
}

func TestForce(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.0.0")
	tr.CommitFile("src/b.go", "2", "style: nothing releasable")

	plain := run(t, tr, cfg(t, "product: image\n"), analyze.Options{})
	if plain.Status != analyze.StatusNoBump {
		t.Fatalf("precondition: %s", plain.Status)
	}

	forced := run(t, tr, cfg(t, "product: image\n"), analyze.Options{Force: "minor"})
	if forced.Status != analyze.StatusRelease {
		t.Fatalf("forced status = %s", forced.Status)
	}
	if forced.Version.String() != "1.1.0" || forced.Reason != analyze.ReasonForced {
		t.Errorf("version=%s reason=%q", forced.Version, forced.Reason)
	}
}

func TestForceDoesNotSkipDeliverability(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.0.0")
	tr.CommitFile("docs/g.md", "d", "docs: only docs")

	res := run(t, tr, cfg(t, "product: image\n"), analyze.Options{Force: "minor"})
	if res.Status != analyze.StatusNotDeliverable {
		t.Fatalf("status = %s — --force must not bypass deliverability", res.Status)
	}

	res = run(t, tr, cfg(t, "product: image\n"),
		analyze.Options{Force: "minor", IgnoreDeliverability: true})
	if res.Status != analyze.StatusRelease {
		t.Fatalf("--ignore-deliverability should release: %s", res.Status)
	}
}

func TestExplicitVersion(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.0.0")
	tr.CommitFile("src/b.go", "2", "fix: something")

	res := run(t, tr, cfg(t, "product: image\n"), analyze.Options{Version: "2.5.0"})
	if res.Version.String() != "2.5.0" || res.Reason != analyze.ReasonForced {
		t.Errorf("version=%s reason=%q", res.Version, res.Reason)
	}
	if res.Bump != semver.Major {
		t.Errorf("bump = %v, want major for a major move", res.Bump)
	}
}

func TestExplicitVersionMustBeHigher(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("2.0.0")
	tr.CommitFile("src/b.go", "2", "fix: something")

	err := runErr(t, tr, cfg(t, "product: image\n"), analyze.Options{Version: "1.0.0"})
	if !errors.Is(err, analyze.ErrVersionNotHigher) {
		t.Errorf("err = %v", err)
	}
}

func TestShallowCloneIsFatal(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("a.go", "1", "feat: one")
	tr.CommitFile("b.go", "2", "feat: two")
	shallow := tr.Clone(1)

	err := runErr(t, shallow, cfg(t, "product: image\n"), analyze.Options{})
	if !errors.Is(err, analyze.ErrShallow) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "GIT_DEPTH") {
		t.Errorf("the message must name the fix: %v", err)
	}
}

func TestRefOverride(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.0.0")
	target := tr.CommitFile("src/b.go", "2", "feat: second")
	tr.CommitFile("src/c.go", "3", "feat: third")

	res := run(t, tr, cfg(t, "product: image\n"), analyze.Options{Ref: target})
	if res.Commit != target {
		t.Errorf("commit = %s, want %s", res.Commit, target)
	}
	if len(res.Decision.Counted) != 1 {
		t.Errorf("analysing an earlier ref must not see later commits: %d", len(res.Decision.Counted))
	}
}

func TestSquashMergeWithoutConventionalTitle(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.0.0")
	tr.CommitFile("src/b.go", "2", "Merge branch 'feature/x' into main")

	res := run(t, tr, cfg(t, "product: image\n"), analyze.Options{})
	if res.Status != analyze.StatusNoBump {
		t.Errorf("status = %s", res.Status)
	}
	if len(res.Decision.NonConforming) != 1 {
		t.Errorf("the commit should be recorded as non-conforming: %+v", res.Decision.NonConforming)
	}
}

// Prereleases (plan.md P10). Two shapes are live in this estate: gsb11 cuts
// -rc from main and stable from `release`, moselwal-packages cuts stable from
// main and -rc from develop. Both are the same mechanism seen from either side.
func TestPrereleases(t *testing.T) {
	cfgSrc := "product: package\nversioning:\n  prereleases:\n    develop: rc\n"

	newRepo := func(t *testing.T) *testrepo.Repo {
		tr := testrepo.New(t)
		tr.CommitFile("src/main.go", "1", "feat: first")
		tr.Tag("v1.0.0")
		return tr
	}

	t.Run("first rc from a prerelease branch", func(t *testing.T) {
		tr := newRepo(t)
		tr.Git("checkout", "-q", "-b", "develop")
		tr.CommitFile("src/b.go", "2", "feat: something new")

		res := run(t, tr, cfg(t, cfgSrc), analyze.Options{Branch: "develop"})
		if res.Status != analyze.StatusRelease {
			t.Fatalf("status = %s", res.Status)
		}
		if res.Version.String() != "1.1.0-rc.1" {
			t.Errorf("version = %s, want 1.1.0-rc.1", res.Version)
		}
		if res.Tag != "v1.1.0-rc.1" {
			t.Errorf("tag = %q", res.Tag)
		}
		if res.Prerelease != "rc" {
			t.Errorf("prerelease = %q", res.Prerelease)
		}
	})

	t.Run("the counter increments while the core stays", func(t *testing.T) {
		tr := newRepo(t)
		tr.Git("checkout", "-q", "-b", "develop")
		tr.CommitFile("src/b.go", "2", "feat: something new")
		tr.Tag("v1.1.0-rc.1")
		tr.CommitFile("src/c.go", "3", "fix: a follow-up")

		res := run(t, tr, cfg(t, cfgSrc), analyze.Options{Branch: "develop"})
		if res.Version.String() != "1.1.0-rc.2" {
			t.Errorf("version = %s, want 1.1.0-rc.2", res.Version)
		}
		// rc.2 describes what is new since rc.1, not since the last stable.
		if len(res.Decision.Counted) != 1 {
			t.Errorf("notes should cover only the new commit, got %d", len(res.Decision.Counted))
		}
	})

	t.Run("a bigger bump moves the core and resets the counter", func(t *testing.T) {
		tr := newRepo(t)
		tr.Git("checkout", "-q", "-b", "develop")
		tr.CommitFile("src/b.go", "2", "feat: something new")
		tr.Tag("v1.1.0-rc.1")
		tr.CommitFile("src/c.go", "3", "feat!: breaking now")

		res := run(t, tr, cfg(t, cfgSrc), analyze.Options{Branch: "develop"})
		if res.Version.String() != "2.0.0-rc.1" {
			t.Errorf("version = %s, want 2.0.0-rc.1", res.Version)
		}
	})

	t.Run("the core accumulates everything since the last stable", func(t *testing.T) {
		tr := newRepo(t)
		tr.Git("checkout", "-q", "-b", "develop")
		// The feat is only in the rc.1 range; rc.2 sees a docs commit alone,
		// but the core must still be 1.1.0 rather than falling back to a patch.
		tr.CommitFile("src/b.go", "2", "feat: something new")
		tr.Tag("v1.1.0-rc.1")
		tr.CommitFile("src/c.go", "3", "fix: small")

		res := run(t, tr, cfg(t, cfgSrc), analyze.Options{Branch: "develop"})
		if res.Version.String() != "1.1.0-rc.2" {
			t.Errorf("version = %s — the minor from rc.1 must not be forgotten", res.Version)
		}
	})

	t.Run("stable branch ignores prerelease tags for the core", func(t *testing.T) {
		tr := newRepo(t)
		tr.CommitFile("src/b.go", "2", "feat: something new")
		tr.Tag("v1.1.0-rc.1")
		tr.CommitFile("src/c.go", "3", "fix: ready")

		res := run(t, tr, cfg(t, cfgSrc), analyze.Options{Branch: "main"})
		if res.Prerelease != "" {
			t.Errorf("main is not a prerelease branch, got %q", res.Prerelease)
		}
		if res.Version.String() != "1.1.0" {
			t.Errorf("version = %s, want the stable 1.1.0", res.Version)
		}
		if res.Previous != "v1.0.0" {
			t.Errorf("previous = %q — the stable baseline, not the rc", res.Previous)
		}
	})

	t.Run("gsb11 shape: rc on main, stable on release", func(t *testing.T) {
		src := "product: extension\nversioning:\n  prereleases:\n    main: rc\n"
		tr := newRepo(t)
		tr.CommitFile("src/b.go", "2", "feat: something new")

		onMain := run(t, tr, cfg(t, src), analyze.Options{Branch: "main"})
		if onMain.Version.String() != "1.1.0-rc.1" {
			t.Errorf("main = %s, want a prerelease", onMain.Version)
		}
		onRelease := run(t, tr, cfg(t, src), analyze.Options{Branch: "release"})
		if onRelease.Version.String() != "1.1.0" {
			t.Errorf("release = %s, want stable", onRelease.Version)
		}
	})

	t.Run("an unconfigured branch is stable", func(t *testing.T) {
		tr := newRepo(t)
		tr.CommitFile("src/b.go", "2", "feat: x")
		res := run(t, tr, cfg(t, cfgSrc), analyze.Options{Branch: "feature/anything"})
		if res.Prerelease != "" || res.Version.String() != "1.1.0" {
			t.Errorf("pre=%q version=%s", res.Prerelease, res.Version)
		}
	})

	t.Run("prerelease ordering holds against the stable release", func(t *testing.T) {
		tr := newRepo(t)
		tr.Git("checkout", "-q", "-b", "develop")
		tr.CommitFile("src/b.go", "2", "feat: x")
		tr.Tag("v1.1.0-rc.1")
		tr.Tag("v1.1.0")
		tr.CommitFile("src/c.go", "3", "fix: y")

		// 1.1.0 beats 1.1.0-rc.1, so the next rc builds on 1.1.0.
		res := run(t, tr, cfg(t, cfgSrc), analyze.Options{Branch: "develop"})
		if res.Version.String() != "1.1.1-rc.1" {
			t.Errorf("version = %s, want 1.1.1-rc.1", res.Version)
		}
	})
}

// Maintenance branches (semantic-release's 1.x / 1.2.x). The defining property
// is not that the branch is old but that a release cannot leave its range: a
// breaking change on 1.x ships as 1.y.0, never as 2.0.0.
func TestMaintenanceBranches(t *testing.T) {
	cfgSrc := "product: image\nversioning:\n  maintenance: [\"1.x\", \"1.2.x\"]\n"

	// A repository that has moved on to 2.x, with a 1.x line still alive.
	newRepo := func(t *testing.T) *testrepo.Repo {
		tr := testrepo.New(t)
		tr.CommitFile("src/main.go", "1", "feat: first")
		tr.Tag("1.2.3")
		tr.CommitFile("src/b.go", "2", "feat!: the next major")
		tr.Tag("2.0.0")
		return tr
	}

	t.Run("a fix on 1.x builds on the 1.x line, not on 2.0.0", func(t *testing.T) {
		tr := newRepo(t)
		tr.Git("checkout", "-q", "-b", "1.x", "1.2.3")
		tr.CommitFile("src/fix.go", "f", "fix: backported")

		res := run(t, tr, cfg(t, cfgSrc), analyze.Options{Branch: "1.x"})
		if res.Version.String() != "1.2.4" {
			t.Errorf("version = %s, want 1.2.4", res.Version)
		}
		if res.Previous != "1.2.3" {
			t.Errorf("previous = %q — 2.0.0 is history here, not a baseline", res.Previous)
		}
		if res.Maintenance != "1.x" {
			t.Errorf("maintenance = %q", res.Maintenance)
		}
	})

	t.Run("a feature on 1.x raises the minor", func(t *testing.T) {
		tr := newRepo(t)
		tr.Git("checkout", "-q", "-b", "1.x", "1.2.3")
		tr.CommitFile("src/f.go", "f", "feat: backported feature")

		res := run(t, tr, cfg(t, cfgSrc), analyze.Options{Branch: "1.x"})
		if res.Version.String() != "1.3.0" {
			t.Errorf("version = %s, want 1.3.0", res.Version)
		}
	})

	// The defining case.
	t.Run("a breaking change on 1.x cannot leave the range", func(t *testing.T) {
		tr := newRepo(t)
		tr.Git("checkout", "-q", "-b", "1.x", "1.2.3")
		tr.CommitFile("src/b.go", "b", "feat!: breaking, but on a maintenance branch")

		res := run(t, tr, cfg(t, cfgSrc), analyze.Options{Branch: "1.x"})
		if res.Version.String() != "1.3.0" {
			t.Errorf("version = %s, want 1.3.0 — a maintenance branch may not reach 2.0.0", res.Version)
		}
	})

	t.Run("1.2.x is pinned to its minor", func(t *testing.T) {
		tr := newRepo(t)
		tr.Git("checkout", "-q", "-b", "1.2.x", "1.2.3")
		tr.CommitFile("src/f.go", "f", "feat: would normally raise the minor")

		res := run(t, tr, cfg(t, cfgSrc), analyze.Options{Branch: "1.2.x"})
		if res.Version.String() != "1.2.4" {
			t.Errorf("version = %s, want 1.2.4 — 1.2.x may not reach 1.3.0", res.Version)
		}
	})

	t.Run("the default branch is unaffected", func(t *testing.T) {
		tr := newRepo(t)
		tr.CommitFile("src/c.go", "c", "feat: on main")

		res := run(t, tr, cfg(t, cfgSrc), analyze.Options{Branch: "main"})
		if res.Maintenance != "" {
			t.Errorf("maintenance = %q on main", res.Maintenance)
		}
		if res.Version.String() != "2.1.0" {
			t.Errorf("version = %s — main still builds on 2.0.0", res.Version)
		}
	})

	t.Run("an unconfigured branch named like a range is not one", func(t *testing.T) {
		tr := newRepo(t)
		tr.Git("checkout", "-q", "-b", "3.x", "1.2.3")
		tr.CommitFile("src/f.go", "f", "feat!: breaking")

		// 3.x is not listed in versioning.maintenance, so it is an ordinary
		// branch: nothing is capped and nothing is restricted.
		res := run(t, tr, cfg(t, cfgSrc), analyze.Options{Branch: "3.x"})
		if res.Maintenance != "" {
			t.Errorf("maintenance = %q", res.Maintenance)
		}
		if res.Version.String() != "2.0.0" {
			t.Errorf("version = %s", res.Version)
		}
	})
}

// Switching tag_format must not restart the version series: the old tags are
// still releases, only new tags take the new shape.
func TestTagFormatChangeKeepsTheHistory(t *testing.T) {
	tr := testrepo.New(t)
	tr.CommitFile("src/main.go", "1", "feat: first")
	tr.Tag("1.6.1")
	tr.CommitFile("src/b.go", "2", "feat: after the switch")

	res := run(t, tr, cfg(t, "product: custom\ntag_format: \"v${version}\"\ndeliverability:\n  non_release_paths: [docs/**]\nversioning:\n  previous_tag_formats: [\"${version}\"]\n"), analyze.Options{})
	if res.Status != analyze.StatusRelease || res.Version.String() != "1.7.0" || res.Tag != "v1.7.0" || res.Previous != "1.6.1" {
		t.Errorf("got status=%s version=%s tag=%s previous=%s; want release 1.7.0 v1.7.0 from 1.6.1", res.Status, res.Version, res.Tag, res.Previous)
	}

	// Without the previous format the history is invisible and the series restarts.
	res = run(t, tr, cfg(t, "product: custom\ntag_format: \"v${version}\"\ndeliverability:\n  non_release_paths: [docs/**]\n"), analyze.Options{})
	if res.Version.String() != "1.0.0" {
		t.Errorf("without previous_tag_formats the old tag must be foreign; got %s", res.Version)
	}
}
