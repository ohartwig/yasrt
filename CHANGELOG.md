# Changelog

## [1.2.1] - 2026-09-11

### :bug: Fixes

- test: force the git identity instead of configuring it (daac8c6)

### :white_check_mark: Tests

- an empty defaults layer must merge to nothing (5b54198)

## [1.2.0] - 2026-09-11

### :sparkles: Features

- layered configuration, so central defaults are possible again (ca55593)

## [1.1.0] - 2026-09-11

### :sparkles: Features

- release through the component instead of hand-written jobs (17be092)
- support prereleases (b7feb8c)
- sign with SSH or OpenPGP, and check who may release (a73cfe9)

### :bug: Fixes

- use a bare nosemgrep on the armour header (3ed1068)
- put the semgrep suppression where semgrep looks for it (8ac6639)
- suppress semgrep on the PGP armour header (81baaf0)

### :memo: Documentation

- record what the pilot found (67a2416)
- record where the work actually stands (c81d93e)
- generalise R8 — every tag-triggered job stops firing (3a95f95)
- record the first real release and what it proved about R1 (6cb6b51)

### :zap: Refactor

- move the image to devops/images/yasrt (ffd82a7)

### :white_check_mark: Tests

- make the intermittent git-log failure self-diagnosing (6a7fa39)

## [1.0.0] - 2026-09-11

### :boom: BREAKING CHANGES

- the module path changed from git.ole-hartwig.eu/devops/yasrt to git.ole-hartwig.eu/yasrt/cli.

### :sparkles: Features

- add exec hooks as the extension mechanism (c8258ac)
- implement next, release and check (51b1cab)

### :bug: Fixes

- configure the git identity before the tag, not before the commit (16b55aa)
- mask every credentialed URL, not only the first (0c424fd)
- ci: use the curl golden image, not the mirror (5535e64)
- ci: run govulncheck on the IPv4 pool, track golangci-lint latest (b06123d)
- image: set address selection before the first apk call (0dd2213)
- ci: install git for the Go jobs, give code fences a language (ea3284e)
- ci: pin the base image by digest and settle hadolint (9f2e94b)

### :memo: Documentation

- bring CLAUDE.md in line with the implementation (d5d163a)

### :zap: Refactor

- move to the yasrt top-level group (323d3b4)

### :white_check_mark: Tests

- isolate the CLI tests from the ambient GitLab environment (55c0177)

### :wrench: Chores

- relicense to MIT (e059281)
