<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
SPDX-License-Identifier: MIT
-->

# Open-source readiness review

Reviewed on 2026-09-13 at 1.6.1, against the question: *what has to change
before a stranger can use this from a public repository without knowing the
estate it grew up in?* Code and documentation, in that order. Findings carry a
severity and a proposed action; nothing here is fixed yet.

Numbers: 6 057 lines of Go in 18 packages, 5 000 lines of tests, 78 % statement
coverage, two dependencies, `go vet` and `golangci-lint` clean, ~650 shadow
verdicts with zero version disagreements over three days.

## Verdict

The core is publishable: the decision logic, the release order, the forge
abstraction and the test discipline are sound, and the invariants are stated
where they are enforced. What is **not** publishable yet is everything that
assumes the reader is us — defaults, identities, paths and prose that only make
sense inside `git.ole-hartwig.eu`. Those are the blockers; the rest is polish.

## Blockers

### B1 — Go cannot install the tool by version

Tags are bare (`1.6.1`). Go modules require `v`-prefixed semver tags:
`go install git.ole-hartwig.eu/yasrt/cli/cmd/yasrt@v1.6.1` fails, `@latest`
falls back to a pseudo-version of `main`, and the README's install line is
therefore misleading. The module also lives under a host that must answer
`?go-get=1` for the path to resolve at all — GitLab does, but a mirror on
GitHub would need a different module path or a vanity import.

*Action:* set `tag_format: v${version}` for this repository (a self-migration:
the next release becomes `v1.7.0`; existing bare tags stay), and decide the
canonical module path before the first public release — changing it later
breaks every `go install`.

### B2 — Estate defaults are baked into the binary

| Where | Value | Problem |
|---|---|---|
| `config.DefaultReleaseAuthor` | `KOH Release Bot <release-bot@ole-hartwig.eu>` | a stranger's releases carry our identity |
| `product: image` derivation | `lefthook.yml`, `.gitlab/**` in `non_release_paths` | tooling choices of this estate |
| default rules | `chore → patch` | the estate preset; semantic-release's `conventionalcommits` default is `chore → nothing`, `revert → patch`. A public user gets releases they did not expect |
| `configureIdentity` fallback | `GITLAB_USER_NAME` / `GITLAB_USER_EMAIL` | GitLab-only variables in a three-forge tool |
| `internal/gitlab` doc comment, `analyze`, `render`, `semver`, `deliver`, `layers` | "this estate", "I-081", "gsb11", "moselwal" | comments that explain history a public reader cannot follow |

*Action:* neutral defaults in the binary (`yasrt <noreply@…>`-style author or
none; `CHANGELOG.md`, `README.md`, `docs/**` only; semantic-release's default
rules), and the estate's values move into the layered defaults file the CI
component already ships — that mechanism exists precisely for this. Rewrite
the comments to state the reason without the estate reference; where the
history matters, point at `docs/design/plan.md`.

### B3 — Documentation written for insiders

- `docs/SPEC.md`, `docs/design/plan.md`, `docs/design/tasks.md` reference improvement items,
  risk ids, ~40 internal project paths and pilot findings. They are the
  design record and worth keeping — but not as the front door.
- `SECURITY.md` is the estate's policy: its scope lists `devops/**` and
  `development/moselwal/**`, its advisories URL is the group's. Wrong for a
  standalone repository.
- `README.md` installs via a GitLab CI component and a generic package
  registry; a GitHub user has no path. The GitHub/Forgejo section is three
  lines.
- `CLAUDE.md` names the estate's images, handbook and stage order.
- `CONTRIBUTING.md` assumes `mise`, `lefthook`, `.gitsigners` and signed
  commits — fine as a policy, but say which of it applies to outside
  contributors.

*Action:* keep `docs/` as history under `docs/design/` with a one-paragraph
preface, write a neutral `SECURITY.md` (contact + disclosure window, no scope
list), and restructure the README: what it is, install (binary release,
`go install`, container image), a GitLab CI example without the component, a
GitHub Actions example, then configuration. The comparison document stays.

### B4 — Two copies of the CI component

`component/` holds "staged" copies of the release-tools templates. They have
drifted from the released ones (67 lines). A public reader takes the copy in
this repository as the truth.

*Action:* delete `component/` and link the release-tools catalogue entry, or
make this repository the source and publish from here. Not both.

## Should fix before 1.0

### S1 — Token in the push URL

`authenticatedURL` builds `https://gitlab-ci-token:<token>@host/…` and hands it
to `git push` as an argument. It is masked in every log line, but it is visible
in the process list of the job container for the duration of the push. On a
shared runner that is a wider audience than a log.

*Action:* pass the credential through `git -c credential.helper=…` with an
inline helper, or `GIT_ASKPASS`, and keep the URL clean. `git.Repo.Push`
already centralises the call.

### S2 — Hook output lands in the report unmasked

`hooks.Result.Output` is captured verbatim into `release-report.json`, a job
artefact. Hooks inherit the full environment by design (documented: "a hook
that needs a token reads it from its own environment"), so a hook that echoes
its environment writes the job token into an artefact.

*Action:* run hook output through the same masker `git.Repo` uses for the
token and the signing key, and say in the hooks documentation that stdout is
recorded.

### S3 — OpenPGP key stays in the ambient keyring

`configureGPGSigning` imports into whatever keyring `gpg` finds and returns a
no-op cleanup. In a job container that is fine; run locally it leaves the
release key in the developer's keyring. The SSH path already uses a private
temp dir and removes it.

*Action:* `GNUPGHOME` set to a temp dir for the run, removed in cleanup —
symmetric with the SSH path.

### S4 — Retrying a `POST` that may have succeeded

`rest.do` retries every request on 5xx/429, including `CreateRelease`. A
create that succeeded server-side but timed out on the wire is retried and
answers 409/422 — which is then reported as a failure of a release that
exists. `GetRelease` runs before the first attempt, not between attempts.

*Action:* on a retryable error after a `POST`, re-check with `GetRelease`
before retrying; treat "exists with this tag" as success.

### S5 — `release.go` is 917 lines

Signing, identity, changelog commit, publishing, triggers and the run itself
share one file. Each is self-contained already.

*Action:* split into `run.go`, `commit.go`, `publish.go`, `signing.go`. No
behaviour change.

### S6 — Platform statement

Nothing says which platforms are supported. The binary is built for
`linux/amd64` and `linux/arm64`; the tests assume a POSIX shell (`#!/bin/sh`
hooks) and `git`, `gpg`, `ssh-keygen` on `PATH` (the signing tests skip
without them). Windows is untested and probably broken around paths and the
hook runner.

*Action:* state "Linux and macOS; Windows unsupported" in the README, and add
`darwin/arm64` to the release build since the tool is also meant to run
locally (`yasrt next` on a developer machine is a documented use).

## Nice to have

- **N1** `errors.As` (6 uses) → `errors.AsType[T]`; the codebase otherwise
  uses Go 1.27 idioms consistently.
- **N2** `internal/gitlab` is now the protection-rule probe only; rename to
  `internal/gitlabprobe` or fold into `forge` so the package name says what it
  does.
- **N3** `tools/shadow-report.py` needs `glab` and the estate's project
  layout. Either generalise (`--host`, `--group`) or move to an
  `ops/`/`tools/` directory with a note that it is operator tooling.
- **N4** A `docs/examples/` directory with one complete `.gitlab-ci.yml` and
  one `.github/workflows/release.yml` — the GitHub probe from 2026-09-11 is the
  workflow, already proven.
- **N5** The JSON schema has no `$id`/`$schema` URL a public editor can fetch;
  publish it at a stable URL once the module path is settled.
- **N6** `renovate.json` and `.mise.toml` pin the estate's mirrors; harmless
  but confusing to a fork.
- **N7** `yasrt check` says "not probed on github" for the release-authority
  invariant. A minimal probe exists on both platforms (branch protection /
  rulesets API); not needed for 1.0, but the message should point at what to
  check by hand.

## What is right and should stay

- The step order and its justification, stated at the top of `release.go` and
  enforced by tests (`TestTagPointsAtTheBuiltCommit`, idempotent re-run,
  moved-branch rebuild).
- Job-token-only credentials, with `check` failing closed.
- Out-of-process hooks with no shell, no credentials in the payload, and
  explicit failure semantics per event.
- `next` writes nothing, `release` is idempotent, and both are documented as a
  public interface with exit codes and dotenv keys.
- Tests that build real repositories and serve real API shapes; the test
  fixture that forces its own git identity, after the lesson that cost five
  flakes.
- Two dependencies.

## Proposed order

1. B1 + module path decision (one afternoon, but it gates everything public).
2. B2 (defaults out of the binary, estate values into the component's
   defaults file) — this is also the correct shape for the estate itself.
3. S1–S3 (security hygiene), S4.
4. B3 + B4 (docs and the duplicate component).
5. S5, S6, N1–N7.
