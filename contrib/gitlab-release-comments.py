#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
# SPDX-License-Identifier: Apache-2.0
"""Tell every merge request and closed issue of a release that it shipped.

What semantic-release/gitlab's `success` step did and yasrt deliberately does
not: the notes API is out of the job token's reach, and yasrt stays on the
job token. So this runs as a job of its own after `release`, with a token that
may write notes, and posts the familiar note:

    :tada: This MR is included in version 1.2.3 :tada:

It reads `release-report.json` (what `yasrt release` wrote) and the
`.release.env` handshake from the environment, asks GitLab which merge
requests each commit of the release belongs to and which issues those close,
adds the issues the commit messages name (#12, Closes #12, the issue URL --
what semantic-release's issue-parser read), and notes each of them once.
Closing issues is GitLab's job at merge time, not the release's; the note
says which version carries the fix.

Environment: CI_API_V4_URL, CI_PROJECT_ID, CI_PROJECT_URL (GitLab's own),
RELEASE_STATUS and RELEASE_PREVIOUS (from `yasrt next`), and the token in the
variable named by YASRT_COMMENT_TOKEN_VAR (default GITLAB_TOKEN).

Non-fatal by design: a missing token or a failed note prints a line and exits
0. The release exists by the time this runs; nothing here may undo it.

Usage in .gitlab-ci.yml (the release-tools/yasrt component wires this as
`release:comments` behind its `mr-comments` input):

    release:comments:
      stage: release
      image: python:3-alpine
      needs: [version, release]
      script: python3 contrib/gitlab-release-comments.py
"""
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

NOTE = (
    ":tada: This {what} version {version} :tada:\n\n"
    "The release is available on [GitLab release]({url})\n\n"
    "Your **yasrt** release :package::rocket:"
)
MR_WHAT = "MR is included in"
ISSUE_WHAT = "issue has been resolved in"

# Issue references in a commit message, the way semantic-release's
# issue-parser read them: #12, Closes #12, or the issue's own URL. Closing is
# not this script's business -- GitLab closes an issue when the merge request
# that names it merges -- only telling it which version carries the fix is.
ISSUE_REF = re.compile(r"(?<![\w/])#(\d+)\b")


def main() -> int:
    if os.environ.get("RELEASE_STATUS", "release") != "release":
        print(f"skip: {os.environ.get('RELEASE_STATUS')}")
        return 0
    token_var = os.environ.get("YASRT_COMMENT_TOKEN_VAR", "GITLAB_TOKEN")
    token = os.environ.get(token_var, "")
    if not token:
        print(f"no token in ${token_var}: nothing to comment with")
        return 0
    try:
        with open(os.environ.get("YASRT_REPORT", "release-report.json")) as f:
            report = json.load(f)
    except OSError as e:
        print(f"no release report: {e}")
        return 0
    api = os.environ["CI_API_V4_URL"]
    pid = os.environ["CI_PROJECT_ID"]
    if not api.startswith("https://"):
        # The only URL this script builds on is the one GitLab hands the job;
        # urllib would also open file:// and the like, so hold it to HTTPS.
        print(f"refusing a non-HTTPS API base: {api}")
        return 0

    def call(path: str, data: dict | None = None):
        body = None if data is None else urllib.parse.urlencode(data).encode()
        req = urllib.request.Request(
            f"{api}/projects/{pid}/{path}", data=body,
            headers={"PRIVATE-TOKEN": token}, method="POST" if data is not None else "GET",
        )
        # nosemgrep: python.lang.security.audit.dynamic-urllib-use-detected.dynamic-urllib-use-detected -- base is CI_API_V4_URL, checked to be HTTPS above; the path is ours
        with urllib.request.urlopen(req, timeout=30) as r:
            return json.load(r)

    version, tag = report["version"], report["tag"]
    url = report.get("release_url") or f"{os.environ.get('CI_PROJECT_URL', '')}/-/releases/{tag}"
    previous = os.environ.get("RELEASE_PREVIOUS", "")
    rng = f"{previous}..{report['tag_sha']}" if previous else report["tag_sha"]

    try:
        commits, page = [], 1
        while True:
            batch = call(f"repository/commits?ref_name={urllib.parse.quote(rng, safe='')}&per_page=100&page={page}")
            commits += batch
            if len(batch) < 100:
                break
            page += 1
        mrs, issues = {}, {}
        for c in commits:
            for m in call(f"repository/commits/{c['id']}/merge_requests"):
                if m["state"] == "merged" and str(m["project_id"]) == pid:
                    mrs[m["iid"]] = m
        for iid in mrs:
            for i in call(f"merge_requests/{iid}/closes_issues"):
                if str(i.get("project_id")) == pid:
                    issues[i["iid"]] = i
        issue_url = f"{os.environ.get('CI_PROJECT_URL', '')}/-/issues/"
        for c in commits:
            text = c.get("message") or c.get("title", "")
            for ref in ISSUE_REF.findall(text) + [u[len(issue_url):] for u in re.findall(re.escape(issue_url) + r"\d+", text)]:
                issues.setdefault(int(ref), None)
        for iid in sorted(mrs):
            call(f"merge_requests/{iid}/notes", {"body": NOTE.format(what=MR_WHAT, version=version, url=url)})
            print(f"noted !{iid}")
        for iid in sorted(issues):
            try:
                call(f"issues/{iid}/notes", {"body": NOTE.format(what=ISSUE_WHAT, version=version, url=url)})
                print(f"noted #{iid}")
            except urllib.error.HTTPError as e:
                if e.code != 404:
                    raise
                print(f"#{iid} named in a commit but not an issue here -- skipped")
    except urllib.error.HTTPError as e:
        print(f"GitLab answered {e.code} on {e.url}: {e.read()[:200]!r} -- the release stands, the notes do not")
        return 0
    print(f"{len(mrs)} merge request(s), {len(issues)} issue(s) told about {tag}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
