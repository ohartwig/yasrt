// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package config loads and validates .yasrt.yaml.
//
// Every field is optional except product, because product is what the rest of
// the defaults are derived from: what a repository ships decides which paths
// cannot possibly be a release, and which tag format its consumers expect.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"git.ole-hartwig.eu/yasrt/cli/internal/hooks"
	"git.ole-hartwig.eu/yasrt/cli/internal/semver"
	yaml "go.yaml.in/yaml/v3"
)

// Product is what the repository produces. It drives two derivations:
// non_release_paths and tag_format.
type Product string

const (
	ProductImage     Product = "image"
	ProductPackage   Product = "package"
	ProductExtension Product = "extension"
	ProductCustom    Product = "custom"
)

// ReleaseScope is the commit scope yasrt uses for its own release commit. It is
// always ignored during analysis and cannot be configured away: it is the
// second of the three loop guards (SPEC §6.3).
const ReleaseScope = "release"

// DefaultReleaseAuthor is the identity release commits are made under when a
// repository does not name its own. It continues the identity the npm
// component used, so history stays attributable across the migration.
const DefaultReleaseAuthor = "KOH Release Bot <release-bot@ole-hartwig.eu>"

// Placeholders substituted in tag_format and in the message templates.
const (
	VersionPlaceholder = "${version}"
	// TagPlaceholder matters wherever the two differ: with a v-prefixed
	// tag_format, "2.5.1" and "v2.5.1" are not interchangeable, and the
	// estate's renovate trigger sends the tag, not the version.
	TagPlaceholder = "${tag}"
)

type Config struct {
	Version        int            `yaml:"version"`
	Product        Product        `yaml:"product"`
	TagFormat      *string        `yaml:"tag_format"`
	Versioning     Versioning     `yaml:"versioning"`
	Rules          []Rule         `yaml:"rules"`
	Ignore         Ignore         `yaml:"ignore"`
	Deliverability Deliverability `yaml:"deliverability"`
	Changelog      Changelog      `yaml:"changelog"`
	ReleaseCommit  ReleaseCommit  `yaml:"release_commit"`
	GitLabRelease  GitLabRelease  `yaml:"gitlab_release"`
	AfterRelease   AfterRelease   `yaml:"after_release"`
	Hooks          Hooks          `yaml:"hooks"`
}

// Hooks are yasrt's extension mechanism: external programs run at defined
// points. See internal/hooks for why they are out-of-process.
type Hooks struct {
	AfterAnalysis []hooks.Hook `yaml:"after_analysis"`
	BeforeTag     []hooks.Hook `yaml:"before_tag"`
	AfterTag      []hooks.Hook `yaml:"after_tag"`
	AfterRelease  []hooks.Hook `yaml:"after_release"`
}

// For returns the hooks configured for one event.
func (h Hooks) For(e hooks.Event) []hooks.Hook {
	switch e {
	case hooks.AfterAnalysis:
		return h.AfterAnalysis
	case hooks.BeforeTag:
		return h.BeforeTag
	case hooks.AfterTag:
		return h.AfterTag
	case hooks.AfterRelease:
		return h.AfterRelease
	}
	return nil
}

type Versioning struct {
	Initial     string `yaml:"initial"`
	MajorOnZero *bool  `yaml:"major_on_zero"`
}

// Rule maps a commit to a release size. The first rule that matches a commit
// wins for that commit; the highest bump across all commits wins overall.
type Rule struct {
	Type     string `yaml:"type"`
	Scope    string `yaml:"scope"`
	Breaking *bool  `yaml:"breaking"`
	Release  string `yaml:"release"`
}

type Ignore struct {
	Scopes   []string `yaml:"scopes"`
	Authors  []string `yaml:"authors"`
	Trailers []string `yaml:"trailers"`
}

type Deliverability struct {
	// NonReleasePaths replaces the derived list entirely.
	NonReleasePaths []string `yaml:"non_release_paths"`
	// ExtraNonReleasePaths extends it.
	ExtraNonReleasePaths []string `yaml:"extra_non_release_paths"`
}

type Changelog struct {
	File     string    `yaml:"file"`
	Sections []Section `yaml:"sections"`
}

type Section struct {
	Type  string `yaml:"type"`
	Title string `yaml:"title"`
}

type ReleaseCommit struct {
	Enabled *bool    `yaml:"enabled"`
	Message string   `yaml:"message"`
	Assets  []string `yaml:"assets"`
	Sign    SignMode `yaml:"sign"`
	Author  string   `yaml:"author"`
}

// SignMode controls commit signing. auto degrades to unsigned when no key is
// present; required fails before anything is pushed.
type SignMode string

const (
	SignAuto     SignMode = "auto"
	SignRequired SignMode = "required"
	SignOff      SignMode = "off"
)

type GitLabRelease struct {
	Enabled *bool         `yaml:"enabled"`
	Name    string        `yaml:"name"`
	Assets  []ReleaseLink `yaml:"assets"`
}

type ReleaseLink struct {
	Name     string `yaml:"name"`
	URL      string `yaml:"url"`
	LinkType string `yaml:"link_type"`
}

type AfterRelease struct {
	Triggers []Trigger `yaml:"triggers"`
}

type Trigger struct {
	Project   string            `yaml:"project"`
	Ref       string            `yaml:"ref"`
	Variables map[string]string `yaml:"variables"`
}

// DefaultRules is the rule set from SPEC §5. Anything not listed produces no
// release, which is why build and revert are absent: both are allowed commit
// types in this estate but neither ships anything.
func DefaultRules() []Rule {
	yes := true
	return []Rule{
		{Breaking: &yes, Release: "major"},
		{Type: "feat", Release: "minor"},
		{Type: "fix", Release: "patch"},
		{Type: "perf", Release: "patch"},
		{Type: "chore", Release: "patch"},
	}
}

// DefaultSections is the fixed changelog order from SPEC §5. The estate's npm
// preset used :repeat: for both ci and chore; :wrench: disambiguates chores.
func DefaultSections() []Section {
	return []Section{
		{Type: "feat", Title: ":sparkles: Features"},
		{Type: "fix", Title: ":bug: Fixes"},
		{Type: "docs", Title: ":memo: Documentation"},
		{Type: "style", Title: ":barber: Styles"},
		{Type: "refactor", Title: ":zap: Refactor"},
		{Type: "perf", Title: ":fast_forward: Performance"},
		{Type: "test", Title: ":white_check_mark: Tests"},
		{Type: "ci", Title: ":repeat: Continuous Integrations"},
		{Type: "chore", Title: ":wrench: Chores"},
	}
}

// derivedNonReleasePaths implements SPEC §5.1. Note the asymmetry: for an
// image, .gitlab-ci.yml builds the deliverable and therefore is one; for a
// package it is not. The estate uses lefthook, never .pre-commit-config.yaml.
func derivedNonReleasePaths(p Product) []string {
	switch p {
	case ProductImage:
		return []string{"CHANGELOG.md", "README.md", "docs/**", "lefthook.yml"}
	case ProductPackage, ProductExtension:
		return []string{
			"CHANGELOG.md", "README.md", "docs/**",
			".gitlab-ci.yml", "lefthook.yml", ".gitlab/**",
		}
	}
	return nil
}

// derivedTagFormat implements decision D5: tooling and images tag bare
// (1.16.12), publishable packages tag with a v prefix (v2.5.1). Both shapes are
// live in this estate today, carried by the old component's tag-format input.
func derivedTagFormat(p Product) string {
	switch p {
	case ProductPackage, ProductExtension:
		return "v" + VersionPlaceholder
	}
	return VersionPlaceholder
}

// Load reads a config file. A missing file is an error: product cannot be
// guessed, and guessing it wrong silently releases the wrong thing.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s not found: every repository needs one, and it must set `product`", path)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return parse(f, path)
}

func parse(r io.Reader, path string) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true) // a typo'd key is a mistake, not something to ignore
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Parse exposes decoding for tests and for `yasrt check` on stdin.
func Parse(r io.Reader, name string) (*Config, error) { return parse(r, name) }

func (c *Config) applyDefaults() {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.TagFormat == nil {
		f := derivedTagFormat(c.Product)
		c.TagFormat = &f
	}
	if c.Versioning.Initial == "" {
		c.Versioning.Initial = "1.0.0"
	}
	if c.Versioning.MajorOnZero == nil {
		t := true
		c.Versioning.MajorOnZero = &t
	}
	if c.Rules == nil {
		c.Rules = DefaultRules()
	}
	// Loop guard 2: our own chore(release) commit must never produce a bump.
	if !slices.ContainsFunc(c.Ignore.Scopes, func(s string) bool {
		return strings.EqualFold(s, ReleaseScope)
	}) {
		c.Ignore.Scopes = append(c.Ignore.Scopes, ReleaseScope)
	}
	if c.Ignore.Trailers == nil {
		c.Ignore.Trailers = []string{"skip release", "release skip"}
	}
	if c.Changelog.File == "" {
		c.Changelog.File = "CHANGELOG.md"
	}
	if c.Changelog.Sections == nil {
		c.Changelog.Sections = DefaultSections()
	}
	if c.ReleaseCommit.Enabled == nil {
		t := true // decision D4
		c.ReleaseCommit.Enabled = &t
	}
	if c.ReleaseCommit.Message == "" {
		c.ReleaseCommit.Message = "chore(" + ReleaseScope + "): " + VersionPlaceholder
	}
	if c.ReleaseCommit.Assets == nil {
		c.ReleaseCommit.Assets = []string{c.Changelog.File}
	}
	if c.ReleaseCommit.Sign == "" {
		c.ReleaseCommit.Sign = SignAuto
	}
	if c.ReleaseCommit.Author == "" {
		// A release commit is made by automation, not by whoever happened to
		// merge. Overridable per repository; this only stops the default from
		// being a person.
		c.ReleaseCommit.Author = DefaultReleaseAuthor
	}
	if c.GitLabRelease.Enabled == nil {
		t := true
		c.GitLabRelease.Enabled = &t
	}
	if c.GitLabRelease.Name == "" {
		c.GitLabRelease.Name = VersionPlaceholder
	}
}

func (c *Config) validate() error {
	var errs []error

	switch c.Version {
	case 1:
	default:
		errs = append(errs, fmt.Errorf("version: %d is not supported (want 1)", c.Version))
	}

	switch c.Product {
	case ProductImage, ProductPackage, ProductExtension:
	case ProductCustom:
		if len(c.Deliverability.NonReleasePaths) == 0 {
			errs = append(errs, errors.New(
				"product: custom derives no non_release_paths, so deliverability.non_release_paths is required"))
		}
	case "":
		errs = append(errs, errors.New("product: required (image, package, extension or custom)"))
	default:
		errs = append(errs, fmt.Errorf("product: %q is not one of image, package, extension, custom", c.Product))
	}

	if !strings.Contains(*c.TagFormat, VersionPlaceholder) {
		errs = append(errs, fmt.Errorf("tag_format: %q must contain %s", *c.TagFormat, VersionPlaceholder))
	}
	if _, err := semver.Parse(c.Versioning.Initial); err != nil {
		errs = append(errs, fmt.Errorf("versioning.initial: %w", err))
	}
	for i, r := range c.Rules {
		if r.Type == "" && r.Scope == "" && r.Breaking == nil {
			errs = append(errs, fmt.Errorf("rules[%d]: matches nothing (set type, scope or breaking)", i))
		}
		if _, err := semver.ParseBump(r.Release); err != nil {
			errs = append(errs, fmt.Errorf("rules[%d].release: %w", i, err))
		}
	}
	switch c.ReleaseCommit.Sign {
	case SignAuto, SignRequired, SignOff:
	default:
		errs = append(errs, fmt.Errorf("release_commit.sign: %q is not auto, required or off", c.ReleaseCommit.Sign))
	}
	for _, e := range hooks.Events() {
		for i, h := range c.Hooks.For(e) {
			if strings.TrimSpace(h.Run) == "" {
				errs = append(errs, fmt.Errorf("hooks.%s[%d].run: required", e, i))
			}
			if h.Timeout != "" {
				if _, err := time.ParseDuration(h.Timeout); err != nil {
					errs = append(errs, fmt.Errorf("hooks.%s[%d].timeout: %q is not a duration such as \"90s\"", e, i, h.Timeout))
				}
			}
		}
	}
	for i, t := range c.AfterRelease.Triggers {
		if t.Project == "" {
			errs = append(errs, fmt.Errorf("after_release.triggers[%d].project: required", i))
		}
		if t.Ref == "" {
			errs = append(errs, fmt.Errorf("after_release.triggers[%d].ref: required", i))
		}
	}

	return errors.Join(errs...)
}

// NonReleasePaths resolves the effective list: an explicit list replaces the
// derived one, extras always extend whatever won.
func (c *Config) NonReleasePaths() []string {
	base := c.Deliverability.NonReleasePaths
	if len(base) == 0 {
		base = derivedNonReleasePaths(c.Product)
	}
	out := slices.Clone(base)
	return append(out, c.Deliverability.ExtraNonReleasePaths...)
}

// TagFormatString returns the raw format string, placeholder included.
func (c *Config) TagFormatString() string { return *c.TagFormat }

func (c *Config) MajorOnZero() bool          { return *c.Versioning.MajorOnZero }
func (c *Config) ReleaseCommitEnabled() bool { return *c.ReleaseCommit.Enabled }
func (c *Config) GitLabReleaseEnabled() bool { return *c.GitLabRelease.Enabled }

// Tag renders a version through tag_format.
func (c *Config) Tag(v semver.Version) string {
	return strings.ReplaceAll(*c.TagFormat, VersionPlaceholder, v.String())
}

// VersionFromTag is the inverse of Tag. It reports false for any tag that does
// not match the configured format, which is how foreign tags are ignored.
func (c *Config) VersionFromTag(tag string) (semver.Version, bool) {
	prefix, suffix, _ := strings.Cut(*c.TagFormat, VersionPlaceholder)
	rest, ok := strings.CutPrefix(tag, prefix)
	if !ok {
		return semver.Version{}, false
	}
	rest, ok = strings.CutSuffix(rest, suffix)
	if !ok {
		return semver.Version{}, false
	}
	v, err := semver.Parse(rest)
	if err != nil {
		return semver.Version{}, false
	}
	return v, true
}

// Expand substitutes ${version} and ${tag} in a template such as the
// release-commit message, the GitLab release name, or a trigger variable.
func Expand(tmpl string, v semver.Version, tag string) string {
	out := strings.ReplaceAll(tmpl, VersionPlaceholder, v.String())
	return strings.ReplaceAll(out, TagPlaceholder, tag)
}
