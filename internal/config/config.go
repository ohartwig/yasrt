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
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ohartwig/yasrt/internal/hooks"
	"github.com/ohartwig/yasrt/internal/semver"
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

// ReleaseScope is the commit scope yasrt uses for its own release commit,
// chore(release). That commit is always ignored during analysis and cannot be
// configured away: it is the second of the three loop guards (SPEC §6.3).
// Only that commit -- the scope alone is not reserved, so a feat(release) in a
// repository whose subject happens to be releasing still counts.
const ReleaseScope = "release"

// IsReleaseCommit reports whether a commit of this type and scope is the one
// yasrt writes itself.
func IsReleaseCommit(typ, scope string) bool {
	return strings.EqualFold(typ, "chore") && strings.EqualFold(scope, ReleaseScope)
}

// DefaultReleaseAuthor is the identity release commits are made under when
// neither the repository nor a defaults layer names one. A placeholder on a
// reserved domain, so that it is obviously not a person and never routes
// mail; an organisation sets its own bot identity in its shared defaults.
const DefaultReleaseAuthor = "yasrt <yasrt@noreply.invalid>"

// Placeholders substituted in tag_format and in the message templates.
const (
	VersionPlaceholder = "${version}"
	// TagPlaceholder matters wherever the two differ: with a v-prefixed
	// tag_format, "2.5.1" and "v2.5.1" are not interchangeable, and a
	// follow-up pipeline usually wants the tag it can check out.
	TagPlaceholder = "${tag}"
	// NotesPlaceholder puts the rendered release notes in a template. The npm
	// preset this replaces writes them into the release-commit body, so
	// without it every migrated repository would quietly lose them.
	NotesPlaceholder = "${notes}"
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
	OnFailure     []hooks.Hook `yaml:"on_failure"`
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
	case hooks.OnFailure:
		return h.OnFailure
	}
	return nil
}

type Versioning struct {
	Initial     string `yaml:"initial"`
	MajorOnZero *bool  `yaml:"major_on_zero"`
	// Prereleases maps a branch to the identifier its releases carry, e.g.
	// {develop: rc} to cut 2.5.0-rc.1 from develop. A branch that is absent
	// cuts stable releases. Both shapes occur in practice: the prerelease on
	// the default branch with stable cut from a release branch, and the
	// opposite.
	Prereleases map[string]string `yaml:"prereleases"`
	// Maintenance lists branches that release inside a fixed version range,
	// written the way semantic-release writes them: "1.x" keeps a release
	// inside major 1, "1.2.x" inside minor 1.2. A breaking change on such a
	// branch cannot leave the range — that is what makes it a maintenance
	// branch rather than just an old one.
	Maintenance []string `yaml:"maintenance"`
	// PreviousTagFormats lists formats earlier releases were tagged with, so a
	// repository that changes tag_format keeps its history: the old tags are
	// still recognised as releases, the new format is used for new ones. A
	// format that is never used to create a tag cannot collide with anything.
	PreviousTagFormats []string `yaml:"previous_tag_formats"`
}

// maintenanceRE matches the conventional shapes: 1.x and 1.2.x.
var maintenanceRE = regexp.MustCompile(`^(\d+)\.(?:(\d+)\.)?x$`)

// Range is the version window a maintenance branch may release within.
type Range struct {
	Major uint64
	Minor uint64
	// HasMinor distinguishes 1.2.x, which is pinned to a minor, from 1.x,
	// which may still raise it.
	HasMinor bool
}

// Contains reports whether a version falls inside the range.
func (r Range) Contains(v semver.Version) bool {
	if v.Major != r.Major {
		return false
	}
	return !r.HasMinor || v.Minor == r.Minor
}

// Cap lowers a bump that would leave the range: on 1.x a major becomes a
// minor, on 1.2.x anything above a patch becomes a patch.
func (r Range) Cap(b semver.Bump) semver.Bump {
	if r.HasMinor {
		if b > semver.Patch {
			return semver.Patch
		}
		return b
	}
	if b > semver.Minor {
		return semver.Minor
	}
	return b
}

func (r Range) String() string {
	if r.HasMinor {
		return fmt.Sprintf("%d.%d.x", r.Major, r.Minor)
	}
	return fmt.Sprintf("%d.x", r.Major)
}

// MaintenanceFor reports the range a branch releases within, if it is one.
func (c *Config) MaintenanceFor(branch string) (Range, bool) {
	for _, b := range c.Versioning.Maintenance {
		if b != branch {
			continue
		}
		m := maintenanceRE.FindStringSubmatch(branch)
		if m == nil {
			return Range{}, false
		}
		var r Range
		r.Major, _ = strconv.ParseUint(m[1], 10, 64)
		if m[2] != "" {
			r.Minor, _ = strconv.ParseUint(m[2], 10, 64)
			r.HasMinor = true
		}
		return r, true
	}
	return Range{}, false
}

// PrereleaseFor returns the identifier a branch releases under, and whether the
// branch is a prerelease branch at all.
func (c *Config) PrereleaseFor(branch string) (string, bool) {
	id, ok := c.Versioning.Prereleases[branch]
	return id, ok && id != ""
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
	File string `yaml:"file"`
	// Title is written as the first line ("# Title") when the file is created
	// or has no title yet. An existing title is left alone: the file belongs
	// to the repository, not to the tool.
	Title    string    `yaml:"title"`
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

// ReleaseLink is one release asset: either a link to something the build
// published (URL) or a local file yasrt uploads (Path). Not both.
type ReleaseLink struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	// Path is a glob relative to the repository. Each match is uploaded; the
	// name is the file's base name unless exactly one file matches and Name
	// is set.
	Path string `yaml:"path"`
	// Package names the GitLab generic package the upload lands in. Defaults
	// to "release". Ignored on GitHub and Forgejo, which attach files to the
	// release itself.
	Package  string `yaml:"package"`
	LinkType string `yaml:"link_type"`
}

// IsUpload reports whether the asset is a local file rather than a link.
func (l ReleaseLink) IsUpload() bool { return l.Path != "" }

type AfterRelease struct {
	Triggers []Trigger `yaml:"triggers"`
}

type Trigger struct {
	Project   string            `yaml:"project"`
	Ref       string            `yaml:"ref"`
	Variables map[string]string `yaml:"variables"`
}

// prereleaseIdentifierRE is SemVer's alphanumeric identifier, minus the dot:
// yasrt appends ".N" itself, so the configured part must not carry one.
var prereleaseIdentifierRE = regexp.MustCompile(`^[0-9A-Za-z-]+$`)

// DefaultRules matches what semantic-release's conventionalcommits preset
// releases, so a repository moving over gets the versions it is used to.
// Anything not listed produces no release; an organisation that ships
// dependency bumps as patches adds `chore` in its shared defaults.
func DefaultRules() []Rule {
	yes := true
	return []Rule{
		{Breaking: &yes, Release: "major"},
		{Type: "feat", Release: "minor"},
		{Type: "fix", Release: "patch"},
		{Type: "perf", Release: "patch"},
		{Type: "revert", Release: "patch"},
	}
}

// DefaultSections is the changelog order from SPEC §5, with the emoji titles
// the conventional-changelog presets popularised. Configurable per section.
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
// image, the CI configuration builds the deliverable and therefore is part of
// it; for a package it is not. Tooling files of a particular organisation
// (hook manifests, editor settings) belong in `extra_non_release_paths` of
// its shared defaults, not here.
func derivedNonReleasePaths(p Product) []string {
	switch p {
	case ProductImage:
		return []string{"CHANGELOG.md", "README.md", "docs/**"}
	case ProductPackage, ProductExtension:
		return []string{
			"CHANGELOG.md", "README.md", "docs/**",
			".gitlab-ci.yml", ".gitlab/**", ".github/**",
		}
	}
	return nil
}

// derivedTagFormat implements decision D5: images and tooling tag bare
// (1.16.12), publishable packages tag with a v prefix (v2.5.1) — the shape
// Composer, npm and Go expect.
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
	// Loop guard 2 -- our own chore(release) commit never bumps -- is enforced
	// in the rule evaluation, not by seeding ignore.scopes: the scope alone
	// would swallow a feat(release) too. Whoever lists the scope here still
	// gets the broader behaviour.
	if c.Ignore.Scopes == nil {
		c.Ignore.Scopes = []string{}
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
		// Subject, blank line, notes — the shape the npm preset produces, so a
		// migrated repository's history does not change shape at the cutover.
		c.ReleaseCommit.Message = "chore(" + ReleaseScope + "): " + VersionPlaceholder +
			"\n\n" + NotesPlaceholder
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
	for i, f := range c.Versioning.PreviousTagFormats {
		if !strings.Contains(f, VersionPlaceholder) {
			errs = append(errs, fmt.Errorf("versioning.previous_tag_formats[%d]: %q must contain %s", i, f, VersionPlaceholder))
		}
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
	for _, b := range c.Versioning.Maintenance {
		if !maintenanceRE.MatchString(b) {
			errs = append(errs, fmt.Errorf(
				"versioning.maintenance: %q is not a range like 1.x or 1.2.x", b))
		}
	}
	for branch, id := range c.Versioning.Prereleases {
		if branch == "" {
			errs = append(errs, errors.New("versioning.prereleases: a branch name cannot be empty"))
		}
		if !prereleaseIdentifierRE.MatchString(id) {
			errs = append(errs, fmt.Errorf(
				"versioning.prereleases[%s]: %q is not a valid identifier (letters, digits and hyphens)", branch, id))
		}
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
	for i, a := range c.GitLabRelease.Assets {
		switch {
		case a.URL != "" && a.Path != "":
			errs = append(errs, fmt.Errorf("gitlab_release.assets[%d]: set url or path, not both", i))
		case a.URL == "" && a.Path == "":
			errs = append(errs, fmt.Errorf("gitlab_release.assets[%d]: url or path is required", i))
		case a.URL != "" && a.Name == "":
			errs = append(errs, fmt.Errorf("gitlab_release.assets[%d].name: required for a link", i))
		case a.Path != "" && (filepath.IsAbs(a.Path) || strings.HasPrefix(a.Path, "..")):
			errs = append(errs, fmt.Errorf("gitlab_release.assets[%d].path: must be relative to the repository", i))
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
// not match the configured format or one of the previous ones, which is how
// foreign tags are ignored.
func (c *Config) VersionFromTag(tag string) (semver.Version, bool) {
	if v, ok := versionFromTag(*c.TagFormat, tag); ok {
		return v, true
	}
	for _, f := range c.Versioning.PreviousTagFormats {
		if v, ok := versionFromTag(f, tag); ok {
			return v, true
		}
	}
	return semver.Version{}, false
}

func versionFromTag(format, tag string) (semver.Version, bool) {
	prefix, suffix, _ := strings.Cut(format, VersionPlaceholder)
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

// Vars are the values a template may refer to.
type Vars struct {
	Version semver.Version
	Tag     string
	Notes   string
}

// Expand substitutes ${version}, ${tag} and ${notes} in a template such as the
// release-commit message, the GitLab release name, or a trigger variable.
func (vars Vars) Expand(tmpl string) string {
	out := strings.ReplaceAll(tmpl, VersionPlaceholder, vars.Version.String())
	out = strings.ReplaceAll(out, TagPlaceholder, vars.Tag)
	return strings.ReplaceAll(out, NotesPlaceholder, vars.Notes)
}

// Expand is the shorthand for templates that cannot refer to the notes.
func Expand(tmpl string, v semver.Version, tag string) string {
	return Vars{Version: v, Tag: tag}.Expand(tmpl)
}
