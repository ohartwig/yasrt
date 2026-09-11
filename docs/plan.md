# YASRT — Implementation Plan

Companion to [`SPEC.md`](SPEC.md). The specification says *what* the tool does; this document says
*how it gets built, in what order, and what it collides with*. Task-level detail lives in
[`tasks.md`](tasks.md).

Status: draft v0.1, 2026-09-11.

---

## 1. Why

The npm `semantic-release` chain in `devops/ci-cd-components/release-tools/semantic-release` works,
but it carries costs the estate can no longer justify:

- ~21 s of every ~40 s release job is `npm install`; ~43 min of runner time per day across 122 runs.
- 384–516 unpinned transitive packages, with no structurally possible lockfile once the GPG path is
  in play.
- Two configuration modes (per-repo `.releaserc.yml` **or** an npm preset package with generated
  `.npmrc`), 14 component inputs, and a preset whose `index.cjs` still names the retired
  `gitlab.moselwal.io` host.
- Tags were cut *before* the artifact was built — one three-day outage left 17 tags with no
  artifact behind them.
- The deliverability guard answers the wrong question, which is recorded as open improvement
  **I-081**: a genuine `fix:` bundled with a `.gitlab-ci.yml` change never ships.

YASRT replaces all of it with one static Go binary, one config file per repo, and `CI_JOB_TOKEN` as
the only required credential.

## 2. What the estate actually looks like

The specification was drafted from memory of the old system. Exploration of the working copies under
`/Volumes/Samsung_X5/Projects` corrected it in ten places. These findings drive the phases below.

### 2.1 The component being replaced

Canonical source: `moselwal/devops/ci-cd-components/release-tools/templates/semantic-release/template.yml`
(remote `git.ole-hartwig.eu/devops/ci-cd-components/release-tools`, tag `1.16.12`). Stale clones exist
at `koh-release-tools` (1.15.4) and inside `composed-default-pipelines` — neither is the source of
truth. The repo holds five templates; `semantic-release` is one of them, alongside
`cleanup-release-tags`, `composer-package-gitlab-release`, `packagist-submit` and
`typo3-extension-release`.

Consequences:

- The new component is a **sixth template in that repo**, not a new component repo. Consumers change
  one path segment, and both templates can run side by side during migration.
- The existing `rules-config` input is overridden by most consumers. The new template must keep an
  equivalent input or every consumer's `.gitlab-ci.yml` breaks.
- The old default rules guard on `CI_COMMIT_REF_PROTECTED` and `CI_PIPELINE_SOURCE == 'web'`, not on
  `CI_COMMIT_BRANCH == CI_DEFAULT_BRANCH` as SPEC §8.1 assumes.

### 2.2 Deliverability is already implemented — and asks the wrong question

The current guard compares `CI_COMMIT_BEFORE_SHA..HEAD`, the *push* range. SPEC §6.1 step 4 compares
`lastTag..HEAD`, the *unreleased* range. That difference is the point of the tool: a docs-only push
following an unreleased `feat:` currently blocks the release forever, and a `fix:` bundled with a CI
change is vetoed. It also means **YASRT will release things the old tool skipped**, which the shadow
run (P7) has to quantify before rollout rather than discover in production.

The old guard also falls back to "releasing normally" when `CI_COMMIT_BEFORE_SHA` cannot be resolved —
a silent shallow-clone path. YASRT hard-fails instead (SPEC §9).

### 2.3 Nothing sets `GIT_DEPTH` today

Neither the component nor any consumer sets `GIT_DEPTH` or `GIT_STRATEGY`; every repo runs on GitLab's
shallow default. `GIT_DEPTH: 0` arrives with the new template, so clone time changes in every migrated
repo — measured in the pilot, not assumed.

### 2.4 Most repos have no CHANGELOG today

Of 74 `.releaserc.yml` files found, 53 are identical minimal configs — `commit-analyzer`,
`release-notes-generator`, `gitlab`, nothing else. Those repos have no `CHANGELOG.md` and no release
commit. Only preset consumers (`@moselwal/semantic-release-config`) commit a changelog.
`release_commit.enabled: true` as a global default is a deliberate unification, and it is visible: the
pilot must include one of the 53.

### 2.5 Tag formats are split

Tooling and CI-component repos tag bare — `release-tools` at `1.16.12`, `composed-default-pipelines`
at `2.3.0`, `lint-tools` at `1.32.10`. Composer packages, TYPO3 extensions and npm packages tag with a
`v` prefix — `bidaq-de` at `v1.11.11`, `cluster-file-backend` at `v2.5.1`. Today this is carried by the
`tag-format` input. YASRT derives it from `product` so the common case needs no configuration.

### 2.6 Prereleases are live

`gsb11/extensions/*` cut `v2.4.0-rc.4` style tags from a `main: prerelease rc` branch config, and
`moselwal-packages/*` use a `develop` prerelease branch. SPEC §2 listed prereleases as a non-goal,
which would strand those repos and keep the old template alive indefinitely. They move to a later
phase (P10) instead, and P10 is the precondition for decommissioning.

Separately, `gsb11` vendors its own fork of the component on a different GitLab instance
(`git.gsb-itzbund.de`). That estate is out of scope.

### 2.7 There is already a Go toolchain image and a Go CI component

`devops/images/golang` publishes `registry.ole-hartwig.eu/devops/images/golang:1.27` — Wolfi-based,
`go-1.27` pinned by APK version, arm64, with an inline smoke test proving both native `-race` builds
and `CGO_ENABLED=0` cross-compiles work. And `devops/ci-cd-components/composed-default-pipelines/go`
already provides `lint:gofmt`, `lint:vet`, `test:go` plus the language-neutral hygiene jobs
(semgrep, trivy-fs, quick-checks, markdown lint, component-integrity).

So YASRT is **not** the estate's first Go project — `S3mail` (`git.ole-hartwig.eu/development/s3mail/s3mail`,
Go 1.27) is the reference implementation to imitate, and no new golden image is needed. What the Go
component deliberately omits is build and release; those are hand-written per repo, and S3mail's
`.gitlab-ci.yml` is the pattern.

House conventions to follow rather than reinvent: stdlib `flag` for the CLI surface (no `cobra`
anywhere in the estate, and S3mail states stdlib-only as policy), version via
`-ldflags "-s -w -X main.version=…"`, `GOTOOLCHAIN: local`, `GOFLAGS: -mod=readonly`, module cache
keyed on `go.sum`, coverage through GitLab's `coverage:` regex over `go tool cover -func`.

### 2.8 Signing is keyed KMS, not keyless

SPEC §4.2 and §11 say cosign keyless via GitLab ID token. That does not work here: a self-hosted
GitLab is not on Fulcio's OIDC issuer allowlist. Every real signature in the estate is **keyed cosign
against AWS KMS**, reached by `aws sts assume-role-with-web-identity` from the pipeline's OIDC token
(`$COSIGN_KEY_ARN`, role `gitlab-ci-image-signing-role`), signed by digest and verified in the same
job. SBOMs come from syft as `cyclonedx-json`, attached with `cosign attest`.

### 2.9 Images are Wolfi, built with standalone BuildKit

There is no distroless or scratch image anywhere in the estate. Everything descends from
`registry.ole-hartwig.eu/devops/ci-mirrors/wolfi-base`, built by `buildkit-image-build` using
`buildctl-daemonless.sh` — deliberately not buildx/DinD, not kaniko, not buildah. Setting
`inherit-signing: 'true'` and `inherit-verify: 'true'` on that component yields KMS signing, SBOM
attestation and existence verification without hand-rolling any of it. Non-root is not enforced in the
estate; for YASRT it is cheap and should be done anyway.

### 2.10 `release-cli` is deliberately unused

The runners are IPv6-only and `registry.gitlab.com` is IPv4-only. Releases and asset links are created
with plain `curl` against the Releases API. YASRT talks to the same endpoints from Go, which is why
decision D3 below drops the client library.

### 2.11 What the handbook requires

From `gitlab-profile` (the OKF handbook / IGS bundle):

- **English** for everything an engineer reads — identifiers, comments, commits, branches, MR text,
  documentation (`engineering/code-quality.md`, `igs/security/secure-development.md`).
- Conventional Commits enforced by lefthook + commitlint; allowed types are `feat`, `fix`, `docs`,
  `style`, `refactor`, `perf`, `test`, `ci`, `chore`, `build`, `revert`. SPEC §5's rule list omits
  `build` and `revert`.
- Scaffolding is **copied from `devops/repo-templates`**, not hand-written: `lefthook.yml`,
  `.mise.toml`, `.gitsigners` + `.gitsigners.d/`, `.gitlab/CODEOWNERS`, three issue templates, an MR
  template, `SECURITY.md`, `.well-known/security.txt`. Explicitly **no `.editorconfig`**, and the
  estate uses **`lefthook.yml`, not `.pre-commit-config.yaml`** — which SPEC §5.1 names wrongly in
  every derived path list.
- REUSE v3.3: SPDX headers, `LICENSES/`, `.reuse/dep5`.
- Namespace rule: things that build, ship or operate what we sell live under `devops/`.
- Protected-tag creation is reserved to the Maintainer-level `release-bot` identity, and service
  accounts are explicitly barred from it. See risk R1.
- Component includes and base images are pinned by digest (`component-integrity`).
- `.golangci.yml` and `govulncheck` exist **nowhere** in the estate. SPEC §11 mandates
  `govulncheck`, so YASRT introduces both. That is net-new tooling, not an inherited convention.

## 3. Decisions

| # | Decision | Rationale |
|---|---|---|
| D1 | Documents, code and commits in English | Handbook policy; YASRT has no user-facing strings, so no exception applies |
| D2 | Plan covers everything through migration | The binary alone changes nothing; value arrives when the old template is switched off |
| D3 | GitLab API via `net/http` + `encoding/json/v2` | Three endpoints. A client library is a large dependency tree in a tool whose reason for existing is dependency reduction; the estate already calls these endpoints with curl |
| D4 | `release_commit.enabled: true` globally | Unifies the estate on a repo-visible changelog. Accepted cost: new behaviour in ~53 repos (R2) |
| D5 | `tag_format` derived from `product` | `image`/`custom` → `${version}`, `package`/`extension` → `v${version}`. Matches the split in §2.5 so the common case needs no config; still overridable |
| D6 | Own top-level group `yasrt/`, starting with `yasrt/cli`; component templates stay in the existing `devops/ci-cd-components/release-tools` | Module path `git.ole-hartwig.eu/yasrt/cli`. A product-scoped top-level group follows the pattern already set by `cura-collect-ai`, `nozzleops`, `ai-ready-platform` and `pinup`, and leaves room for further repositories around the tool. The component catalogue stays where consumers already look for it, so migrating a repository remains a one-word change and both templates can run in parallel |
| D7 | Prereleases are a later phase, not a non-goal | Otherwise the old template can never be removed (§2.6) |
| D8 | Keyed cosign against AWS KMS | Keyless Fulcio cannot work against this instance (§2.8) |
| D11 | Extension through **exec hooks**, out of process | semantic-release's plugins are npm packages resolved at run time — the direct cause of the 384–516 unpinned dependencies and the ~21 s `npm install` per run. Hooks give the same extensibility without a package manager in the release path: a hook is any executable, gets the release context on stdin, and signals failure with an exit code. The SPEC §2 non-goal is narrowed accordingly: no plugin *package chain*, but yes to extension points |
| D9 | Wolfi base, `buildkit-image-build` with inherited signing | The only image pattern the estate has; gets SBOM and signature for free (§2.9) |
| D10 | **MIT** | On the handbook's approved list (`igs/compliance/open-source-compliance.md`), no obligations for internal use or customer deliverables, and the lowest possible adoption barrier for a tool other teams may want to run. Both dependencies are permissive, so there is nothing to be compatible with. AGPL was considered and rejected: its distinguishing clause is network-use copyleft, and a CI binary serves nobody over a network, so it would add adoption cost for a protection that never applies — and the handbook flags it for legal review. `release-tools` being GPL-3.0 creates no conflict: it contains YAML that invokes the binary, it does not link it |

D3, D5, D7 and D8 contradict `SPEC.md` as first drafted; the corrections are applied there.

## 4. Architecture

Two write-free commands and one writing command, as SPEC §4.1. The invariants that carry the design —
tag as confirmation rather than announcement, `RELEASE_COMMIT == HEAD`, remote-tag idempotency, the
deliberate tag-before-release-commit ordering — are stated in `CLAUDE.md` and must not be traded away
for convenience.

### 4.1 Packages

| Package | Responsibility |
|---|---|
| `cmd/yasrt` | `flag` parsing, subcommand dispatch, exit-code mapping, `-X main.version` |
| `internal/config` | `.yasrt.yaml` load, `product` derivation, defaults, validation, JSON schema |
| `internal/conventional` | commit parser: header, body, footers, `!`, `BREAKING CHANGE:` |
| `internal/rules` | rule evaluation, `ignore` filters, highest-bump resolution |
| `internal/semver` | version type and bump arithmetic incl. `major_on_zero` — no dependency |
| `internal/git` | `os/exec` wrapper: `tag --merged`, `log`, `diff --name-only`, `ls-remote`, `tag -a`, `commit`, `push` |
| `internal/deliver` | doublestar glob matching over changed paths |
| `internal/analyze` | orchestrates `next`: last tag → commits → bump → deliverability → result |
| `internal/render` | release notes and CHANGELOG rendering, golden-tested |
| `internal/gitlab` | three `net/http` calls: create release, release links, pipeline trigger |
| `internal/output` | `.release.env`, `--json`, `release-report.json` |
| `internal/logging` | `log/slog` handlers plus credential masking |

Dependency budget: `github.com/bmatcuk/doublestar/v4` and one YAML parser. Nothing else. Go 1.27
idioms per `CLAUDE.md`.

Git goes through `os/exec` on the `git` binary, never go-git: GPG and SSH signing, credential
handling and shallow-clone semantics have to be exactly Git's.

### 4.2 Why the write order is what it is

`release` performs notes → CHANGELOG → **tag → release commit** → GitLab release → triggers. The tag
is pushed before the release commit so that a failure at the commit step leaves a complete release
behind rather than half of one, and the tag points at `RELEASE_COMMIT` — what was actually built —
never at the release commit. Steps 3–5 are individually idempotent, so a re-run finds the tag, skips
it, and finishes the rest.

## 5. Phases

Each phase ends in something demonstrable. Task IDs are in [`tasks.md`](tasks.md).

| Phase | Outcome | Ends when |
|---|---|---|
| **P0** Bootstrap | Repo exists, scaffolded to house standard, CI green on an empty suite | `git init`, `go.mod` on Go 1.27, templates copied from `devops/repo-templates`, REUSE clean, `composed-default-pipelines/go` included |
| **P1** `yasrt next` | Read-only analysis complete | Runs against real clones and prints correct status for `release`, `no-bump`, `not-deliverable`, `already-released`; exit codes 0/1/2/3/4 |
| **P2** `yasrt check` | Environment probe | Reports config errors, shallow clone, missing token and missing push permission before any release is attempted |
| **P3** `yasrt release` | Write path complete and idempotent | Tag, release commit, GitLab release, triggers, `release-report.json`; a re-run after an induced failure at each step converges; exit 5 on tag/commit mismatch |
| **P4** Distribution | Installable artefacts | Binary in the generic package registry, Wolfi image signed against KMS with a CycloneDX SBOM attached, `SHA256SUMS` signed |
| **P5** Component | `templates/yasrt/template.yml` in `release-tools` | Template plus MR-preview job, `rules-config`-compatible inputs, README entry |
| **P6** Dogfooding | YASRT releases YASRT | Its own tags are cut by itself, old chain removed from its pipeline |
| **P7** Shadow run | Difference quantified | One week of `yasrt next` beside `semantic-release --dry-run` across a sample; every divergence explained, especially the §2.2 window change |
| **P8** Pilot | 2–3 repos per `product` | Includes one of the 53 changelog-less repos and one large repo for the `GIT_DEPTH` measurement; push setting enabled per repo |
| **P9** Rollout | Estate migrated | Renovate-driven include bumps, `GITLAB_TOKEN` and `RENOVATE_TRIGGER_TOKEN` deleted, handbook pages updated |
| **P10** Prereleases | Old chain removable | `-rc.N` support, then the `semantic-release` template and `@moselwal/semantic-release-config` are decommissioned |

P0–P4 are sequential. P5 can start once P1 lands. P7 needs only P1, so it can run in parallel with
P3–P4 and buy a week of calendar time.

## 6. Risks

**R1 — Protected tags versus the job token.** *(partially answered, still blocks P8)*

*Evidence, 2026-09-11:* on `yasrt/cli` the job token **did** push an annotated tag to a
`*` protected-tag rule set to Maintainer, with `ci_push_repository_for_job_token_allowed`
enabled — and the push started no pipeline, confirming loop guard 1 empirically. But the
pipeline was triggered by the Owner, so this proves the mechanism works, **not** that it works
for a Developer merging to `main`, which is the case the estate actually needs. The decision
below is therefore still open.

`igs/engineering/commit-signing-policy.md` reserves protected-tag creation to the Maintainer-level
`release-bot` identity and bars service accounts from it. A `CI_JOB_TOKEN` push acts as the
*triggering user*, so under SPEC §7 the tag is created by whoever merged to `main` — a Developer,
who by that control may not create it. Either the protected-tag rule is widened for the release
pattern, or a Maintainer identity stays in the loop and the "no stored secrets" premise weakens.
This is the one risk that can invalidate a core design goal, and it must be decided before the pilot,
not during it. `yasrt check` exists partly to surface it per repo.

**R2 — `release_commit.enabled: true`** introduces `CHANGELOG.md` and a branch push into ~53 repos
that have neither (§2.4). Mitigation: one such repo in the pilot, and the behaviour called out in the
migration note per repo.

**R3 — The deliverability window change** will release commits the old tool skipped (§2.2). That is
the fix for I-081, but the blast radius is unknown until P7 measures it.

**R4 — `GIT_DEPTH: 0`** on repos that have only ever cloned shallow (§2.3). Clone-time regression,
measured in P8 on the largest pilot repo.

**R5 — Enabling the push setting flips behaviour for repos still on semantic-release** (SPEC §7): it
starts authenticating with the job token and release pipelines stop running. Strictly per-repo, in the
same MR as the component swap. Never estate-wide, never ahead of the swap.

**R6 — The static GPG key** conflicts with the handbook's KMS/OIDC preference and is the same class of
weakness already documented for the Renovate and melange keys. Carried as an explicit exception with
the SSH-signing-via-OIDC path as the exit; `sign: auto` degrades to unsigned so the key is never
load-bearing for a release.

**R8 — Tag-triggered builds stop firing.** *(open, affects every image repo)*
Image repositories today build on `rules: if: $CI_COMMIT_TAG`, and the tag
pipeline runs because `semantic-release` pushes with a token that starts
pipelines. `CI_JOB_TOKEN` deliberately does not — that is loop guard 1. So after
migration a tag-triggered build would simply never run, and the image for a
release would silently never be built. Migrating an image repository therefore
means rewriting its build job from tag-triggered to `RELEASE_VERSION`-driven in
the same pipeline, which is what SPEC §6.4 already prescribes and what yasrt's
own `.gitlab-ci.yml` demonstrates. This is real per-repo migration work that the
first draft of this plan did not account for, and it lands in P8 and P9.

**R7 — First `golangci-lint` and `govulncheck` in the estate** (§2.11). No config to inherit, and
adding them to `composed-default-pipelines/go` would affect the two other Go consumers. Start local to
YASRT; propose promotion afterwards.

## 7. Verification

- `go test ./...` green, `-race` on the unit suite, coverage reported through the `coverage:` regex.
- Golden tests for release notes and CHANGELOG rendering.
- Integration tests that build throwaway git repos per case — tags, squash merge without a
  conventional title, shallow clone, re-run after partial failure, concurrent push — and serve the
  GitLab API from `httptest`. No test touches a real GitLab instance.
- `yasrt next` run against real working copies: `release-tools` (bare tags, `product: custom`), an
  image repo such as `golang-image` (`product: image`), and a Composer package such as
  `cluster-file-backend` (`v`-prefixed tags, `product: package`). Compared against
  `npx semantic-release --dry-run`.
- `yasrt check` against a scratch project to prove the push-permission probe reports R1 before a real
  release is attempted.
- End to end: one test project that passes through each of the four statuses once.
- House gates: lefthook hooks pass, REUSE lint clean, gitleaks clean, `trivy fs` clean, all component
  includes and base images digest-pinned.

## 8. Open items

1. Coverage threshold for Go. The estate's stated gate is 75 % line coverage from Cobertura for PHP
   (`engineering/quality-gates.md`); Go reports through the `coverage:` regex instead. Pick a number
   for YASRT and say whether it blocks.
2. `chore → patch` combined with Renovate's `chore(deps)` commits — every dependency bump becomes a
   release. Proposal in SPEC §14.1 is to keep the rule but default `ignore.authors: [renovate-bot]`
   for `package`/`extension`.
3. Whether `sign: required` is mandated for any repo class before the OIDC signing path exists.
4. `build` and `revert` are allowed commit types in this estate but appear in no rule list. Proposal:
   both no-release by default.
5. The name. YASRT is honest and unpronounceable.
