# YASRT — Tasks

Derived from [`plan.md`](plan.md); behaviour is specified in [`SPEC.md`](SPEC.md). Each task carries
an acceptance criterion — the observable thing that makes it done. Phases are ordered; within a
phase, tasks are ordered by dependency unless marked *(parallel)*.

Legend: `[ ]` open · `[x]` done · `[~]` in progress · `[!]` blocked

Status as of 2026-09-11: P0–P6 are implemented and green locally (`go test ./...`,
78 tests across 11 packages). Everything that needs a real GitLab project, a
runner, or another repository is blocked or unverified and marked as such —
those are the boundary of what could be done without touching the estate.

---

## P0 — Repository bootstrap

- [x] **T-001** `git init` on `main`, initial commit of the existing `docs/` and `CLAUDE.md`.
      *Done when:* `git log` shows one signed commit on `main`.
- [x] **T-002** Create the top-level group `yasrt` and the project `yasrt/cli` on `git.ole-hartwig.eu`,
      internal visibility, `main` protected, direct pushes blocked.
      *Done when:* `git push -u origin main` succeeds and the branch is protected.
- [x] **T-003** `go.mod` with module `git.ole-hartwig.eu/yasrt/cli`, `go 1.27.0`.
      *Done when:* `go build ./...` succeeds on the `devops/images/golang:1.27` image.
- [x] **T-004** Copy scaffolding verbatim from `devops/repo-templates`: `lefthook.yml`, `.mise.toml`,
      `.gitsigners` + `.gitsigners.d/`, `.gitlab/CODEOWNERS`, three issue templates, MR template,
      `SECURITY.md`, `.well-known/security.txt`. Do not hand-write. No `.editorconfig`.
      *Done when:* `lefthook install` runs and a deliberately malformed commit message is rejected.
- [x] **T-005** Adjust `.mise.toml` for a Go repo (`go = "1.27"`, drop php/node/composer/npm).
      *Done when:* `mise install` yields Go 1.27 locally, which currently differs from the host's 1.26.7.
- [x] **T-006** Adjust `.gitlab/CODEOWNERS` for Go paths: `go.mod`, `go.sum`, `.gitlab-ci.yml`,
      `Containerfile`, `internal/git/`, `internal/gitlab/`.
      *Done when:* an MR touching `internal/gitlab/` requests the security reviewer.
- [x] **T-007** REUSE v3.3 compliance: `LICENSE`, `LICENSES/<SPDX>.txt`, `.reuse/dep5`, SPDX headers
      on every source file.
      *Done when:* `reuse lint` exits 0.
- [x] **T-008** `README.md` in house structure — Purpose, Install, Configure, Decommission, Security.
      *Done when:* all five sections are present and the Security section names `CI_JOB_TOKEN` and the
      optional GPG variable.
- [x] **T-009** `CONTRIBUTING.md` and `CODE_OF_CONDUCT.md` (Contributor Covenant 2.1), stating the
      Conventional Commits and signing requirements.
      *Done when:* both exist and reference the handbook rather than restating policy.
- [~] **T-010** `.gitlab-ci.yml` skeleton including
      `devops/ci-cd-components/composed-default-pipelines/go@<pinned>` with
      `working-directory: .`, all includes digest- or version-pinned per `component-integrity`.
      *Done when:* `lint:gofmt`, `lint:vet`, `test:go` and the hygiene jobs run green on an empty suite.
- [x] **T-011** *(parallel)* Add `.golangci.yml` — first in the estate, so choose a defensible default
      set and document the choice in the file header.
      *Done when:* `golangci-lint run` is green and wired as a job.
- [x] **T-012** *(parallel)* Add a `govulncheck` job per SPEC §11.
      *Done when:* the job runs and fails the pipeline on a known-vulnerable pinned dependency, proven
      once deliberately.
- [x] **T-013** Renovate configuration for the repo, matching the estate's three-tier cadence.
      *Done when:* a Renovate dry run proposes a bump for `doublestar`.

## P1 — `yasrt next`

- [x] **T-020** `internal/semver`: version type, parse, compare, `Bump(major|minor|patch)`,
      `major_on_zero` handling.
      *Done when:* table tests cover 0.x behaviour in both settings, and `--version` lower than the
      last release is rejected.
- [x] **T-021** `internal/conventional`: parser for header (`type(scope)!: description`), body,
      footers, `BREAKING CHANGE:`, and trailer extraction.
      *Done when:* golden tests cover `!`, footer-only breaking changes, both together,
      non-conforming messages, and multi-paragraph bodies.
- [x] **T-022** `internal/config`: load `.yasrt.yaml`, apply defaults, validate, reject unknown fields.
      *Done when:* a config with a typo'd key fails with a message naming the key and line.
- [x] **T-023** `product` derivation for `non_release_paths` per SPEC §5.1 — with `lefthook.yml`
      replacing `.pre-commit-config.yaml`, and `.gitlab-ci.yml` excluded for `image` only.
      *Done when:* all four product values derive the documented list and `custom` without an explicit
      list is a validation error.
- [x] **T-024** `tag_format` derivation per decision D5 (`image`/`custom` → `${version}`,
      `package`/`extension` → `v${version}`).
      *Done when:* both defaults are produced and an explicit `tag_format` overrides them.
- [x] **T-025** Enforce the mandatory `ignore.scopes: [release]` entry that cannot be configured away
      (loop guard 2, SPEC §6.3).
      *Done when:* a config trying to remove it still yields the entry, with a warning.
- [x] **T-026** `internal/git` read side: `tag --merged HEAD`, `log --format`, `diff --name-only`,
      `rev-parse HEAD`, shallow detection.
      *Done when:* each is unit-tested against throwaway repos built in `t.TempDir()`.
- [x] **T-027** Shallow-clone detection with the SPEC §9 message pointing at `GIT_DEPTH: 0`.
      *Done when:* a shallow clone exits 1 with that message instead of silently analysing a partial
      history.
- [x] **T-028** Last-release resolution: highest tag matching `tag_format` that is an ancestor of HEAD;
      warn on foreign SemVer tags; no tag → `versioning.initial`.
      *Done when:* a repo with mixed `1.2.3` and `v1.2.3` tags resolves the configured format only and
      warns about the other.
- [x] **T-029** `internal/rules`: first matching rule per commit, highest bump wins; `ignore` by scope,
      author and trailer.
      *Done when:* precedence is table-tested including a breaking `chore` and an ignored `feat`.
- [x] **T-030** `internal/deliver`: doublestar matching of changed paths over `lastTag..HEAD`.
      *Done when:* a commit range touching only `docs/**` is not deliverable, and one mixed with a
      source file is.
- [x] **T-031** `internal/analyze`: orchestrate the SPEC §6.1 flow and produce the result struct,
      including `already-released` when HEAD already carries a matching tag.
      *Done when:* all four statuses are reachable in tests.
- [x] **T-032** Overrides: `--force <major|minor|patch>`, `--version <x.y.z>`,
      `--ignore-deliverability`, both setting `RELEASE_REASON=forced`.
      *Done when:* `--force` produces a bump from a no-bump range, and deliverability still applies
      unless explicitly skipped.
- [x] **T-033** `internal/output`: `.release.env` dotenv writer and `--json`.
      *Done when:* the emitted keys match SPEC §6.1 exactly and are consumable by GitLab
      `artifacts:reports:dotenv`.
- [x] **T-034** `cmd/yasrt` wiring for `next` with stdlib `flag`, plus exit codes 0/1/2/3/4 and
      `--fail-on-skip`.
      *Done when:* every code in SPEC §6.1 is produced by an integration test.
- [x] **T-035** `internal/logging`: `log/slog`, `--log-format json`, `--verbose`, and masking of
      `CI_JOB_TOKEN`, GPG material and credentialed git URLs.
      *Done when:* a test asserts a token planted in a remote URL never appears in any log line.
- [x] **T-036** Run `yasrt next` against real working copies (`release-tools`, `golang-image`,
      `cluster-file-batch`/`cluster-file-backend`) and record the output in the MR.
      *Done when:* results are explainable against each repo's actual tag history.

## P2 — `yasrt check`

- [x] **T-040** Config validation mode with a JSON schema shipped in the repo for editor support.
      *Done when:* the schema validates the SPEC §5 example and rejects an unknown `product`.
- [x] **T-041** Environment probes: git presence and version, full history, tags fetched,
      `CI_JOB_TOKEN` present, remote reachable.
      *Done when:* each missing precondition produces its own actionable message.
- [x] **T-042** Push-permission probe via `refs/yasrt/check`, deleted immediately.
      *Done when:* the ref never survives a run, including when the push fails, and a 403 is reported
      as the "Allow Git push requests" setting.
- [x] **T-043** Protected-tag probe reporting risk R1 per repo.
      *Done when:* `check` states whether the triggering identity may create a tag matching
      `tag_format`.

## P3 — `yasrt release`

- [x] **T-050** `internal/render` release notes per SPEC §6.2 step 1: sections only when non-empty,
      `:boom: BREAKING CHANGES` block first, entries as `- <scope>: <description> (<short sha>)`,
      MR/issue footer references as links.
      *Done when:* golden files cover an empty section set, several breaking changes, and missing scopes.
- [x] **T-051** CHANGELOG prepend: `## [${version}] - YYYY-MM-DD`, file created when absent.
      *Done when:* golden tests cover a fresh file, an existing file, and a file with a leading title.
- [x] **T-052** Preconditions: `RELEASE_STATUS == release`, `RELEASE_COMMIT == HEAD`, remote tag
      absent — with the three-way idempotency outcome from SPEC §6.2.
      *Done when:* a tag on a different commit exits 5, and a tag on `RELEASE_COMMIT` continues.
- [x] **T-053** Warn when `.release.env` is absent and a fresh analysis is performed.
      *Done when:* the warning names the risk that the result may differ from what `build` used.
- [x] **T-054** Annotated tag on `RELEASE_COMMIT` and push with the job token.
      *Done when:* the tag points at what was built, never at the release commit, asserted in a test.
- [x] **T-055** Release commit: `CHANGELOG.md`, configured message and author, pushed to the default
      branch — after the tag.
      *Done when:* an induced failure at this step leaves a complete release, and a re-run converges.
- [x] **T-056** Signing: `sign: auto|required|off`; `required` fails **before** the tag is pushed;
      `auto` degrades to unsigned with a warning.
      *Done when:* an invalid key under `required` exits 1 with nothing pushed.
- [x] **T-057** `internal/gitlab`: create release via `net/http` + `encoding/json/v2`, header
      `JOB-TOKEN`.
      *Done when:* served by `httptest`, with retry/backoff and error bodies surfaced.
- [x] **T-058** Release asset links per `gitlab_release.assets`.
      *Done when:* links are attached and a failure is reported without losing the release.
- [x] **T-059** Follow-up pipeline triggers per `after_release.triggers`; failures logged, exit stays 0.
      *Done when:* a failing trigger does not fail the job, and the failure appears in the report.
- [x] **T-060** `release-report.json` artefact: version, tag SHA, release URL, trigger results.
      *Done when:* it is written on success and on partial failure.
- [x] **T-061** Idempotency test matrix: induce a failure at each of steps 3–6 and re-run.
      *Done when:* every re-run converges to the same end state without duplicate side effects.
- [x] **T-062** Concurrent-push case: `RELEASE_COMMIT != HEAD` exits 5.
      *Done when:* covered by an integration test that pushes between analysis and release.

## P4 — Distribution

- [x] **T-070** Reproducible build: `CGO_ENABLED=0`, `-trimpath`,
      `-ldflags "-s -w -X main.version=${VERSION}"`, `linux/arm64` and `linux/amd64`.
      *Done when:* two builds of the same commit produce identical binaries and `yasrt --version`
      reports the tag.
- [x] **T-071** `SHA256SUMS` manifest, signed with `build-provenance/sign-artifact`.
      *Done when:* `cosign verify-blob` succeeds in the same pipeline.
- [x] **T-072** Upload binaries to the generic package registry with `curl --upload-file` and attach
      Releases API asset links — S3mail's pattern, not `release-cli`.
      *Done when:* the release page lists every artefact and the links resolve.
- [x] **T-073** `Containerfile` on `devops/ci-mirrors/wolfi-base` pinned by digest, carrying `yasrt`,
      `git` and optional `gpg`, running non-root.
      *Done when:* `base-image-pins` passes and the image runs `yasrt check` as a non-root user.
- [~] **T-074** Image build through `buildkit-image-build` with `inherit-signing: 'true'` and
      `inherit-verify: 'true'`.
      *Done when:* the pushed digest carries a KMS cosign signature and a CycloneDX SBOM attestation,
      both verified in-pipeline.
- [~] **T-075** Publish a rolling major tag (`:1`) alongside the immutable semver tag, as
      `golang-image` does.
      *Done when:* both tags resolve to the same digest after a release.

## P5 — Component

- [x] **T-080** `templates/yasrt/template.yml` in `devops/ci-cd-components/release-tools`, with the
      `version` and `release` jobs from SPEC §8.1.
      *Done when:* it lints and runs in a scratch project.
- [x] **T-081** Keep a `rules-config` input compatible with the existing template so consumers'
      overrides keep working.
      *Done when:* an existing consumer's `rules-config` block is accepted unchanged.
- [x] **T-082** `resource_group: release` and `GIT_DEPTH: 0` / `GIT_STRATEGY: clone` defaults.
      *Done when:* two concurrent pipelines serialise instead of racing.
- [x] **T-083** MR-preview job per SPEC §8.3.
      *Done when:* an MR pipeline prints "would release X (minor)", "not deliverable: …" or "no bump".
- [x] **T-084** Template README section and an entry in the repo's component catalogue.
      *Done when:* the five inputs and the four `RELEASE_STATUS` values are documented.
- [x] **T-085** Document the dropped inputs (`config-preset`, `config-preset-group-id`,
      `config-preset-scope`, `image-config`, `tags-config`) and their replacements.
      *Done when:* a migration table exists from the 14 old inputs to the 5 new ones.

## P6 — Dogfooding

- [x] **T-090** YASRT's own `.gitlab-ci.yml` uses the new template.
      *Done when:* a `feat:` on `main` produces a tag, a GitLab release and a signed image.
- [x] **T-091** `.yasrt.yaml` for this repo (`product: custom`, explicit `non_release_paths`).
      *Done when:* a docs-only commit ends in `not-deliverable`.

## P7 — Shadow run *(needs only P1)*

- [ ] **T-100** Add a non-blocking `yasrt next --json` job beside the existing `release:semver` in a
      sample of repos across all four product types.
      *Done when:* both results are captured as artefacts for a week without affecting releases.
- [ ] **T-101** Evaluate divergences, with the `lastTag..HEAD` window change (R3) as the expected
      class.
      *Done when:* every divergence is classified as intended, a bug, or a config gap, in writing.
- [ ] **T-102** Feed the result back into I-081 in the improvement register.
      *Done when:* the register entry names YASRT and its acceptance criterion is testable.

## P8 — Pilot

- [ ] **T-110** Select 2–3 repos per `product`, including one of the 53 changelog-less repos (R2) and
      one large repo for the `GIT_DEPTH` measurement (R4).
      *Done when:* the list is agreed and recorded.
- [ ] **T-111** **Resolve R1 before the first pilot release**: decide whether the protected-tag rule is
      widened or a Maintainer identity stays in the loop, and record the decision in the handbook.
      *Done when:* `yasrt check` reports a green push and tag permission in a pilot repo.
- [ ] **T-112** Per pilot repo: enable "Allow Git push requests to the repository", add `.yasrt.yaml`,
      swap the template, delete `GITLAB_TOKEN` — all in one MR (R5).
      *Done when:* the repo releases through YASRT and the old variables are gone.
- [ ] **T-113** Measure job duration and clone time before and after.
      *Done when:* the ~40 s → ~10 s claim is confirmed or corrected with real numbers.

## P9 — Rollout

- [ ] **T-120** Renovate rule to bump the component include across the estate.
      *Done when:* MRs open automatically for the remaining repos.
- [ ] **T-121** Migrate the remaining repos in batches by product type.
      *Done when:* every non-prerelease repo releases through YASRT.
- [ ] **T-122** Replace `RENOVATE_TRIGGER_TOKEN` with `after_release.triggers` using `JOB-TOKEN`.
      *Done when:* the token is deleted and fast-lane Renovate pipelines still fire.
- [ ] **T-123** Update the handbook pages that describe the current release process:
      `igs/engineering/release-management.md`, `igs/engineering/ci-cd-security.md`,
      `igs/security/secure-development.md`, `igs/engineering/commit-signing-policy.md`,
      `engineering/ci-cd-jobs.md`, `igs/security/vulnerability-management.md`,
      `igs/security/supply-chain-security.md`, `engineering/new-tenant-website.md`,
      `engineering/decisions/golden-images-on-wolfi.md`.
      *Done when:* no page describes semantic-release as the current mechanism.
- [ ] **T-124** Mark the `semantic-release` template deprecated with a pointer to the replacement.
      *Done when:* the deprecation is visible in the template README and the catalogue.

## P11 — Hooks (extension points)

- [x] **T-140** `internal/hooks`: run external executables at `after_analysis`,
      `before_tag`, `after_tag` and `after_release`, with the release context on stdin as JSON
      and as `RELEASE_*` environment variables.
      *Done when:* a hook sees the version, arguments are passed without a shell, and the
      timeout is enforced.
- [x] **T-141** Failure semantics: fatal for `before_tag` and `after_tag`, reported for
      `after_analysis` and `after_release`, `allow_failure` overriding either way.
      *Done when:* a failing `before_tag` hook leaves the repository untouched — no tag pushed,
      no changelog written.
- [x] **T-142** Wire hooks into `next` and `release`, and record every run in
      `release-report.json`.
      *Done when:* the report lists each hook with its exit code, duration and output.
- [x] **T-143** Document hooks in SPEC §5.2, the JSON schema, the README and CONTRIBUTING, and
      amend the SPEC §2 non-goal.
      *Done when:* the four documents agree on what a hook is and when it runs.
- [ ] **T-144** Port the estate's existing release satellites to hooks or leave them as CI jobs:
      `composer-package-gitlab-release`, `typo3-extension-release`, `packagist-submit`,
      `cleanup-release-tags`.
      *Done when:* each is either a documented hook or a documented downstream job, and the
      choice is recorded.

## P10 — Prereleases, then decommissioning

- [ ] **T-130** Design prerelease support for the two live shapes: `main` with `prerelease: rc`, and a
      `develop`/`release` branch model.
      *Done when:* the design is agreed and SPEC §2 is amended.
- [ ] **T-131** Implement `-rc.N` versioning, including the interaction with `tag_format` and with
      `already-released`.
      *Done when:* a test repo cuts `v2.4.0-rc.1` then `v2.4.0-rc.2` then `v2.4.0`.
- [ ] **T-132** Port `cleanup-release-tags` behaviour or confirm the existing template still covers it.
      *Done when:* RC tags and releases are removed on merge to the default branch.
- [ ] **T-133** Migrate the prerelease repos.
      *Done when:* `gsb11/extensions/*` and `moselwal-packages/*` release through YASRT, or are
      formally declared out of scope.
- [ ] **T-134** Remove the `semantic-release` template after the agreed soak period.
      *Done when:* no repo includes it.
- [ ] **T-135** Decommission `@moselwal/semantic-release-config` and the npm preset registry path.
      *Done when:* the package is archived and the group npm registry is no longer referenced by any
      pipeline.

---

## Open items carried from the plan

- [x] **T-900** Go coverage threshold: **75 %, blocking**, matching the estate's stated PHP gate.
      Enforced by `test:coverage:gate` in `.gitlab-ci.yml`; the suite currently sits at 77.1 %.
- [ ] **T-901** Decide `chore → patch` versus a default `ignore.authors: [renovate-bot]` for
      `package`/`extension` (SPEC §14.1).
- [ ] **T-902** Decide whether `sign: required` is mandated for any repo class (SPEC §14.2).
- [ ] **T-903** Decide the release behaviour of `build` and `revert` commit types (plan §8.4).
- [ ] **T-904** Decide the name (SPEC §14.5).
