<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
SPDX-License-Identifier: Apache-2.0
-->

# yasrt

One static Go binary that decides whether a repository should be released, and
publishes the release **after** the artefact has been built. It replaces the npm
`semantic-release` chain in `devops/ci-cd-components/release-tools`.

- **The tag confirms, it does not announce.** `version` decides, `build` runs,
  and only then does `release` tag. A failed build leaves no tag behind.
- **No stored secrets.** `CI_JOB_TOKEN` is the only credential required. A GPG
  key is optional, and `sign: auto` degrades to unsigned rather than failing.
- **One file per repository.** `.yasrt.yaml`, with almost everything derived
  from a single `product:` field.
- **Three dependencies.** A YAML parser, a glob matcher, and git itself.

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
go install git.ole-hartwig.eu/devops/yasrt/cmd/yasrt@latest
```

Binaries for `linux/amd64` and `linux/arm64` are published to this project's
generic package registry with a signed `SHA256SUMS` manifest.

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

Integration tests build throwaway git repositories and serve the GitLab API from
`httptest`. Nothing in the suite touches a real GitLab instance.

## Decommission

Remove the component include and `.yasrt.yaml`, and put the previous
`semantic-release` include back. yasrt holds no state of its own: everything it
produces is a tag, a commit, a GitLab release and a job artefact. Disable the
project setting **Allow Git push requests to the repository** afterwards if
nothing else needs it.

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
