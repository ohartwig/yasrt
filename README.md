<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
SPDX-License-Identifier: MIT
-->

# yasrt

One static Go binary that decides whether a repository should be released, and
publishes the release **after** the artefact has been built. It replaces the npm
`semantic-release` chain in `devops/ci-cd-components/release-tools`.

- **The tag confirms, it does not announce.** `version` decides, `build` runs,
  and only then does `release` tag. A failed build leaves no tag behind.
- **No stored secrets.** The job's own token (`CI_JOB_TOKEN`, `GITHUB_TOKEN`)
  is the only credential required. A signing key is optional, and `sign: auto`
  degrades to unsigned rather than failing.
- **Three forges.** GitLab, GitHub and Forgejo — the platforms semantic-release
  publishes to — detected from the job environment, no configuration needed.
- **One file per repository.** `.yasrt.yaml`, with almost everything derived
  from a single `product:` field.
- **Three dependencies.** A YAML parser, a glob matcher, and git itself.
- **Extensible out of process.** Hooks call any executable at defined points and
  hand it the release context — the extensibility of a plugin system without a
  package manager in the release path.

## Purpose

The chain being replaced spent about 21 seconds of every 40-second release job
on `npm install`, pulled 384 to 516 unpinned transitive packages, and answered
the deliverability question against the wrong commit range — a genuine `fix:`
bundled with a `.gitlab-ci.yml` change never shipped (improvement item I-081).
It also tagged before building, which once left 17 tags with no artefact behind
them.

## Install

In CI, use the image:

```yaml
include:
  - component: $CI_SERVER_HOST/devops/ci-cd-components/release-tools/yasrt@1
```

Locally:

```sh
go install git.ole-hartwig.eu/yasrt/cli/cmd/yasrt@latest
```

Binaries for `linux/amd64` and `linux/arm64` are published to this project's
generic package registry with a signed `SHA256SUMS` manifest.

On GitHub Actions or Forgejo Actions the same binary runs with the workflow's
token:

```yaml
- run: yasrt next && <build> && yasrt release
  env:
    GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}   # FORGEJO_TOKEN on Forgejo
```

The job needs `contents: write`. Where the platform cannot be told apart from
its variables, `--forge gitlab|github|forgejo` (or `YASRT_FORGE`) says so.

## Configure

`.yasrt.yaml` in the repository root. The minimum is one line:

```yaml
product: image    # image | package | extension | custom
```

`product` derives both the paths that cannot constitute a release and the tag
format, matching what this estate already does:

| product | non-release paths | tag format |
|---|---|---|
| `image` | `CHANGELOG.md`, `README.md`, `docs/**`, `lefthook.yml` — **not** `.gitlab-ci.yml`, which builds the image | `1.2.3` |
| `package` | the above **plus** `.gitlab-ci.yml`, `.gitlab/**` | `v1.2.3` |
| `extension` | as `package` | `v1.2.3` |
| `custom` | nothing derived; `non_release_paths` is required | `1.2.3` |

### Hooks

Extension points, in the order they run. A hook is any executable; it gets the
release context as JSON on stdin and as `RELEASE_*` environment variables, and
signals failure with a non-zero exit code.

```yaml
hooks:
  before_tag:
    - run: ./scripts/policy-check
      name: policy gate
      timeout: 90s
  after_tag:
    - run: ./scripts/publish-composer.sh
  after_release:
    - run: ./scripts/notify.sh
      allow_failure: true
```

| event | when | failure |
|---|---|---|
| `after_analysis` | in `next`, after the decision | reported |
| `before_tag` | in `release`, before any write | **aborts**, repository untouched |
| `after_tag` | tag on the remote, release commit not yet made | **aborts** |
| `after_release` | last, after the forge release and triggers | reported |
| `on_failure` | when `release` is about to exit non-zero; told `error` and `failed_step` | reported |

`args` are passed verbatim — no shell, so nothing is word-split or
glob-expanded. `allow_failure` overrides the default either way. Hooks are never
handed a credential; one that needs a token reads it from its own environment.

Everything else is optional and documented in
[`schema/yasrt.schema.json`](schema/yasrt.schema.json), which editors pick up
for completion and which `yasrt check` validates against. The full surface is
specified in [`docs/SPEC.md`](docs/SPEC.md) §5.

## Use

```sh
yasrt next --output .release.env   # decide; writes nothing to the repository
yasrt release                      # publish; idempotent
yasrt check                        # validate config and probe the environment
```

`next` always exits `0` once the analysis completed — the answer is in
`RELEASE_STATUS`, which is one of `release`, `no-bump`, `not-deliverable` or
`already-released`. Add `--fail-on-skip` to turn the last three into exit codes.

| exit | meaning |
|---|---|
| 0 | analysis or release completed |
| 1 | configuration or git error |
| 2 | invalid arguments |
| 3 | `no-bump`, with `--fail-on-skip` |
| 4 | `not-deliverable`, with `--fail-on-skip` |
| 5 | the repository moved: HEAD changed, or the tag exists on another commit |

The dotenv keys `RELEASE_STATUS`, `RELEASE_VERSION`, `RELEASE_TAG`,
`RELEASE_PREVIOUS`, `RELEASE_BUMP`, `RELEASE_REASON` and `RELEASE_COMMIT` are a
public interface; consuming pipelines switch on them.

Because GitLab evaluates `rules:` at pipeline creation — before `.release.env`
exists — downstream jobs must check the status **in the script**, never in
`rules:`:

```yaml
build:
  needs: [version]
  script:
    - '[ "$RELEASE_STATUS" = "release" ] || exit 0'
    - docker build --label org.opencontainers.image.version=$RELEASE_VERSION .
```

## Develop

```sh
go test ./...                                   # unit, golden and integration
go test ./internal/render -update               # rewrite golden files
go test ./internal/analyze -run TestForce -v     # one test
gofmt -l . && go vet ./...
```

Integration tests build throwaway git repositories and serve the GitLab, GitHub
and Forgejo release APIs from `httptest`. Nothing in the suite touches a real
instance of any of them.

## Decommission

Remove the component include and `.yasrt.yaml`, and put the previous
`semantic-release` include back. yasrt holds no state of its own: everything it
produces is a tag, a commit, a release on the forge and a job artefact. On
GitLab, disable the project setting **Allow Git push requests to the
repository** afterwards if nothing else needs it.

## Forges

| | GitLab | GitHub | Forgejo |
|---|---|---|---|
| detected by | `GITLAB_CI` | `GITHUB_ACTIONS` on github.com | `FORGEJO_ACTIONS`, or `GITHUB_SERVER_URL` elsewhere |
| token | `CI_JOB_TOKEN` | `GITHUB_TOKEN` | `FORGEJO_TOKEN` or `GITHUB_TOKEN` |
| release links (`assets[].url`) | attached to the release | listed in the release body | listed in the release body |
| uploads (`assets[].path`) | generic package registry, linked from the release | attached to the release | attached to the release |
| `after_release.triggers` | pipeline trigger API | not available — reported per trigger, exit stays `0` | as GitHub |
| `yasrt check` authority probe | compares branch and tag protection | not probed | not probed |

Forgejo is identified before GitHub because its runner sets the `GITHUB_*`
variables for compatibility; a naive check would post a Forgejo token to
api.github.com. `gitea` is accepted as a synonym for `forgejo`.

## Signing

`release_commit.sign` takes `auto` (sign when a key is present), `required`
(fail before anything is written if it is not) or `off`. The key comes from
`GPG_SEM_REL_B64` and may be **either an OpenPGP or an OpenSSH private key**,
base64-encoded — yasrt detects which from the material rather than asking you to
declare it. An SSH key is written to a 0600 file for the duration of the run and
removed afterwards; GitLab verifies SSH signatures, and this estate already
trusts SSH keys for human commits through `.gitsigners`.

Both paths are tested by generating real keys and verifying the resulting tag
and commit with `git verify-tag` / `git verify-commit`, rather than by asserting
that yasrt called git with the right flag.

## Who may release

A `CI_JOB_TOKEN` push acts as the user who triggered the pipeline — on the
default branch, whoever merged. So the tag push succeeds exactly when the
weakest role allowed to merge is also allowed to create the release tag. If
merging is Maintainer-only, a Maintainer-only protected tag is consistent and
nothing needs loosening. `yasrt check` compares the two and reports a mismatch
as fatal before the first release depends on it.

## Security

The only required credential is `CI_JOB_TOKEN`, taken from the job environment.
`GPG_SEM_REL_B64` is optional and is the sole long-lived secret; it is never
written to disk outside the job and never logged. Tokens and credentials
embedded in git URLs are masked in every log line and in the run report by the
log handler itself, not at call sites.

Pushing requires the project setting **Allow Git push requests to the
repository**, and the triggering identity must be allowed to create the
protected tag. `yasrt check --push` proves both before a real release depends on
them. Report vulnerabilities as described in [SECURITY.md](SECURITY.md).
