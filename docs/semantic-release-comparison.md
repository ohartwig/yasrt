<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
SPDX-License-Identifier: MIT
-->

# semantic-release → yasrt: configuration mapping

Option by option, against semantic-release core and the seven plugins this
estate uses or could use (`commit-analyzer`, `release-notes-generator`,
`changelog`, `git`, `gitlab`, `github`, `exec`), as documented in their
READMEs on 2026-09-11. Read it in one direction: *I have this in
`.releaserc.yml` — where does it go in `.yasrt.yaml`?*

Legend: **✓** same capability · **≈** covered differently · **✗** not
available, with the reason · **+** yasrt only.

## Core

| semantic-release | yasrt | |
|---|---|---|
| `branches` (list) | default branch is always a release branch; `versioning.maintenance` for `1.x` / `1.2.x` ranges; `versioning.prereleases` for `{branch: identifier}` | ≈ Ranges and prereleases are the two shapes the estate uses. Not covered: `channel`, and the `next` / `next-major` distribution channels — those exist to serve npm dist-tags, which the binary does not publish to. |
| `branches[].range` | `versioning.maintenance: ["1.x"]` | ✓ Same syntax. A bump that would leave the range is lowered, not refused. |
| `branches[].prerelease` | `versioning.prereleases: {develop: rc}` | ✓ `-rc.N`, counter resets when the core version moves. |
| `branches[].channel` | — | ✗ Registry concept; registries stay with the build jobs. |
| `repositoryUrl` | detected from `CI_PROJECT_URL` / `GITHUB_SERVER_URL` + `GITHUB_REPOSITORY` | ≈ Not configurable; the forge tells us. |
| `tagFormat` | `tag_format`, **derived from `product`** (`${version}` for image/custom, `v${version}` for package/extension) | ✓ Same `${version}` placeholder; explicit value overrides the derivation. |
| `plugins` | `hooks.*` (out of process) | ≈ See the exec section. There is no in-process plugin chain by design. |
| `extends` (shareable config) | `--defaults` / `YASRT_DEFAULTS`, layered: maps merge, scalars and lists replace | ✓ The component ships the estate defaults; a repository states only what differs. |
| `dryRun` / `--dry-run` | `yasrt next` writes nothing; `yasrt release --dry-run` renders and writes nothing | ✓ |
| `ci` / `noCi` | — | ≈ Runs anywhere; outside CI say `--forge`. |
| `debug` | `--verbose`, `--log-format json` | ✓ |
| — | `product` | + The one field almost everything derives from. |
| — | `versioning.initial` | + First version; semantic-release hard-codes `1.0.0`. |
| — | `versioning.major_on_zero` | + Breaking change below 1.0.0 bumps minor when `false`. |
| — | `yasrt next --force minor`, `--version 2.0.0`, `--ref` | + Manual override without a fake commit. |

## `@semantic-release/commit-analyzer`

| semantic-release | yasrt | |
|---|---|---|
| `preset` (`angular`, `conventionalcommits`, …) | own Conventional Commits parser | ≈ Behaves like `conventionalcommits`: `type(scope)!: subject`, `BREAKING CHANGE:` footer. No preset dependency, so no preset choice. |
| `parserOpts` | — | ✗ Header regex is fixed. No repository in the estate overrides it. |
| `releaseRules` | `rules[]` with `type`, `scope`, `breaking`, `release` | ≈ Same first-match-wins evaluation. semantic-release can match any commit property (`subject`, `tag`, custom); yasrt matches type, scope and the breaking flag — the three that appear in every `releaseRules` in the estate. |
| `presetConfig` | — | ✗ Preset concept absent. |
| default rules | breaking → major, `feat` → minor, `fix`/`perf`/`revert` → patch | ✓ Same as the `conventionalcommits` preset. An organisation that ships dependency bumps adds `chore → patch` in its shared defaults; the rules list replaces rather than merges. |
| — | `ignore.scopes` (always contains `release`) | + Loop guard; not configurable away. |
| — | `ignore.authors` | + Skip commits by author, e.g. a bot. |
| — | `ignore.trailers` (`skip release`, `release skip`) | + Per-commit opt-out. |
| — | `deliverability.non_release_paths`, `extra_non_release_paths` | + A change touching only these paths is not a release. semantic-release has no equivalent; the old component bolted it on in shell, against the wrong commit range (I-081). |

## `@semantic-release/release-notes-generator`

| semantic-release | yasrt | |
|---|---|---|
| `preset` / `config` | fixed `conventional-changelog` shape | ≈ Output matches what the estate's `CHANGELOG.md` files already contain, so a migrated file does not change format halfway down. |
| `parserOpts` | — | ✗ As above. |
| `writerOpts` | `changelog.sections[]` (`type` → `title`, in order) | ≈ Section titles and order, which is what `writerOpts` is used for here. Arbitrary templates: no. |
| `host`, `linkCompare`, `linkReferences`, `commit`, `issue` | derived from the forge (`/commit/`, `/compare/`, `/issues/`, `/merge_requests/` or `/pull/`) | ≈ Always linked, shape follows the platform. |
| `presetConfig` | — | ✗ |

## `@semantic-release/changelog`

| semantic-release | yasrt | |
|---|---|---|
| `changelogFile` | `changelog.file` (default `CHANGELOG.md`) | ✓ |
| `changelogTitle` | `changelog.title` | ✓ Written only where the file has no title yet; an existing title is kept. |

## `@semantic-release/git`

| semantic-release | yasrt | |
|---|---|---|
| plugin present at all | `release_commit.enabled` (default `true`) | ✓ |
| `message` | `release_commit.message` (`${version}`, `${tag}`, `${notes}`) | ✓ semantic-release's `${nextRelease.version}` / `${nextRelease.notes}` become the short names. |
| `assets` | `release_commit.assets` (globs) | ✓ Default `["CHANGELOG.md"]`. |
| `GIT_AUTHOR_*` / `GIT_COMMITTER_*` | `release_commit.author` (default a `yasrt <…@noreply.invalid>` placeholder; organisations set theirs in the shared defaults) | ✓ Configured, not taken from the environment. |
| — | `release_commit.sign: auto \| required \| off` | + OpenPGP or OpenSSH key from `GPG_SEM_REL_B64`, detected from the material. semantic-release leaves signing to whatever `git` is configured with. |
| commit **before** tag | tag **before** release commit, tag on the built commit | ≈ Deliberate: a failure after the tag leaves a complete release, and the tag names what was built. |

## `@semantic-release/gitlab`

| semantic-release | yasrt | |
|---|---|---|
| `gitlabUrl`, `gitlabApiPathPrefix`, `GL_URL`, `GL_PREFIX` | `CI_API_V4_URL` | ≈ Detected. |
| `GL_TOKEN` / `GITLAB_TOKEN` | — | ✗ Deliberately: job token only. |
| `useJobToken`, `CI_JOB_TOKEN` | always | ✓ |
| `assets[]` with `url` | `gitlab_release.assets[]` (`name`, `url`, `link_type`) | ✓ |
| `assets[]` with `path`, `target: generic_package`, `packageName` | `gitlab_release.assets[].path`, `package` | ✓ Generic package registry only — the one upload target a job token may write to. `project_upload` is not offered. |
| `milestones` | — | ✗ Not used in the estate; `after_release` hook if needed. |
| `successComment`, `successCommentCondition`, `failComment`, `failTitle`, `failCommentCondition`, `labels`, `assignee` | — | ✗ Issue/MR commenting. Not used here; an `after_release` hook has the release context on stdin and can do it. |
| `retryLimit` | fixed: 3 attempts, exponential backoff, on 5xx and 429 | ≈ |
| `HTTP_PROXY`, `NO_PROXY` | honoured by `net/http` | ✓ |
| — | `gitlab_release.enabled`, `gitlab_release.name` | + `name` ≈ GitHub's `releaseNameTemplate`. |
| — | `after_release.triggers[]` (`project`, `ref`, `variables`) | + Pipeline trigger API; GitLab only, reported as undeliverable elsewhere. |

## `@semantic-release/github`

| semantic-release | yasrt | |
|---|---|---|
| `githubUrl`, `githubApiPathPrefix`, `githubApiUrl`, `GITHUB_URL`, `GITHUB_API_URL` | `GITHUB_SERVER_URL`, `GITHUB_API_URL` | ≈ Detected; GHES works through the same variables. |
| `GITHUB_TOKEN` / `GH_TOKEN` | `GITHUB_TOKEN` (`FORGEJO_TOKEN` on Forgejo) | ✓ The workflow's own token, `contents: write`. |
| `assets[]` (file upload) | `gitlab_release.assets[].path` | ✓ Attached to the release. `url` entries become an `### Assets` list in the body. Same block as for GitLab, so a repository moving between forges keeps its configuration. |
| `releaseNameTemplate` | `gitlab_release.name` | ✓ |
| `releaseBodyTemplate` | — | ✗ Body is the notes (+ assets). |
| `draftRelease` | — | ✗ Tag is confirmation; a draft would announce first. |
| `successComment`, `failComment`, `failTitle`, `labels`, `assignees`, `releasedLabels`, `addReleases`, `discussionCategoryName` | — | ✗ Hook territory, as above. |
| `proxy` | `HTTPS_PROXY` | ✓ via `net/http`. |
| — | Forgejo | + Same client, `token` auth, `<server>/api/v1`; detected before GitHub. |

## `@semantic-release/exec`

semantic-release runs a shell string at each lifecycle step; yasrt runs an
executable with `args` (no shell, no word splitting) and hands it the release
context as JSON on stdin plus `RELEASE_*` variables. Each hook has `name`,
`timeout` and `allow_failure`.

| semantic-release | yasrt | |
|---|---|---|
| `verifyConditionsCmd` | `yasrt check`; `hooks.before_tag` for repository-specific checks | ≈ `before_tag` runs before any write and aborts on failure. |
| `analyzeCommitsCmd` (return a custom release type) | — | ✗ Replacing the analysis defeats the point; `rules[]` and `--force` cover the cases seen. |
| `verifyReleaseCmd` | `hooks.after_analysis` (in `next`) or `hooks.before_tag` (in `release`) | ✓ |
| `generateNotesCmd` | — | ✗ Notes are rendered by yasrt so that CHANGELOG and release stay identical. |
| `prepareCmd` | `hooks.before_tag` | ✓ Runs after notes/changelog are rendered, before the tag. |
| `publishCmd` | `hooks.after_tag` | ✓ The tag is on the remote; the release commit is not yet made. Fatal on failure. |
| `addChannelCmd` | — | ✗ No channels. |
| `successCmd` | `hooks.after_release` | ✓ Reported, never fatal. |
| `failCmd` | `hooks.on_failure` | ✓ Told `error` and `failed_step`; never fatal itself. |
| `shell` | — | ✗ No shell by design; wrap in a script if one is needed. |
| `execCwd` | — | ✗ Runs in the repository root. |

## What has no counterpart in semantic-release

- **`.release.env` handoff** and the `version → build → release` split: the tag is created only after the customer's build succeeded.
- **Idempotent re-run** keyed on the remote tag; exit `5` when the tag exists on another commit.
- **`yasrt check`** including the release-authority probe (who may merge vs. who may create the tag), which fails closed.
- **Deliverability** over `lastTag..HEAD`.
- **Three loop guards** built in.
- **Layered defaults** from the component, so ninety repositories carry no configuration at all.

## What semantic-release has that yasrt deliberately does not

- The plugin ecosystem itself (`npm`, `pypi`, `docker`, `slack`, …). Everything
  publishing-shaped stays in build jobs or becomes a hook.
- Distribution channels (`next`, `channel`) — an npm dist-tag concept.
- Release assets as **project uploads** (`target: project_upload`); yasrt
  uploads to the generic package registry, which a job token may write to.
- Issue and merge-request comments, labels, milestones, draft releases,
  discussions.
