// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package analyze_test

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"git.ole-hartwig.eu/yasrt/cli/internal/analyze"
	"git.ole-hartwig.eu/yasrt/cli/internal/config"
	"git.ole-hartwig.eu/yasrt/cli/internal/git"
	"git.ole-hartwig.eu/yasrt/cli/internal/semver"
	"git.ole-hartwig.eu/yasrt/cli/internal/testrepo"
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
