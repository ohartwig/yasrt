<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
SPDX-License-Identifier: MIT
-->

# Contributing

## Before the first commit

```sh
mise install     # Go 1.27 and lefthook
lefthook install # commit-msg, gofmt, gitleaks
```

## Commits

Conventional Commits, enforced by the `commit-msg` hook and again in CI. Allowed
types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `ci`,
`chore`, `build`, `revert`. A breaking change takes `!` after the type, or a
`BREAKING CHANGE:` footer.

Only `feat`, `fix`, `perf` and `chore` produce a release here; `build` and
`revert` deliberately do not.

Commits are signed. English for everything an engineer reads — identifiers,
comments, commit messages, branch names, merge-request titles and descriptions.

## Merge requests

Open against `main`; direct pushes are blocked. One merge request, one logical
change. Changes to `.gitlab-ci.yml`, the component templates or anything under
`internal/git`, `internal/gitlab` or `internal/logging` need a second reviewer,
per CODEOWNERS.

## Tests

Every behavioural change needs a test. The suite is defined against real git
behaviour rather than mocks: `internal/testrepo` builds throwaway repositories,
and the GitLab API is served by `httptest`. Golden files under
`internal/render/testdata` are regenerated with `go test ./internal/render -update`
and reviewed like any other diff.

Before pushing:

```sh
gofmt -l . && go vet ./... && go test ./...
```

## What not to add

The non-goals in [`docs/SPEC.md`](docs/SPEC.md) §2 are decisions, not gaps:
no monorepo versioning, no forge other than GitLab, no publishing to package
registries from the binary, no CLI framework, and no Conventional Commits preset
dependency. Prereleases are the one deferred item, planned as phase P10.

Extension goes through **exec hooks** (§5.2), never through a package chain
resolved at run time — that chain is what this tool was built to remove. If a
feature can be a hook, it should be a hook.
