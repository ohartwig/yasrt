# YASRT – Technical Specification

*Yet Another Semantic Release Tool* — working title. As of 2026-09-11, draft v0.2

> v0.2 corrects v0.1 against the actual estate. See [`plan.md`](plan.md) §2 for the findings and
> §3 for the decisions behind each change.

---

## 1. Goal

A single, statically linked Go binary replaces the current npm-based
`semantic-release` chain (5 plugins, 2 configuration modes, GPG special path) in the
GitLab CI/CD component. It

- decides **whether** and **which** version gets published (commit analysis + deliverability),
- publishes it **only after the artifact has been built** (tag = confirmation, not announcement),
- runs **without stored secrets** (only `CI_JOB_TOKEN`; GPG optional),
- is described by **layered configuration**: shared defaults supplied by the CI
  component, overridden per repository where it differs (§5.4). A repository
  that matches its component's defaults needs no file at all.

## 2. Non-goals

Prereleases (§5.3), maintenance branches (§5.5) and the three forges
semantic-release publishes to — GitLab, GitHub and Forgejo (§7.1) — **are**
supported. What remains out of scope, and why:

- **Monorepos with independently versioned packages.** No repository in this
  estate does this: `development/moselwal/dev` carries nested configurations but
  releases as one version series. Building it would change the core model from
  one version per repository to N, for no current consumer.
- **Publishing to registries.** Four components already do this
  (`composer-package-gitlab-release`, `typo3-extension-release`,
  `packagist-submit`, `cleanup-release-tags`), and an `after_release` hook
  covers anything they do not. Duplicating working infrastructure inside the
  release tool would give two places to be wrong. That includes npm, PyPI and
  OCI registries: those stay with the build jobs.
- **Plugin system as a package chain.** semantic-release's plugins are npm
  packages resolved at run time, which is what produces the 384–516 unpinned
  transitive dependencies and the ~21 s `npm install` this tool exists to
  remove. Extension instead happens out of process, through **exec hooks**
  (§5.2): yasrt calls any executable at defined points and hands it the release
  context. The binary stays dependency-free and a plugin needs no package
  manager in the release path.
- **Forges beyond the three above** (Bitbucket, Azure DevOps). semantic-release
  does not ship them either; the `forge.Client` interface is small enough that
  one would be an afternoon, but nobody here has asked.

## 3. Current state

| Metric | Value |
|---|---|
| Runs / day | 122 |
| Job duration | ~40 s, of which ~21 s `npm install` |
| Runner time spent on install | ~43 min / day |
| Unpinned transitive packages | 384–516 (with the GPG path; a lockfile is structurally impossible) |
| Configuration modes | 2 (`.releaserc.yml` per repo **or** preset package from the npm registry with `.npmrc` generation) |
| Component inputs | 14 |
| Known outage | 3 days, 17 tags without artifact (tag before build) |

## 4. Architecture

### 4.1 Overview

```text
┌──────────────────────── GitLab pipeline (default branch) ────────────────────────┐
│                                                                                  │
│  version ──► build/publish ──► release                                           │
│  (yasrt next)  (customer jobs)  (yasrt release)                                  │
│      │              │               │                                            │
│      │ .release.env │ uses          │ CHANGELOG commit, tag, GitLab release,      │
│      │ (dotenv)     │ RELEASE_      │ follow-up trigger                          │
│      │              │ VERSION       │                                            │
└──────────────────────────────────────────────────────────────────────────────────┘
```

Two subcommands, one config file, one binary:

| Command | Purpose | Side effects |
|---|---|---|
| `yasrt next` | F1–F3: determine version, check deliverability, emit result as dotenv/JSON | none (read-only) |
| `yasrt release` | F4–F9: notes, CHANGELOG, commit, tag, GitLab release, follow-up trigger | writing, idempotent |
| `yasrt check` | validate configuration, probe environment (git depth, token, push permission) | none |

### 4.2 Technology

- Go 1.27, built on `registry.ole-hartwig.eu/devops/images/golang:1.27` (Wolfi, arm64).
  `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w -X main.version=…"`, reproducible build.
  Estate convention: `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly`, module cache keyed on `go.sum`.
- Git operations via `os/exec` against the `git` binary in the job image (no go-git; GPG/SSH signing, credential handling and shallow-clone behaviour must be exactly Git's).
- GitLab API: `net/http` + `encoding/json/v2`, three endpoints only (Releases, Release Links,
  Pipeline Trigger). No client library — the estate's own release jobs already reach these
  endpoints with plain `curl`, and a client tree is hard to justify in a tool built to cut
  dependencies.
- Conventional Commits parser: own, small implementation (header, body, footer `BREAKING CHANGE:`, `!` marker). No preset dependency.
- Configuration: YAML, with a JSON schema in the repo for editor support and `yasrt check`.
- CLI: standard library `flag`, no framework dependency.
- Distribution: `linux/arm64` (the runner fleet) and `linux/amd64`; as a binary in the GitLab
  generic package registry **and** as a Wolfi-based image (`devops/ci-mirrors/wolfi-base`) built
  with standalone BuildKit through `devops/ci-cd-components/buildkit-image-build`.
  Signed with **keyed cosign against AWS KMS** via the pipeline's OIDC assume-role — keyless
  Fulcio is not usable here, because a self-hosted GitLab is not on Fulcio's OIDC issuer
  allowlist. SBOM (CycloneDX, via syft) and provenance attestation come from
  `image-signing/cosign-attest-sbom`.

## 5. Configuration

File `.yasrt.yaml` in the repo root. All fields optional except `product`.

```yaml
version: 1

# F11 – What does this repo produce?
product: image          # image | package | extension | custom

tag_format: "${version}"   # F2; default derived from product:
                           #   image | custom      -> "${version}"   (1.16.12)
                           #   package | extension -> "v${version}"  (v2.5.1)

versioning:                # public request #6
  initial: "1.0.0"         # first version when no tag exists
  major_on_zero: true      # false ⇒ breaking in 0.x only bumps minor
  previous_tag_formats: [] # formats earlier releases used, e.g. ["${version}"]
                           # after switching to "v${version}": still recognised
                           # as releases, never used to create a tag

# F1 – rules: first match per commit, highest bump wins
rules:
  - breaking: true
    release: major
  - type: feat
    release: minor
  - type: fix
    release: patch
  - type: perf
    release: patch
  - type: revert
    release: patch
  # everything else: no release. The defaults follow semantic-release's
  # preset; an organisation that ships dependency bumps adds chore → patch
  # in its shared defaults file.

ignore:                    # public request #7
  scopes: []               # our own chore(release) commit is always ignored, scope or not
  authors: []              # e.g. ["renovate-bot"]
  trailers: ["skip release", "release skip"]   # #5, [skip release] in body

# F3/F11 – deliverability
deliverability:
  # derived from product (see 5.1); overrides only
  non_release_paths: []    # replaces the derived list entirely
  extra_non_release_paths: []   # extends it

changelog:                 # F4/F5
  file: CHANGELOG.md
  title: ""                # "# Title" written only where the file has none
  sections:                # fixed order
    - { type: feat,     title: ":sparkles: Features" }
    - { type: fix,      title: ":bug: Fixes" }
    - { type: docs,     title: ":memo: Documentation" }
    - { type: style,    title: ":barber: Styles" }
    - { type: refactor, title: ":zap: Refactor" }
    - { type: perf,     title: ":fast_forward: Performance" }
    - { type: test,     title: ":white_check_mark: Tests" }
    - { type: ci,       title: ":repeat: Continuous Integrations" }
    - { type: chore,    title: ":wrench: Chores" }   # :repeat: was used twice

release_commit:            # F6
  enabled: true
  message: "chore(release): ${version}"
  assets: [CHANGELOG.md]
  sign: auto               # auto | required | off  (auto: sign if a key is present)
  author: "yasrt <yasrt@noreply.invalid>"   # default placeholder; an organisation
                                             # sets its bot identity in its shared defaults

gitlab_release:            # F7 — the release object on whichever forge
  enabled: true
  name: "${version}"
  assets:                  # optional; links (url) or uploads (path), not both
    - { name: "image", url: "${CI_REGISTRY_IMAGE}:${version}", link_type: image }
    - { path: "dist/*.tar.gz", package: yasrt }        # generic package on GitLab,
                                                       # attached on GitHub/Forgejo

after_release:             # F9 – non-fatal
  triggers:
    - project: "devops/renovate-runner"
      ref: main
      # The names are the estate's existing contract, not an invention: the npm
      # component posts exactly these two, and RELEASED_VERSION carries the tag
      # rather than the bare version. With a v-prefixed tag_format the two
      # differ, which is what ${tag} is for.
      variables:
        RELEASED_PACKAGE: "${CI_PROJECT_PATH}"
        RELEASED_VERSION: "${tag}"
```

### 5.4 Layered configuration

```sh
yasrt next --defaults /etc/yasrt/image.yaml          # or YASRT_DEFAULTS
```

`--defaults` is repeatable and merges **under** the repository's own
`.yasrt.yaml`; later layers win. `.yasrt.yaml` is optional once defaults supply
what is needed — a repository that matches its component's defaults carries no
file.

Merge rules: maps merge key by key, scalars and lists replace. Lists replace on
purpose — with `rules` the order decides the outcome, and a silently extended
list would change which rule matches first. Where extending is wanted the schema
says so, which is what `extra_non_release_paths` is for.

Each layer is checked for unknown fields on its own, so a typo is reported
against the file it is in rather than against the merged whole; validation then
runs on the merged result, so defaults may be incomplete as long as the
repository completes them.

**Why this is not the preset mechanism again.** What the npm chain got wrong was
not central defaults but their transport: a preset package resolved at run time,
which is where the 384–516 unpinned dependencies came from. Here the defaults
are a plain file written by the component the repository already includes and
pins. No registry, no network, no resolution step. This matters because the
estate keeps release configuration central on purpose — 90 repositories carry
none of their own, and the shared template says so in as many words.

### 5.5 Maintenance branches

```yaml
versioning:
  maintenance: ["1.x", "1.2.x"]
```

A branch listed here releases inside its own line of versions. Two things follow,
and the second is what makes it a maintenance branch rather than merely an old
one:

- **The baseline is restricted to the range.** A `2.0.0` tag reachable from
  `1.x` is history, not a predecessor, so a fix on `1.x` after `1.2.3` becomes
  `1.2.4` and not `2.0.1`.
- **A bump that would leave the range is lowered, not refused.** A breaking
  change on `1.x` ships as `1.3.0`; on `1.2.x` a feature ships as `1.2.4`. The
  fix still ships — it simply cannot take the branch out of its line.

Only branches listed here are treated this way. A branch merely *named* `3.x` is
an ordinary branch.

### 5.3 Prereleases

```yaml
versioning:
  prereleases:
    develop: rc        # branch develop cuts 2.5.0-rc.1, 2.5.0-rc.2, …
```

A branch listed here cuts prereleases under the given identifier; every other
branch cuts stable releases. Both live shapes in this estate are the same
mechanism seen from either side: `moselwal-packages/*` cuts stable from `main`
and `-rc` from `develop`, `gsb11/extensions/*` cuts `-rc` from `main` and stable
from `release`.

Two ranges are involved, deliberately:

- The **core version** is computed from the last **stable** release, because a
  prerelease accumulates everything since the last real release. A `feat` that
  appeared in `rc.1` still makes the core a minor bump at `rc.2`.
- The **notes and the changed paths** are computed from the last release of
  **any** kind, so `rc.2` describes what is new since `rc.1` rather than
  repeating it.

The counter resets when the core moves: `1.1.0-rc.1` followed by a breaking
change yields `2.0.0-rc.1`, not `2.0.0-rc.2`. A stable release ignores
prerelease tags when choosing its baseline, so `1.0.0` → `1.1.0-rc.1` → `1.1.0`.

The branch is taken from `--branch`, else `CI_COMMIT_BRANCH`, else git. In CI the
checkout is usually detached, which is why the environment is the authority and
git only the fallback.

### 5.2 Hooks (extension points)

```yaml
hooks:
  after_analysis:          # in `next`; observes the decision, cannot change it
    - run: ./scripts/announce.sh
  before_tag:              # in `release`, before anything is written
    - run: ./scripts/policy-check
      name: policy gate
      timeout: 90s
  after_tag:               # tag is on the remote, release commit not yet made
    - run: ./scripts/publish-composer.sh
  after_release:           # last; failures are reported, not fatal
    - run: ./scripts/notify.sh
      allow_failure: true
  on_failure:              # when `release` is about to exit non-zero
    - run: ./scripts/page-someone.sh
```

A hook is any executable. It receives the release context as JSON on stdin and
as `RELEASE_*` environment variables, and signals failure with a non-zero exit
code. `args` are passed verbatim — no shell is involved, so nothing is
word-split or glob-expanded. Default timeout five minutes.

Failure is fatal for `before_tag` and `after_tag` and non-fatal for
`after_analysis`, `after_release` and `on_failure`; `allow_failure` overrides
either way. A `before_tag` veto leaves the repository exactly as it was found.

`on_failure` is the counterpart of semantic-release's `failCmd`: it runs once,
after the step that failed, with `error` and `failed_step` added to the JSON
context (`YASRT_ERROR`, `YASRT_FAILED_STEP` in the environment). It cannot
rescue the release, and its own failure is logged and otherwise ignored — the
original error is what `release` exits with.

Hooks are never handed a credential. A hook that needs a token reads it from
its own environment.

### 5.1 Derivation `product → non_release_paths` (F11)

| product | Deliverable | non_release_paths (default) |
|---|---|---|
| `image` | container image built from the repo | `CHANGELOG.md`, `README.md`, `docs/**` — **not** `.gitlab-ci.yml`, which is part of the deliverable |
| `package` | library / package | `CHANGELOG.md`, `README.md`, `docs/**`, `.gitlab-ci.yml`, `.gitlab/**`, `.github/**` |
| `extension` | extension (e.g. TYPO3 extension, plugin) | same as `package` |
| `custom` | no derivation; `non_release_paths` is required | — |

Glob semantics: `doublestar` (`**`), repo-relative, evaluated against `git diff --name-only <lastTag>..HEAD`.

Tooling files of one organisation (`lefthook.yml`, `.pre-commit-config.yaml`, editor settings) are
not defaults; they go into `deliverability.extra_non_release_paths` of that organisation's shared
defaults file, which the CI component carries.

**Pipeline-only changes in an image repository.** `.gitlab-ci.yml` being part of an image's
deliverable is deliberate: how the image is built, named, signed and scanned lives there and nowhere
else, and a repository once shipped nothing for four days because a pipeline-only fix "changed no
product". The flip side is a dependency bot bumping a pin that touches only the pipeline — a
scanner digest, a component version — as `chore(deps)`: with `chore → patch` that is a release,
the reproducible build produces the bytes already published under a new tag, and every consumer
pinned to the tag rolls for nothing (`devops/images/c2patool` 2.1.10 → 2.1.11, 2026-09-14). The
derivation stays; the expectation is on the bots: **a bump that touches nothing but
`.gitlab-ci.yml` or `.gitlab/**` is typed `ci(deps)`**, and `ci` releases nothing under the
estate's rules. `yasrt next` warns when a `product: image` release would consist of nothing but
`chore` commits whose changed paths all lie under those two — a warning, not a refusal, so the
case stays visible in the log for as long as some bot still writes `chore` there.
Also note the range: the current component compares `CI_COMMIT_BEFORE_SHA..HEAD` (the push range),
which is why a `fix:` bundled with a CI change never ships today (improvement register I-081).
Comparing `<lastTag>..HEAD` is the fix, and it is a behaviour change to be measured before rollout.

## 6. Functional specification

### 6.1 `yasrt next` (F1–F3)

**Inputs:** git repo with full history and tags; `.yasrt.yaml`; optionally `--ref`, `--force <major|minor|patch>`, `--version <x.y.z>`.

**Flow:**

1. Determine last release: highest tag matching `tag_format` that is an ancestor of HEAD (`git tag --merged HEAD`). No tag ⇒ `versioning.initial` as candidate, all commits count.
2. Read commits `lastTag..HEAD` (`git log --format=...`), parse. Ignore if scope, author or trailer matches `ignore`. Non-conforming commits count as "no release" and are not listed in the notes (but appear in the debug log).
3. Determine bump: first matching rule per commit; highest bump across all commits. `major_on_zero: false` lowers `major` to `minor` while current < 1.0.0.
4. Deliverability (F3): changed paths `lastTag..HEAD`; if **all** paths match `non_release_paths` ⇒ not deliverable.
5. Override: `--force` enforces a bump type (even with "no bump"), `--version` sets the version directly; both set `RELEASE_REASON=forced`. Deliverability is **not** skipped unless `--ignore-deliverability` is given.
6. Emit result.

**Output** (`--output .release.env`, plus `--json` to stdout):

```text
RELEASE_STATUS=release        # release | no-bump | not-deliverable | already-released
RELEASE_VERSION=3.4.0
RELEASE_TAG=3.4.0
RELEASE_PREVIOUS=3.3.2
RELEASE_BUMP=minor
RELEASE_REASON=feat           # highest triggering type, or forced
RELEASE_COMMIT=<sha of HEAD>
```

For `no-bump` / `not-deliverable`, `RELEASE_VERSION`/`RELEASE_TAG` are empty.

**Exit codes:** `0` whenever the analysis completed (status is in the output). `--fail-on-skip` ⇒ `3` on `no-bump`, `4` on `not-deliverable`. `1` on configuration/git errors, `2` on invalid arguments.

**`already-released`:** HEAD already carries a tag in `tag_format`. Idempotency anchor for re-runs.

### 6.2 `yasrt release` (F4–F9)

**Inputs:** `.release.env` (or a fresh analysis if absent — the tool then warns, because the result may differ from what `build` used); `CI_JOB_TOKEN`; optionally `GPG_SEM_REL_B64`.

**Preconditions (hard failures):**

- `RELEASE_STATUS == release`.
- `RELEASE_COMMIT == HEAD`. Prevents tagging something other than what was built after an intermediate push.
- Tag does not exist remotely (`git ls-remote --tags`). If it exists on exactly `RELEASE_COMMIT` ⇒ idempotency: skip all completed steps, finish the rest. If it exists on a different commit ⇒ abort, exit `5`.

**Flow, in this order:**

1. **Release notes** (F4) rendered from the analysed commits, in the shape
   `conventional-changelog` produces — because that is what every `CHANGELOG.md`
   in this estate already contains, and a migrated repository would otherwise
   change format halfway down the file:

   ```text
   ## [1.1.1](<project>/compare/1.1.0...1.1.1) (2026-08-24)

   ### :bug: Fixes

   * **scope:** description ([abc1234](<project>/commit/<sha>)) ([#7](<project>/issues/7))
   ```

   Sections only if non-empty. Breaking changes as a dedicated
   `:boom: BREAKING CHANGES` block before all sections. Links omit the `/-/`
   infix, matching the existing files; GitLab serves both forms.
2. **CHANGELOG** (F5): prepend the block above; create the file if missing,
   with no document title — the estate's changelogs start at the first release
   heading, and adding one would put a line above every file's history at the
   moment it migrates.
3. **Tag** (F7, part 1): annotated tag on `RELEASE_COMMIT` (not on the release commit — provenance: the tag points at what was built). Push with job token.
4. **Release commit** (F6): commit `CHANGELOG.md`, message from `release_commit.message`; signature per `sign`. Push to the default branch with job token. **After** the tag, so a failure here leaves a complete release behind, not a half one. When the push is rejected because the branch has moved on — on a repository where Renovate merges several times an hour that was 13 of ~450 pushes in one day; semantic-release refuses outright in that case and releases nothing — the commit is rebuilt on the branch's new head (it only ever touches the changelog and the configured assets) and pushed again, up to three times. A branch that no longer contains the released commit was rewritten and is refused.
5. **Forge release** (F7, part 2): the platform's release object for the tag,
   with the notes as its description. Skipped when one already exists for the
   tag. `gitlab_release.assets` entries with `url` become release links —
   attached on GitLab, appended to the body as an `### Assets` list on GitHub
   and Forgejo, which have no link endpoint (§7.1). Entries with `path` are
   uploads: on GitLab the files go to the generic package registry
   (`<package>/<version>/<file>`, before the release is created, so a failed
   upload leaves nothing behind) and are linked from the release; on GitHub
   and Forgejo they are attached to the release after it is created, where a
   failed upload fails the job but leaves the release — a re-run skips it, so
   the file is attached by hand.
6. **Follow-up trigger** (F9): `POST /projects/:id/trigger/pipeline` per entry in `after_release.triggers`, header `JOB-TOKEN`. `project`, `ref` and every variable expand `${version}`, `${tag}` and `${CI_*}` environment references, so a repository can trigger its own tag pipeline (`project: "${CI_PROJECT_PATH}", ref: "${tag}"`) — the one the job-token push does not start. A tag pushed seconds earlier is not always visible to that endpoint yet (GitLab answers 400 "Reference not found"); that one answer is retried with a growing wait, 5 s to 20 s, up to five attempts. Errors are logged, exit stays `0`. GitLab only — on the other forges every trigger is reported as undeliverable in `release-report.json` rather than guessed at.

Rationale for the order: steps 3–5 are individually idempotent; a re-run after a failure at step 4 finds the tag, skips 3, repeats 4–6.

### 6.3 Loop guard (F8)

Three independent safeguards:

1. **Transport:** pushes with `CI_JOB_TOKEN` do not trigger pipelines in GitLab. This is the primary guard and needs no configuration.
2. **Analysis:** our own `chore(release): x.y.z` commit produces no bump — recognised by type *and* scope, so a `feat(release)` still counts. Cannot be disabled (the tool enforces it in the rule evaluation, not through `ignore.scopes`).
3. **Path:** `CHANGELOG.md` is a `non_release_path` in every `product` derivation.

Today's `rules:changes` guard in `.gitlab-ci.yml` is dropped.

### 6.4 Tag only after the artifact (F10)

Enforced by the job split: `release` runs in a stage after `build`. If `build` fails, `release` does not run; no tag, no release commit. The version is nevertheless already known in `build` (from `.release.env`) and goes into the artifact (image label, package metadata, `--version` output).

Consequence for `rules:`: GitLab evaluates `rules` at pipeline creation, **before** `.release.env` exists. Downstream jobs therefore cannot react to `RELEASE_STATUS` via `rules`; they check it in the script (see 8.1), or the component merges `version` + `build` into one job.

## 7. Authentication and secrets

| Action | Mechanism | Prerequisite |
|---|---|---|
| Git push (tag, release commit) | `https://gitlab-ci-token:${CI_JOB_TOKEN}@…` | project setting **Allow Git push requests to the repository** (GitLab ≥ 18.4); the triggering user may push to the default branch and protected tags |
| Create release | Releases API, header `JOB-TOKEN` | — |
| Follow-up trigger | Pipeline Trigger API, header `JOB-TOKEN` | target project has the source project/group on its job token allowlist |
| Commit and tag signature | `GPG_SEM_REL_B64` (optional), **OpenPGP or OpenSSH** | the only remaining long-lived secret; `sign: auto` degrades to unsigned without a key. The format is detected from the key material, not configured |

**No** PATs, project or group access tokens. `yasrt check` verifies the push setting by test-pushing a dummy ref (`refs/yasrt/check`, deleted immediately) and reports missing permissions before the first real release.

**Signing formats.** The variable may hold either an armoured OpenPGP private key or an OpenSSH private key, base64-encoded (raw armour is accepted too). yasrt detects which from the material: an OpenSSH key sets `gpg.format=ssh` and points `user.signingkey` at a 0600 file that is removed when the run ends; an OpenPGP key is imported into the job's keyring. Both paths are covered by tests that generate real keys and verify the resulting objects with `git verify-tag` and `git verify-commit`.

SSH is the format with a future here: all three forges verify SSH signatures, and an organisation that already trusts SSH keys for human commits needs no second key type. The roadmap remains a short-lived key from Vault via OIDC. Sigstore commit signatures are not verified by GitLab and are therefore not a target.

**Who may release — the authority invariant.** A `CI_JOB_TOKEN` push acts as *the user who triggered the pipeline*, and on the default branch that is whoever merged. The tag push therefore succeeds exactly when:

> the weakest role allowed to **merge into the default branch** is also allowed to **create tags matching `tag_format`**.

Nothing needs weakening if merging is already Maintainer-only: a Maintainer-only protected tag is then consistent. The failure case is a repository where Developers may merge but not tag — every release they trigger dies at the tag push. `yasrt check` compares the two levels and reports the mismatch as fatal, per repository, before the first release depends on it. It fails closed: GitLab answers 404 for an unauthorised read exactly as it does for an unprotected branch, so being unable to look is reported as such and never as "unprotected".

**Note:** the push setting changes the behaviour of npm `semantic-release` in repos that still use it (it then authenticates with the job token and release pipelines stop running). Migrate **per repo**: enable the setting only once the repo has moved to YASRT.

### 7.1 Forges

yasrt publishes to the platforms semantic-release publishes to. The forge is
**detected from the job environment**; `--forge` / `YASRT_FORGE` overrides
detection for a job that runs somewhere the markers do not say (`gitea` is
accepted as a synonym for `forgejo`).

| | GitLab | GitHub | Forgejo |
|---|---|---|---|
| Detected by | `GITLAB_CI=true` or `CI_API_V4_URL` | `GITHUB_ACTIONS=true` with `GITHUB_SERVER_URL` = github.com | `FORGEJO_ACTIONS=true`, `GITEA_ACTIONS=true`, or a `GITHUB_SERVER_URL` that is not github.com |
| API root | `CI_API_V4_URL` | `GITHUB_API_URL`, else `https://api.github.com` | `GITHUB_API_URL`, else `<server>/api/v1` |
| Token | `CI_JOB_TOKEN` | `GITHUB_TOKEN` | `FORGEJO_TOKEN`, else `GITHUB_TOKEN` |
| Auth header | `JOB-TOKEN` | `Authorization: Bearer` | `Authorization: token` |
| Push user | `gitlab-ci-token:<token>` | `x-access-token:<token>` | `<token>:x-oauth-basic` |
| Release | `POST /projects/:id/releases` | `POST /repos/o/r/releases` | same as GitHub |
| Release links | `assets/links` endpoint | in the body (`### Assets`) | in the body |
| Uploads (`assets[].path`) | generic package registry, linked | attached to the release (upload host) | attached to the release (multipart) |
| Pipeline trigger | yes | no — reported, not fatal | no — reported, not fatal |
| Link shapes | `/commit/`, `/compare/`, `/issues/`, `/merge_requests/` | `/commit/`, `/compare/`, `/issues/`, `/pull/` | as GitHub |

Detection checks Forgejo **before** GitHub: Forgejo's Actions runner sets the
`GITHUB_*` variables for compatibility, so a plain `GITHUB_ACTIONS` test would
call every Forgejo job GitHub and post its token to the wrong API.

The authority invariant above is GitLab-specific in mechanism only. On GitHub
the equivalent is a ruleset that lets the workflow's `GITHUB_TOKEN` (with
`contents: write`) create tags matching `tag_format`; on Forgejo, a branch
protection that allows the token's owner to push tags. `yasrt check` probes
GitLab; on the other forges it reports the probe as not implemented rather than
claiming a pass.

## 8. Pipeline integration

### 8.1 Component (replacing the current one)

```yaml
# templates/release.yml
spec:
  inputs:
    stage-version: { default: version }
    stage-release: { default: release }
    yasrt-image:   { default: "registry.ole-hartwig.eu/devops/images/yasrt:1" }
    config-path:   { default: ".yasrt.yaml" }
    gpg-key-var:   { default: "GPG_SEM_REL_B64" }
---
.yasrt-base:
  image: $[[ inputs.yasrt-image ]]
  variables:
    GIT_DEPTH: 0
    GIT_STRATEGY: clone
    YASRT_CONFIG: $[[ inputs.config-path ]]
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH && $CI_PIPELINE_SOURCE == "push"

version:
  extends: .yasrt-base
  stage: $[[ inputs.stage-version ]]
  script:
    - yasrt next --output .release.env
  artifacts:
    reports: { dotenv: .release.env }
    expire_in: 1 day

release:
  extends: .yasrt-base
  stage: $[[ inputs.stage-release ]]
  needs: [version, build]         # build from the customer repo
  resource_group: release          # serialises parallel runs
  script:
    - '[ "$RELEASE_STATUS" = "release" ] || { echo "skip: $RELEASE_STATUS"; exit 0; }'
    - yasrt release
```

5 of 14 inputs remain. `config-preset`, `config-preset-group-id`, `config-preset-scope`,
`image-config`, `tags-config` are dropped without replacement.

The template ships as a sixth template inside the existing
`devops/ci-cd-components/release-tools` repo, beside `semantic-release`, so both can run in
parallel during migration. `rules-config` must survive as an input: most consumers override it, and
the existing default guards on `CI_COMMIT_REF_PROTECTED` rather than on the default-branch name
used in the example above.

### 8.2 Customer repo

```yaml
include:
  - component: $CI_SERVER_HOST/devops/ci-cd-components/release-tools/yasrt@1

build:
  stage: build
  needs: [version]
  script:
    - '[ "$RELEASE_STATUS" = "release" ] || exit 0'
    - docker build --label org.opencontainers.image.version=$RELEASE_VERSION -t $CI_REGISTRY_IMAGE:$RELEASE_VERSION .
    - docker push $CI_REGISTRY_IMAGE:$RELEASE_VERSION
```

### 8.3 MR preview (public request #3)

```yaml
release-preview:
  image: registry.ole-hartwig.eu/devops/images/yasrt:1
  variables: { GIT_DEPTH: 0 }
  rules: [{ if: $CI_PIPELINE_SOURCE == "merge_request_event" }]
  script:
    - yasrt next --ref origin/$CI_MERGE_REQUEST_TARGET_BRANCH_NAME --json | tee preview.json
```

Output in the job log: "would release 3.4.0 (minor)" / "not deliverable: only docs/**" / "no bump". Optionally later as an MR comment — that needs a token with `api` scope, hence deliberately **not** in the MVP.

## 9. Edge cases

| Case | Behaviour |
|---|---|
| No tag in the repo | `versioning.initial`, all commits, `RELEASE_PREVIOUS` empty |
| Shallow clone | `next` aborts with exit `1` and a clear message (`GIT_DEPTH: 0` missing). Note this is the estate's current default — nothing sets `GIT_DEPTH` today, so every migrated repo meets this on first run |
| Tags not matching `tag_format` | ignored; warning if foreign SemVer tags are found |
| Squash merge without conventional title | commit does not count; README recommends squash template `%{title}` + MR title lint |
| Several breaking changes | one major bump, all listed in the `:boom:` block |
| `--version` lower than last release | exit `2` |
| Re-run after partial failure | see 6.2, idempotent via tag existence |
| Concurrent push during `release` | `RELEASE_COMMIT != HEAD` ⇒ exit `5`; `resource_group` prevents parallel `release` jobs |
| Push setting not enabled | `git push` fails with 403; message points to `yasrt check` |
| GPG key invalid | `sign: auto` ⇒ warning + unsigned; `sign: required` ⇒ exit `1` **before** step 3 |

## 10. Observability

- Structured logs (JSON with `--log-format json`, otherwise human-readable), log level `--verbose`.
- `next --json` and `release --json` emit machine-readable summaries; `release` additionally writes `release-report.json` (version, tag SHA, release URL, trigger results) as a job artifact.
- Secrets (`CI_JOB_TOKEN`, GPG material) are never logged; git URLs with credentials are masked.

## 11. Security and supply chain

- No network access except the GitLab API and the git remote of the own project (plus trigger targets).
- `go.sum` pinned, Renovate on the tool repo, `govulncheck` in CI. Note that neither
  `govulncheck` nor `golangci-lint` exists anywhere in the estate today; both are introduced here.
- The tool releases itself with YASRT (dogfooding), signed with keyed cosign against AWS KMS,
  SBOM as a release asset, provenance attestation.
- Image: Wolfi-based (`devops/ci-mirrors/wolfi-base`, digest-pinned), non-root, only `yasrt` +
  `git` + optional `gpg`.

## 12. Migration

1. Create the tool repo, build the MVP (`next`, `release`, `check`), publish to the registry.
2. Provide the new component `release@1` alongside the old one.
3. Pilot: 2–3 repos per `product` type. Enable the push setting per repo, add `.yasrt.yaml`, swap the component, delete the `GITLAB_TOKEN` variable.
4. Comparison run: `yasrt next` and `npx semantic-release --dry-run` side by side for one week, evaluate differences (expected: deliverability now applies correctly; `chore(release)` ignored).
5. Roll out to the remaining repos via a Renovate config update; deprecate the old component, remove after 4 weeks.
6. Shut down the preset registry (npm).

Expected effect: job duration ~40 s → ~10 s; additionally, every run ending in `no-bump` or `not-deliverable` stops before the build.

## 13. Tests

- Unit: parser (header/body/footer, `!`, `BREAKING CHANGE:`), rule evaluation, glob matching, version arithmetic incl. `major_on_zero`.
- Golden tests: release notes and CHANGELOG rendering against fixed fixtures.
- Integration: temporary git repo per test case (tags, squash, shallow, re-run), GitLab API against an `httptest` server.
- E2E: one pipeline on a test project of the GitLab instance that passes through every status value once.

## 14. Open questions

**Resolved in v0.2** (see [`plan.md`](plan.md) §3): #3 — the release commit stays on by default
(`release_commit.enabled: true`), accepting that ~53 repos gain a `CHANGELOG.md` they do not have
today. Prereleases move from non-goal to a later phase. `tag_format` is derived from `product`.
The GitLab client library is dropped in favour of `net/http`. Signing is keyed cosign against AWS
KMS, not keyless.

**Still open** (tracked as T-900…T-904 in [`tasks.md`](tasks.md)):

1. **`chore → patch`:** intentional? With Renovate commits (`chore(deps)`) every dependency bump becomes a release. Proposal: keep the rule, but default `ignore.authors: [renovate-bot]` for `package`/`extension`.
2. **Mandatory signing:** should `sign: required` be enforced for certain repos (public sector) before the OIDC path exists?
3. ~~**Release commit at all?**~~ *(resolved: enabled by default)* Without F6, the branch push, GPG and the provenance question disappear entirely; the CHANGELOG then lives only in the GitLab release. Recommendation: keep it switchable per repo (`release_commit.enabled: false`), decide the default.
4. **Emoji for Chores:** `:wrench:` as replacement for the duplicated `:repeat:` — matter of taste.
5. ~~**`build` / `revert`.**~~ *(resolved 2026-09-13)* The binary's default rules are
   semantic-release's: `revert → patch`, `build` and `chore` release nothing. The estate's
   `chore → patch` and "revert ships nothing" live in the CI component's defaults file, where
   every organisation-specific value now lives (OSS review, B2).
6. **Go coverage threshold.** The estate's stated gate is 75 % line coverage from Cobertura for PHP;
   Go reports through GitLab's `coverage:` regex instead. Pick a number and say whether it blocks.
7. **Name.** YASRT is honest but unpronounceable.
