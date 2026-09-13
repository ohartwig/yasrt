// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/ohartwig/yasrt/internal/semver"
)

func load(t *testing.T, yaml string) *Config {
	t.Helper()
	c, err := Parse(strings.NewReader(yaml), "test.yaml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return c
}

func loadErr(t *testing.T, yaml string) error {
	t.Helper()
	_, err := Parse(strings.NewReader(yaml), "test.yaml")
	if err == nil {
		t.Fatal("expected an error")
	}
	return err
}

func TestMinimalConfigGetsDefaults(t *testing.T) {
	c := load(t, "product: image\n")
	if c.Version != 1 {
		t.Errorf("version = %d", c.Version)
	}
	if got := c.TagFormatString(); got != "${version}" {
		t.Errorf("tag_format = %q", got)
	}
	if c.Versioning.Initial != "1.0.0" || !c.MajorOnZero() {
		t.Errorf("versioning = %+v", c.Versioning)
	}
	if !c.ReleaseCommitEnabled() || !c.GitLabReleaseEnabled() {
		t.Error("release commit and gitlab release should default on")
	}
	if c.ReleaseCommit.Sign != SignAuto {
		t.Errorf("sign = %q", c.ReleaseCommit.Sign)
	}
	// Subject, blank line, notes — the shape the npm preset produces, so a
	// migrated repository's history keeps the same form.
	if c.ReleaseCommit.Message != "chore(release): ${version}\n\n${notes}" {
		t.Errorf("message = %q", c.ReleaseCommit.Message)
	}
	if len(c.Rules) != len(DefaultRules()) {
		t.Errorf("rules = %+v", c.Rules)
	}
	if len(c.Changelog.Sections) != 9 || c.Changelog.File != "CHANGELOG.md" {
		t.Errorf("changelog = %+v", c.Changelog)
	}
}

// Decision D5: the tag format follows from what the repository ships.
func TestTagFormatDerivedFromProduct(t *testing.T) {
	for _, tc := range []struct{ product, want string }{
		{"image", "${version}"},
		{"custom", "${version}"},
		{"package", "v${version}"},
		{"extension", "v${version}"},
	} {
		src := "product: " + tc.product + "\n"
		if tc.product == "custom" {
			src += "deliverability:\n  non_release_paths: [docs/**]\n"
		}
		c := load(t, src)
		if got := c.TagFormatString(); got != tc.want {
			t.Errorf("product %s: tag_format = %q, want %q", tc.product, got, tc.want)
		}
	}
}

func TestExplicitTagFormatWins(t *testing.T) {
	c := load(t, "product: package\ntag_format: \"${version}\"\n")
	if got := c.TagFormatString(); got != "${version}" {
		t.Errorf("tag_format = %q", got)
	}
}

// SPEC §5.1, including the asymmetry that .gitlab-ci.yml is part of an image's
// deliverable but not a package's.
func TestNonReleasePathsDerivation(t *testing.T) {
	img := load(t, "product: image\n").NonReleasePaths()
	if slices.Contains(img, ".gitlab-ci.yml") {
		t.Error("image: .gitlab-ci.yml builds the deliverable and must stay releasable")
	}
	if slices.Contains(img, "lefthook.yml") || slices.Contains(img, ".pre-commit-config.yaml") {
		t.Errorf("image: tooling files of one organisation are not a default, got %q", img)
	}

	pkg := load(t, "product: package\n").NonReleasePaths()
	if !slices.Contains(pkg, ".gitlab-ci.yml") || !slices.Contains(pkg, ".gitlab/**") {
		t.Errorf("package: %q", pkg)
	}
}

func TestNonReleasePathsOverrideAndExtend(t *testing.T) {
	c := load(t, "product: image\ndeliverability:\n  non_release_paths: [only/**]\n  extra_non_release_paths: [more/**]\n")
	got := c.NonReleasePaths()
	want := []string{"only/**", "more/**"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q — an explicit list replaces the derived one", got, want)
	}

	c2 := load(t, "product: image\ndeliverability:\n  extra_non_release_paths: [more/**]\n")
	got2 := c2.NonReleasePaths()
	if !slices.Contains(got2, "CHANGELOG.md") || !slices.Contains(got2, "more/**") {
		t.Errorf("extras alone must extend the derived list, got %q", got2)
	}
}

// Loop guard 2 (SPEC §6.3): the release scope is enforced, not merely defaulted.
func TestReleaseScopeAlwaysIgnored(t *testing.T) {
	for _, src := range []string{
		"product: image\n",
		"product: image\nignore:\n  scopes: [deps]\n",
		"product: image\nignore:\n  scopes: []\n",
	} {
		c := load(t, src)
		if !slices.Contains(c.Ignore.Scopes, "release") {
			t.Errorf("%q: release scope must be present, got %q", src, c.Ignore.Scopes)
		}
	}
	// Already present in another case: not duplicated.
	c := load(t, "product: image\nignore:\n  scopes: [Release]\n")
	if len(c.Ignore.Scopes) != 1 {
		t.Errorf("scopes = %q, want no duplicate", c.Ignore.Scopes)
	}
}

func TestValidation(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"missing product", "version: 1\n", "product: required"},
		{"unknown product", "product: chart\n", "not one of"},
		{"custom without paths", "product: custom\n", "non_release_paths is required"},
		{"bad tag format", "product: image\ntag_format: \"release\"\n", "must contain"},
		{"bad initial", "product: image\nversioning:\n  initial: \"1.0\"\n", "versioning.initial"},
		{"bad release type", "product: image\nrules:\n  - type: feat\n    release: huge\n", "rules[0].release"},
		{"empty rule", "product: image\nrules:\n  - release: minor\n", "matches nothing"},
		{"bad sign", "product: image\nrelease_commit:\n  sign: maybe\n", "release_commit.sign"},
		{"trigger without ref", "product: image\nafter_release:\n  triggers:\n    - project: a/b\n", "ref: required"},
		{"unsupported version", "version: 2\nproduct: image\n", "not supported"},
		{"previous tag format without placeholder", "product: image\nversioning:\n  previous_tag_formats: [\"release-\"]\n", "previous_tag_formats[0]"},
		{"asset with url and path", "product: image\ngitlab_release:\n  assets:\n    - name: x\n      url: https://a\n      path: dist/x\n", "not both"},
		{"asset with neither", "product: image\ngitlab_release:\n  assets:\n    - name: x\n", "url or path is required"},
		{"link without name", "product: image\ngitlab_release:\n  assets:\n    - url: https://a\n", "name: required"},
		{"absolute upload path", "product: image\ngitlab_release:\n  assets:\n    - path: /etc/passwd\n", "relative to the repository"},
		{"on_failure hook without run", "product: image\nhooks:\n  on_failure:\n    - name: x\n", "hooks.on_failure[0].run"},
		{"unknown key", "product: image\nprodukt: image\n", "field produkt not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := loadErr(t, tc.src)
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestTagRoundTrip(t *testing.T) {
	v := semver.Version{Major: 2, Minor: 5, Patch: 1}

	bare := load(t, "product: image\n")
	if got := bare.Tag(v); got != "2.5.1" {
		t.Errorf("bare tag = %q", got)
	}
	prefixed := load(t, "product: package\n")
	if got := prefixed.Tag(v); got != "v2.5.1" {
		t.Errorf("prefixed tag = %q", got)
	}

	for _, tc := range []struct {
		cfg, tag string
		wantOK   bool
		want     string
	}{
		{"product: image\n", "2.5.1", true, "2.5.1"},
		{"product: image\n", "v2.5.1", false, ""},
		{"product: package\n", "v2.5.1", true, "2.5.1"},
		{"product: package\n", "2.5.1", false, ""},
		{"product: package\n", "v2.5", false, ""},
		{"product: image\n", "nightly", false, ""},
	} {
		c := load(t, tc.cfg)
		got, ok := c.VersionFromTag(tc.tag)
		if ok != tc.wantOK {
			t.Errorf("VersionFromTag(%q) ok = %v, want %v", tc.tag, ok, tc.wantOK)
			continue
		}
		if ok && got.String() != tc.want {
			t.Errorf("VersionFromTag(%q) = %v, want %v", tc.tag, got, tc.want)
		}
	}
}

func TestFullSpecExample(t *testing.T) {
	// The example from SPEC §5 must load cleanly, or the specification is lying.
	c := load(t, `
version: 1
product: image
tag_format: "${version}"
versioning:
  initial: "1.0.0"
  major_on_zero: true
rules:
  - breaking: true
    release: major
  - type: feat
    release: minor
  - type: fix
    release: patch
ignore:
  scopes: [release]
  authors: []
  trailers: ["skip release", "release skip"]
deliverability:
  non_release_paths: []
  extra_non_release_paths: []
changelog:
  file: CHANGELOG.md
  sections:
    - { type: feat, title: ":sparkles: Features" }
release_commit:
  enabled: true
  message: "chore(release): ${version}"
  assets: [CHANGELOG.md]
  sign: auto
  author: "release-bot <release-bot@example.invalid>"
gitlab_release:
  enabled: true
  name: "${version}"
  assets: []
after_release:
  triggers:
    - project: "devops/renovate-runner"
      ref: main
      variables: { FAST_LANE: "true" }
`)
	if c.AfterRelease.Triggers[0].Variables["FAST_LANE"] != "true" {
		t.Errorf("triggers = %+v", c.AfterRelease.Triggers)
	}
}

// ${tag} and ${version} differ wherever tag_format carries a prefix, and the
// estate's renovate trigger sends the tag. Expanding only ${version} made that
// contract inexpressible.
func TestExpandVersionAndTag(t *testing.T) {
	v := semver.Version{Major: 2, Minor: 5, Patch: 1}
	for _, tc := range []struct{ tmpl, tag, want string }{
		{"chore(release): ${version}", "v2.5.1", "chore(release): 2.5.1"},
		{"${tag}", "v2.5.1", "v2.5.1"},
		{"${version} == ${tag}", "2.5.1", "2.5.1 == 2.5.1"},
		{"release ${version} (tag ${tag})", "v2.5.1", "release 2.5.1 (tag v2.5.1)"},
		{"nothing to expand", "v2.5.1", "nothing to expand"},
	} {
		if got := Expand(tc.tmpl, v, tc.tag); got != tc.want {
			t.Errorf("Expand(%q, tag=%q) = %q, want %q", tc.tmpl, tc.tag, got, tc.want)
		}
	}
}

// A release commit is made by automation; defaulting to the person who merged
// attributes a bot's work to a human.
func TestReleaseAuthorDefaultsToTheBotAndStaysOverridable(t *testing.T) {
	if got := load(t, "product: image\n").ReleaseCommit.Author; got != DefaultReleaseAuthor {
		t.Errorf("author = %q, want %q", got, DefaultReleaseAuthor)
	}
	c := load(t, "product: image\nrelease_commit:\n  author: \"Someone Else <s@example.invalid>\"\n")
	if got := c.ReleaseCommit.Author; got != "Someone Else <s@example.invalid>" {
		t.Errorf("author = %q — it must stay configurable", got)
	}
}

// A repository that changes its tag format keeps its history: old tags are
// still releases, new tags take the new shape.
func TestPreviousTagFormats(t *testing.T) {
	cfg := load(t, "product: custom\ntag_format: \"v${version}\"\ndeliverability:\n  non_release_paths: [docs/**]\nversioning:\n  previous_tag_formats: [\"${version}\", \"release-${version}\"]\n")
	for tag, want := range map[string]string{"v1.2.3": "1.2.3", "1.2.3": "1.2.3", "release-0.9.0": "0.9.0"} {
		v, ok := cfg.VersionFromTag(tag)
		if !ok || v.String() != want {
			t.Errorf("VersionFromTag(%q) = %v, %v; want %s", tag, v, ok, want)
		}
	}
	if _, ok := cfg.VersionFromTag("build-1.2.3"); ok {
		t.Error("a format that was never configured must not match")
	}
	if got := cfg.Tag(semver.Version{Major: 2}); got != "v2.0.0" {
		t.Errorf("new tags use the current format, got %q", got)
	}
}
