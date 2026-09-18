// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The case the estate actually has: ninety repositories with no configuration
// of their own, and a component that knows what they build.
func TestDefaultsAloneAreEnough(t *testing.T) {
	dir := t.TempDir()
	base := write(t, dir, "defaults.yaml", "product: extension\n")

	c, err := LoadLayered([]string{base}, filepath.Join(dir, ".yasrt.yaml"))
	if err != nil {
		t.Fatalf("a repository whose component supplies product needs no file: %v", err)
	}
	if c.Product != ProductExtension {
		t.Errorf("product = %q", c.Product)
	}
	// Derivations still follow from the merged product.
	if c.TagFormatString() != "v${version}" {
		t.Errorf("tag_format = %q", c.TagFormatString())
	}
}

func TestNeitherDefaultsNorFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadLayered(nil, filepath.Join(dir, ".yasrt.yaml"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no defaults given") {
		t.Errorf("the message should say what is missing: %v", err)
	}
}

func TestRepositoryOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	base := write(t, dir, "defaults.yaml", `
product: package
versioning:
  initial: "0.1.0"
  major_on_zero: false
release_commit:
  enabled: true
  sign: auto
`)
	repo := write(t, dir, ".yasrt.yaml", `
tag_format: "${version}"
versioning:
  major_on_zero: true
release_commit:
  sign: required
`)
	c, err := LoadLayered([]string{base}, repo)
	if err != nil {
		t.Fatal(err)
	}
	// Overridden by the repository.
	if c.TagFormatString() != "${version}" {
		t.Errorf("tag_format = %q", c.TagFormatString())
	}
	if !c.MajorOnZero() {
		t.Error("major_on_zero should be overridden to true")
	}
	if c.ReleaseCommit.Sign != SignRequired {
		t.Errorf("sign = %q", c.ReleaseCommit.Sign)
	}
	// Inherited from the defaults, untouched by the repository.
	if c.Product != ProductPackage {
		t.Errorf("product = %q", c.Product)
	}
	if c.Versioning.Initial != "0.1.0" {
		t.Errorf("initial = %q — a sibling key must survive a partial override", c.Versioning.Initial)
	}
	if !c.ReleaseCommitEnabled() {
		t.Error("release_commit.enabled should have been inherited")
	}
}

func TestLaterDefaultsWinOverEarlierOnes(t *testing.T) {
	dir := t.TempDir()
	a := write(t, dir, "a.yaml", "product: image\nversioning:\n  initial: \"1.0.0\"\n")
	b := write(t, dir, "b.yaml", "versioning:\n  initial: \"2.0.0\"\n")
	c, err := LoadLayered([]string{a, b}, filepath.Join(dir, "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Versioning.Initial != "2.0.0" {
		t.Errorf("initial = %q", c.Versioning.Initial)
	}
}

// Lists replace rather than append, because with `rules` the order decides the
// outcome and a silently extended list would change which rule matches first.
func TestListsReplaceRatherThanAppend(t *testing.T) {
	dir := t.TempDir()
	base := write(t, dir, "defaults.yaml", `
product: image
rules:
  - type: feat
    release: minor
  - type: fix
    release: patch
`)
	repo := write(t, dir, ".yasrt.yaml", `
rules:
  - type: chore
    release: patch
`)
	c, err := LoadLayered([]string{base}, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Rules) != 1 || c.Rules[0].Type != "chore" {
		t.Errorf("rules = %+v — a repository that states rules states all of them", c.Rules)
	}
}

// The explicit extension point still works across layers.
func TestExtraPathsExtendAcrossLayers(t *testing.T) {
	dir := t.TempDir()
	base := write(t, dir, "defaults.yaml", "product: image\n")
	repo := write(t, dir, ".yasrt.yaml", "deliverability:\n  extra_non_release_paths: [\"adr/**\"]\n")
	c, err := LoadLayered([]string{base}, repo)
	if err != nil {
		t.Fatal(err)
	}
	paths := c.NonReleasePaths()
	if !slices.Contains(paths, "CHANGELOG.md") || !slices.Contains(paths, "adr/**") {
		t.Errorf("paths = %q — extras must extend the derived list", paths)
	}
}

func TestMapsMergeKeyByKey(t *testing.T) {
	dir := t.TempDir()
	base := write(t, dir, "defaults.yaml", `
product: extension
versioning:
  prereleases:
    develop: rc
`)
	repo := write(t, dir, ".yasrt.yaml", `
versioning:
  prereleases:
    main: beta
`)
	c, err := LoadLayered([]string{base}, repo)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := c.PrereleaseFor("develop"); !ok || id != "rc" {
		t.Errorf("develop = %q %v — inherited entries must survive", id, ok)
	}
	if id, ok := c.PrereleaseFor("main"); !ok || id != "beta" {
		t.Errorf("main = %q %v", id, ok)
	}
}

// A typo must be reported against the file it is in, not against the merge.
func TestUnknownFieldNamesItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	base := write(t, dir, "defaults.yaml", "product: image\nprodukt: image\n")
	repo := write(t, dir, ".yasrt.yaml", "tag_format: \"${version}\"\n")

	_, err := LoadLayered([]string{base}, repo)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "defaults.yaml") {
		t.Errorf("error should name defaults.yaml: %v", err)
	}
	if !strings.Contains(err.Error(), "produkt") {
		t.Errorf("error should name the field: %v", err)
	}
}

func TestMissingDefaultsFileIsReported(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadLayered([]string{filepath.Join(dir, "nope.yaml")}, filepath.Join(dir, ".yasrt.yaml"))
	if err == nil || !strings.Contains(err.Error(), "nope.yaml") {
		t.Errorf("err = %v", err)
	}
}

// Validation runs on the merged result: defaults that are incomplete on their
// own are fine as long as the repository completes them.
func TestValidationAppliesToTheMergedResult(t *testing.T) {
	dir := t.TempDir()
	base := write(t, dir, "defaults.yaml", "product: custom\n")
	// custom with no non_release_paths is invalid on its own …
	if _, err := LoadLayered([]string{base}, filepath.Join(dir, "absent.yaml")); err == nil {
		t.Fatal("custom without non_release_paths should fail")
	}
	// … and valid once the repository supplies them.
	repo := write(t, dir, ".yasrt.yaml", "deliverability:\n  non_release_paths: [\"docs/**\"]\n")
	if _, err := LoadLayered([]string{base}, repo); err != nil {
		t.Errorf("should be valid once completed: %v", err)
	}
}

// The single-file path must keep behaving exactly as before.
func TestSingleFileStillWorks(t *testing.T) {
	dir := t.TempDir()
	repo := write(t, dir, ".yasrt.yaml", "product: image\n")
	c, err := LoadLayered(nil, repo)
	if err != nil {
		t.Fatal(err)
	}
	if c.Product != ProductImage {
		t.Errorf("product = %q", c.Product)
	}
}

// The CI component always writes the defaults file, even when it has nothing
// to say, so an empty layer must merge to nothing rather than fail.
func TestEmptyLayerIsHarmless(t *testing.T) {
	dir := t.TempDir()
	empty := write(t, dir, "defaults.yaml", "")
	repo := write(t, dir, ".yasrt.yaml", "product: image\n")

	c, err := LoadLayered([]string{empty}, repo)
	if err != nil {
		t.Fatalf("an empty defaults layer must merge to nothing: %v", err)
	}
	if c.Product != ProductImage {
		t.Errorf("product = %q", c.Product)
	}
}

// And with nothing anywhere, the error names what is missing rather than
// complaining about the empty file.
func TestEmptyLayerAndNoRepoFileReportsTheMissingProduct(t *testing.T) {
	dir := t.TempDir()
	empty := write(t, dir, "defaults.yaml", "")

	_, err := LoadLayered([]string{empty}, filepath.Join(dir, ".yasrt.yaml"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "product") {
		t.Errorf("the message should name product: %v", err)
	}
}

// A comment-only defaults file is the same case with a different shape.
func TestCommentOnlyLayerIsHarmless(t *testing.T) {
	dir := t.TempDir()
	c1 := write(t, dir, "defaults.yaml", "# nothing to configure here\n")
	repo := write(t, dir, ".yasrt.yaml", "product: package\n")
	if _, err := LoadLayered([]string{c1}, repo); err != nil {
		t.Fatalf("a comment-only layer must merge to nothing: %v", err)
	}
}
