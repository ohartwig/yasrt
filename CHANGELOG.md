# Changelog

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
