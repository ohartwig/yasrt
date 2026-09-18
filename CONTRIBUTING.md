<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
SPDX-License-Identifier: Apache-2.0
-->

# Contributing

Thank you for looking at this. The tool is small on purpose; most
contributions are a test and a few lines.

## Where to send what

- **Bugs and ideas:** issues on [GitHub](https://github.com/ohartwig/yasrt/issues).
- **Changes:** pull requests on GitHub are welcome. Development and the
  release pipeline run on the author's GitLab; a pull request is reviewed on
  GitHub and lands there through the mirror, so a merge may take a day.
- **Security problems:** see [SECURITY.md](SECURITY.md), not an issue.

## Setup

Any Go 1.27 toolchain and `git`. The signing tests also want `gpg` and
`ssh-keygen` on `PATH` and skip without them. No other tooling is required;
the author's own checkout carries hook and toolchain manifests that are not
part of the public repository.

## Commits

Conventional Commits — the tool releases itself from them. Allowed types:
`feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `ci`, `chore`,
`build`, `revert`; a breaking change takes `!` after the type or a
`BREAKING CHANGE:` footer. Only `feat`, `fix`, `perf` and `revert` produce a
release. English for everything an engineer reads: identifiers, comments,
commit messages, branch names, titles and descriptions.

Signing your commits is appreciated, not required. Commits are signed before
they reach `main`.

## Tests

Every behavioural change needs a test. The suite is defined against real git
behaviour rather than mocks: `internal/testrepo` builds throwaway
repositories, and the GitLab, GitHub and Forgejo release APIs are served by
`httptest`. Golden files under `internal/render/testdata` are regenerated
with `go test ./internal/render -update` and reviewed like any other diff.

Before pushing:

```sh
gofmt -l . && go vet ./... && go test ./...
```

One pull request, one logical change. Changes to `internal/git`,
`internal/forge`, `internal/hooks` or the signing path get a closer look:
that is where a mistake becomes a wrong tag or a leaked token.

## What not to add

The non-goals in [`docs/SPEC.md`](docs/SPEC.md) §2 are decisions, not gaps:
no monorepo versioning, no forges beyond GitLab, GitHub and Forgejo, no
publishing to package registries from the binary, no CLI framework, and no
Conventional Commits preset dependency.

Extension goes through **exec hooks** (§5.2), never through a package chain
resolved at run time — that chain is what this tool was built to remove. If a
feature can be a hook, it should be a hook.
