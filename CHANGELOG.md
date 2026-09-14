# Changelog

## [1.11.0](https://git.ole-hartwig.eu/yasrt/cli/compare/v1.10.1...v1.11.0) (2026-09-14)

### :sparkles: Features

* **next:** name the release that carries only pipeline chores in an image repository ([b27d6b6](https://git.ole-hartwig.eu/yasrt/cli/commit/b27d6b62878b6ab7c57e9a432a7cc0d0fe1712a4))

## [1.10.1](https://git.ole-hartwig.eu/yasrt/cli/compare/v1.10.0...v1.10.1) (2026-09-14)

### :bug: Fixes

* **forge:** wait for a just-pushed ref before giving up on the trigger ([39c591a](https://git.ole-hartwig.eu/yasrt/cli/commit/39c591a92f8e9420ab71cf09b6d28e37ef68b93e))

### :memo: Documentation

* **tasks:** one blank line ([3fabf62](https://git.ole-hartwig.eu/yasrt/cli/commit/3fabf620e2a01119c0641abbdf47575f5b2b59a5))
* **tasks:** the old chain is gone ([97772e1](https://git.ole-hartwig.eu/yasrt/cli/commit/97772e13e1994af89e86525b00848b799b679aab))
* **tasks:** the cut-over is complete; first night in production ([1fc991f](https://git.ole-hartwig.eu/yasrt/cli/commit/1fc991ff69fd046406b6eb1ecae3f38c90c1eb90))

### :repeat: Continuous Integrations

* release-tools/yasrt@2 ([f00caed](https://git.ole-hartwig.eu/yasrt/cli/commit/f00caed7cfec70aa73c3a67334c89a58f1578c14))
* the release tells merge requests and issues they shipped, as before ([3c58f6d](https://git.ole-hartwig.eu/yasrt/cli/commit/3c58f6d3e7325c474f474f88877d2e6ad6a80650))

## [1.10.0](https://git.ole-hartwig.eu/yasrt/cli/compare/v1.9.1...v1.10.0) (2026-09-13)

### :sparkles: Features

* **contrib:** issues named in commits get the note too ([032efb2](https://git.ole-hartwig.eu/yasrt/cli/commit/032efb2c4a7cf8aaa1945200ce28d9bef8231751))
* **contrib:** the release comments as a script beside the binary ([c0e7b97](https://git.ole-hartwig.eu/yasrt/cli/commit/c0e7b97959b34da9635d4b1c0392a93585ce9a7f))

### :bug: Fixes

* **contrib:** hold the API base to HTTPS ([b058cb2](https://git.ole-hartwig.eu/yasrt/cli/commit/b058cb2dec48d90585d317cc9b43ec2e585c71e6))

### :memo: Documentation

* **tasks:** wave 2 and the trigger-token finding ([2ca1633](https://git.ole-hartwig.eu/yasrt/cli/commit/2ca163303750a3c03aee047545953bda7d1c68bc))

## [1.9.1](https://git.ole-hartwig.eu/yasrt/cli/compare/v1.9.0...v1.9.1) (2026-09-13)

### :bug: Fixes

* **forge:** the pipeline trigger carries the job token in its body ([940fca4](https://git.ole-hartwig.eu/yasrt/cli/commit/940fca4ebcaee9e9c1c0f006d8033e62dfea1d33))

### :memo: Documentation

* **tasks:** cut-over, publication and the tag-pipeline finding ([6a80fdd](https://git.ole-hartwig.eu/yasrt/cli/commit/6a80fdd80b80e439842cfcee33569c3fbbf23f29))

## [1.9.0](https://git.ole-hartwig.eu/yasrt/cli/compare/v1.8.2...v1.9.0) (2026-09-13)

### :sparkles: Features

* **release:** trigger project and ref expand like the variables ([72e64b8](https://git.ole-hartwig.eu/yasrt/cli/commit/72e64b81175488fa97cf14ae57e7bf8cce812227))

### :bug: Fixes

* **rules:** the loop guard is the release commit, not the scope ([1ce698c](https://git.ole-hartwig.eu/yasrt/cli/commit/1ce698c6a9a0ae8f16c0dfc2220f81d0390fa4b3))

## [1.8.2](https://git.ole-hartwig.eu/yasrt/cli/compare/v1.8.1...v1.8.2) (2026-09-13)

### :repeat: Continuous Integrations

* **mirror:** prune every branch GitHub has that the filter did not keep ([2b4f668](https://git.ole-hartwig.eu/yasrt/cli/commit/2b4f6686da0681d1e9c340381ef03186b83e945b))
* **mirror:** run on the runner that can reach github.com ([b46f5ed](https://git.ole-hartwig.eu/yasrt/cli/commit/b46f5ed89e70080ab65bb290e19733809e9882b2))
* **mirror:** prune refs with a loop, not grep | xargs ([47b1ad7](https://git.ole-hartwig.eu/yasrt/cli/commit/47b1ad746a454b55f80b7739be8b9730b5278efe))

### :wrench: Chores

* what the public mirror shows ([a35a27b](https://git.ole-hartwig.eu/yasrt/cli/commit/a35a27b7968efb5efbee6c381243108e1d43f9c7))

## [1.8.1](https://git.ole-hartwig.eu/yasrt/cli/compare/v1.8.0...v1.8.1) (2026-09-13)

### :bug: Fixes

* report the module version when built by go install ([63f1c08](https://git.ole-hartwig.eu/yasrt/cli/commit/63f1c08291b49ea8b6bd327c7d6baeb9052795b5))

### :repeat: Continuous Integrations

* mirror to GitHub through a job that leaves the instance's files out ([9562f23](https://git.ole-hartwig.eu/yasrt/cli/commit/9562f23bee30d190e9e79bff5c364699114f4590))

### :wrench: Chores

* CLAUDE.md and .gitsigners stay in this repository ([561da0e](https://git.ole-hartwig.eu/yasrt/cli/commit/561da0eef6c352f771d9425959273e340df7f734))
* keep agent guidance out of the repository ([e1e6dcb](https://git.ole-hartwig.eu/yasrt/cli/commit/e1e6dcb3d202348951da22deaecbc34a72de9baa))
* what a public repository must not carry, and what it must ([fba8150](https://git.ole-hartwig.eu/yasrt/cli/commit/fba8150e4b6bae32b5e9ff523fc5481e5432acbe))

## [1.8.0](https://git.ole-hartwig.eu/yasrt/cli/compare/v1.7.1...v1.8.0) (2026-09-13)

### :sparkles: Features

* module path github.com/ohartwig/yasrt ([861679b](https://git.ole-hartwig.eu/yasrt/cli/commit/861679bba0dc7fdc5828877438911b77ebfd0f74))

### :memo: Documentation

* **examples:** pin actions/checkout to a commit ([609a00d](https://git.ole-hartwig.eu/yasrt/cli/commit/609a00d1484f11b6fff0641dbae90fb54b122a30))
* single blank line after the plan's preface ([cc69ce0](https://git.ole-hartwig.eu/yasrt/cli/commit/cc69ce043d4f9b7b89f35cfd7185209d0946ccd9))
* a front door for readers who do not know this estate ([44d2d3f](https://git.ole-hartwig.eu/yasrt/cli/commit/44d2d3ffe564fcd852458f2b5c882b19879baab1))

## [1.7.1](https://git.ole-hartwig.eu/yasrt/cli/compare/v1.7.0...v1.7.1) (2026-09-13)

### :zap: Refactor

* errors.AsType, and a package named for what it does ([4b21fac](https://git.ole-hartwig.eu/yasrt/cli/commit/4b21fac0c2b9fcab443eaf4182eb97e194e160e6))
* split release.go by concern ([6dc331c](https://git.ole-hartwig.eu/yasrt/cli/commit/6dc331c1a39e72d133de3648213cd8535646dfe8))

### :wrench: Chores

* the small items from the OSS review ([2e5f08c](https://git.ole-hartwig.eu/yasrt/cli/commit/2e5f08cab934bd45c5d52d39d701738d3fe8dfbd))

## [1.7.0](https://git.ole-hartwig.eu/yasrt/cli/compare/1.6.1...v1.7.0) (2026-09-13)

### :sparkles: Features

* keep the history when tag_format changes, and tag this repository v-prefixed ([21e9e0c](https://git.ole-hartwig.eu/yasrt/cli/commit/21e9e0cc26c4ebcbbec9704ce361b39f27c9dbff))

### :bug: Fixes

* **ci:** decide and release with the binary under test ([e91d30b](https://git.ole-hartwig.eu/yasrt/cli/commit/e91d30ba9d1394091a2d98083215a20781f286f3))
* do not retry a release creation that already took effect ([157b4cf](https://git.ole-hartwig.eu/yasrt/cli/commit/157b4cff68425a51ff2cd629949aa31d905e7a9d))
* import the OpenPGP signing key into a keyring of its own ([6f13a0a](https://git.ole-hartwig.eu/yasrt/cli/commit/6f13a0a315eee7352ff3d4628620d9599e2821f1))
* mask secrets in hook output before it reaches the report ([76259b0](https://git.ole-hartwig.eu/yasrt/cli/commit/76259b0a01191c90413409bf6c9f9a10873c5c40))
* hand git the token through a credential helper, not the URL ([ec24751](https://git.ole-hartwig.eu/yasrt/cli/commit/ec247512a0515af8eea6003d6c75661e2718b962))

### :memo: Documentation

* open-source readiness review at 1.6.1 ([8c05894](https://git.ole-hartwig.eu/yasrt/cli/commit/8c05894b4b0e2a90a3cf02d8f85eb86e01e053df))

### :barber: Styles

* gofmt after removing the helper ([378f36a](https://git.ole-hartwig.eu/yasrt/cli/commit/378f36ab355d5fcda08ade56bd7133795c90d325))

### :zap: Refactor

* defaults an organisation would not recognise as its own ([1b94ff2](https://git.ole-hartwig.eu/yasrt/cli/commit/1b94ff21085d1d30dcb1696ee69cf96e529f7475))

## [1.6.1](https://git.ole-hartwig.eu/yasrt/cli/compare/1.6.0...1.6.1) (2026-09-12)

### :bug: Fixes

* **shadow-report:** count only jobs that spoke as yasrt's shadow ([47dc931](https://git.ole-hartwig.eu/yasrt/cli/commit/47dc931e7df6fec8228b1f5eccd0973321ec02c0))
* rebuild the release commit when the branch moved on ([c2e85dc](https://git.ole-hartwig.eu/yasrt/cli/commit/c2e85dcd94644f660d483b161bef77cc3ac21c09))

### :memo: Documentation

* **plan:** R8 resolved for image repositories ([c01f3e1](https://git.ole-hartwig.eu/yasrt/cli/commit/c01f3e1aed6cb7f5c8f6e350456322d0cad011e1))
* record R8 as resolved and the image pilot release ([35b801d](https://git.ole-hartwig.eu/yasrt/cli/commit/35b801d72025dba9fa38f3b924a7f89cd4ea1d85))
* record the estate-wide shadow rollout and add the verdict report ([6d5613e](https://git.ole-hartwig.eu/yasrt/cli/commit/6d5613ed974d689d797b3ae21e28231d0458004c))

## [1.6.0](https://git.ole-hartwig.eu/yasrt/cli/compare/1.5.1...1.6.0) (2026-09-11)

### :sparkles: Features

* close the remaining semantic-release gaps ([160d835](https://git.ole-hartwig.eu/yasrt/cli/commit/160d8352f5eb5c002f9407efe700d487dccc7e85))

### :memo: Documentation

* map semantic-release options to yasrt ([243d1ca](https://git.ole-hartwig.eu/yasrt/cli/commit/243d1ca7580a186a92a6ffe312afcbd6350c228f))

## [1.5.1](https://git.ole-hartwig.eu/yasrt/cli/compare/1.5.0...1.5.1) (2026-09-11)

### :bug: Fixes

* put the release commit on the branch that was built ([346533d](https://git.ole-hartwig.eu/yasrt/cli/commit/346533d64614b9dc599bc291d06385d5338e029e))

## [1.5.0] - 2026-09-11

### :sparkles: Features

- publish to GitHub and Forgejo as well as GitLab (e980fb6)

## [1.4.0] - 2026-09-11

### :sparkles: Features

- support maintenance branches (0743efa)

## [1.3.0] - 2026-09-11

### :sparkles: Features

- match semantic-release's output, not my reading of it (34341d6)

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
