<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
SPDX-License-Identifier: MIT
-->

# yasrt

**Yet another semantic release tool** — one static Go binary that reads your
Conventional Commits, decides whether and what to release, and publishes the
release **after** the artefact has been built. A replacement for the
`semantic-release` npm chain, for people who want the same decisions without a
package manager in the release path.

- **The tag confirms, it does not announce.** `yasrt next` decides, your build
  runs, and only then does `yasrt release` tag. A failed build leaves no tag
  behind.
- **No stored secrets.** The job's own token (`CI_JOB_TOKEN`, `GITHUB_TOKEN`,
  `FORGEJO_TOKEN`) is the only credential required. A signing key is optional;
  `sign: auto` degrades to unsigned rather than failing.
- **Three forges.** GitLab, GitHub and Forgejo, detected from the job
  environment.
- **One file per repository**, almost everything derived from a single
  `product:` field — and organisation-wide defaults layered underneath, so most
  repositories need no file at all.
- **Two dependencies** (a YAML parser and a glob matcher) plus `git` itself.
- **Extensible out of process.** Hooks call any executable at defined points
  and hand it the release context: a plugin system's reach without its supply
  chain.

## Why

`semantic-release` gets the decisions right and the mechanics wrong for CI:
it tags before anything is built, resolves hundreds of unpinned packages at
run time, and judges "did anything shippable change?" against the push range
rather than the last release — so a real fix bundled with a CI change never
ships. yasrt keeps the decisions (same commit conventions, same version
arithmetic, same changelog shape) and fixes the mechanics. The full mapping is
in [docs/semantic-release-comparison.md](docs/semantic-release-comparison.md).

## Install

**Binary.** Every release ships `yasrt-linux-{amd64,arm64}` and `yasrt-darwin-{arm64,amd64}`
with a `SHA256SUMS` on the release page. Linux and macOS are supported;
Windows is not.

**Go.**

```sh
go install github.com/ohartwig/yasrt/cmd/yasrt@latest   # or a version: @v1.8.0
```

**Container.** `registry.ole-hartwig.eu/devops/images/yasrt:1` is the image the
CI examples below use: 30 MB of Wolfi with `git`, `gpg` and `ssh-keygen`. Its
recipe and pipeline are public at <https://github.com/ohartwig/yasrt-image>;
build your own from it if you would rather not pull ours.

## Use

Three commands, two of which write nothing:

```sh
yasrt next --output .release.env   # decide; writes nothing to the repository
yasrt check                        # validate config, probe the environment
yasrt release                      # publish; idempotent, safe to re-run
```

`next` leaves its decision in a dotenv file — `RELEASE_STATUS` is one of
`release`, `no-bump`, `not-deliverable` or `already-released`, with
`RELEASE_VERSION`, `RELEASE_TAG`, `RELEASE_PREVIOUS`, `RELEASE_BUMP`,
`RELEASE_REASON` and `RELEASE_COMMIT` beside it. Your build reads it;
`release` reads it back and refuses to tag anything other than the commit that
was analysed. These keys and the exit codes are a public interface:

| exit | meaning |
|---|---|
| 0 | analysis or release completed |
| 1 | configuration or git error |
| 2 | invalid arguments |
| 3 | `no-bump`, with `--fail-on-skip` |
| 4 | `not-deliverable`, with `--fail-on-skip` |
| 5 | the repository moved: HEAD changed, or the tag exists on another commit |

### GitLab CI

```yaml
stages: [version, build, release]

default:
  image:
    name: registry.ole-hartwig.eu/devops/images/yasrt:1
    entrypoint: [""]

version:
  stage: version
  variables: { GIT_DEPTH: 0 }          # yasrt needs the full history
  script: yasrt next --output .release.env
  artifacts:
    reports: { dotenv: .release.env }
  rules: [{ if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH }]

build:
  stage: build
  image: your/build-image
  script:
    - '[ "$RELEASE_STATUS" = "release" ] || exit 0'   # gate in the script, not in rules:
    - make build VERSION=$RELEASE_VERSION
  rules: [{ if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH }]

release:
  stage: release
  variables: { GIT_DEPTH: 0 }
  script: yasrt release
  rules: [{ if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH }]
```

GitLab evaluates `rules:` when the pipeline is created, before `.release.env`
exists — downstream jobs therefore gate on `RELEASE_STATUS` **in the script**.
The project must allow job-token pushes (*Settings → CI/CD → Job token
permissions → Allow Git push requests to the repository*), and whoever may
merge to the default branch must be allowed to create the release tag —
`yasrt check` verifies both. A push made with the job token starts no pipeline,
which is what keeps the release commit from releasing again.

A CI/CD component with this shape (`version`, `release`, a shared defaults
layer, `product` and `tag-format` as inputs) lives in
`devops/ci-cd-components/release-tools` on the same instance:
`include: [component: $CI_SERVER_HOST/devops/ci-cd-components/release-tools/yasrt@1]`.

### GitHub Actions and Forgejo Actions

```yaml
permissions: { contents: write }
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with: { fetch-depth: 0 }
      - run: yasrt next --output .release.env
      - run: make build                    # gate on RELEASE_STATUS inside
      - run: yasrt release
        env: { GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }} }   # FORGEJO_TOKEN on Forgejo
```

A push made with `GITHUB_TOKEN` starts no workflow, so the changelog commit
does not release itself. Where the platform cannot be told apart from its
variables, say `--forge gitlab|github|forgejo` or set `YASRT_FORGE`. Complete
files are in [docs/examples](docs/examples/).

## Configure

`.yasrt.yaml` in the repository root. The minimum is one line:

```yaml
product: image    # image | package | extension | custom
```

`product` derives the paths that cannot constitute a release and the tag
format:

| product | non-release paths | tag format |
|---|---|---|
| `image` | `CHANGELOG.md`, `README.md`, `docs/**` — **not** the CI file, which builds the image | `1.2.3` |
| `package` | the above **plus** `.gitlab-ci.yml`, `.gitlab/**`, `.github/**` | `v1.2.3` |
| `extension` | as `package` | `v1.2.3` |
| `custom` | nothing derived; `non_release_paths` is required | `1.2.3` |

The default rules are semantic-release's: a breaking change is a major, `feat`
a minor, `fix`, `perf` and `revert` a patch, everything else nothing.

**Organisation defaults.** `--defaults file` (repeatable, or a `:`-separated list in `YASRT_DEFAULTS`)
layers YAML files *under* the repository's own: maps merge, scalars and lists
replace, the repository wins. That is where a bot identity, `chore → patch`
for dependency bumps, or a hook manifest as a non-release path belong — stated
once, in the CI template every repository already includes, rather than in
every repository.

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
    - run: ./scripts/publish.sh
  after_release:
    - run: ./scripts/notify.sh
      allow_failure: true
  on_failure:
    - run: ./scripts/page-someone.sh
```

| event | when | failure |
|---|---|---|
| `after_analysis` | in `next`, after the decision | reported |
| `before_tag` | in `release`, before any write | **aborts**, repository untouched |
| `after_tag` | tag on the remote, release commit not yet made | **aborts** |
| `after_release` | last, after the forge release and triggers | reported |
| `on_failure` | when `release` is about to exit non-zero; told `error` and `failed_step` | reported |

`args` are passed verbatim — no shell, so nothing is word-split or
glob-expanded. Hooks inherit the job's environment (a hook that needs a token
reads it there) and are never handed one in the payload; what they print is
recorded in the run report with secrets masked.

Everything else is optional and documented in
[`schema/yasrt.schema.json`](schema/yasrt.schema.json), which editors pick up
for completion and `yasrt check` validates against; the full surface is
specified in [`docs/SPEC.md`](docs/SPEC.md) §5.

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
`GPG_SEM_REL_B64` (the variable name is configurable) and may be **either an
OpenPGP or an OpenSSH private key**, base64-encoded — yasrt detects which from
the material. Both live in a private temporary directory for the duration of
the run and are removed afterwards; all three forges verify SSH signatures.

Both paths are tested by generating real keys and verifying the resulting tag
and commit with `git verify-tag` / `git verify-commit`.

## Security

The only required credential is the job's token, taken from the environment
and handed to git through a credential helper — never in a URL or an argument
list. The signing key is the sole optional long-lived secret. Both are masked
in every log line, in error messages and in the run report. Report
vulnerabilities as described in [SECURITY.md](SECURITY.md).

## Develop

```sh
go test ./...                                   # unit, golden and integration
go test ./internal/render -update               # rewrite golden files
go test ./internal/analyze -run TestForce -v     # one test
gofmt -l . && go vet ./...
```

Integration tests build throwaway git repositories and serve the GitLab,
GitHub and Forgejo release APIs from `httptest`; the signing tests generate
real keys and skip when `gpg` or `ssh-keygen` is absent. Nothing in the suite
touches a real instance of anything. Design history — the specification, the
plan with its decisions and risks, and the task log — is under
[docs/](docs/).

## Removing yasrt

Delete `.yasrt.yaml` and the CI jobs. yasrt holds no state of its own:
everything it produces is a tag, a commit, a release on the forge and a job
artefact. On GitLab, disable the job-token push setting afterwards if nothing
else needs it.

## Where this lives

The canonical public home is <https://github.com/ohartwig/yasrt>; that is the
module path and where tags and releases appear. Development and the release
pipeline run on the author's GitLab, which mirrors here — merge requests there,
issues and pull requests here are both read.

## License

MIT — see [LICENSE](LICENSE). Every file carries an SPDX header; the
repository is [REUSE](https://reuse.software/) compliant.
