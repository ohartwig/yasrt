<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
SPDX-License-Identifier: MIT
-->

# contrib

Things that belong next to yasrt without belonging in it. The binary does
three things with one credential, the job token; everything here needs
something more -- a token with `api` scope, a second interpreter -- and so
lives outside, as a script a pipeline runs after `release`.

| Script | What it does | Needs |
|---|---|---|
| `gitlab-release-comments.py` | Notes on every merge request and closed issue of a release: ":tada: This MR is included in version x.y.z" -- what semantic-release/gitlab's `success` step did | python3, a token that may write notes (`GITLAB_TOKEN`) |

## Why a script and not a feature

The GitLab notes API is not among the endpoints a `CI_JOB_TOKEN` may call,
and yasrt does not take any other token -- that is the design, not an
omission (see the README on "Only `CI_JOB_TOKEN`"). A comment therefore
cannot come from `yasrt release`. What can come from it is everything the
comment needs: `release-report.json` carries the version, tag, tag SHA and
release URL, `.release.env` the previous tag. The script reads those, asks
GitLab which merge requests the released commits belong to and which issues
they close, and posts the note. Non-fatal by design; the release exists
before it runs.

## How it is wired here

The `release-tools/yasrt` component includes it as `release:comments` behind
the input `mr-comments: 'true'` (`comment-token-var` names the token variable,
`GITLAB_TOKEN` by default) -- the template carries a verbatim copy of the
script, with this file as the source of truth. In a hand-written pipeline:

```yaml
release:comments:
  stage: release          # after `release`
  image: python:3-alpine
  needs:
    - { job: version, artifacts: true }   # .release.env
    - { job: release, artifacts: true }   # release-report.json
  rules: [{ if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH && $CI_PIPELINE_SOURCE == "push" }]
  allow_failure: true
  script:
    - python3 contrib/gitlab-release-comments.py
```

On GitHub and Forgejo the same association is a different API; nothing here
attempts it.
