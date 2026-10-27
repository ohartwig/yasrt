#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
# SPDX-License-Identifier: MIT
"""Take a release back after a publish job failed -- the release THIS run cut.

What release-tools' `release:rollback` job runs. `yasrt release` cuts the tag
before the artefact is published; a publish job that fails afterwards would
leave a version that is tagged in git and absent from the registry, so the
job deletes the GitLab release and the tag again, and the next push to the
default branch cuts the same version afresh.

Three questions before anything is deleted, each answered from
`release-report.json` (what `yasrt release` wrote, on failure too) and the
remote:

  1. Did this run cut the tag at all? Only when the report's `tag` step is
     `done` and `lost_to` is absent. A run that ended with exit 5 ("tag
     already exists on a different commit") lost the race to a sibling
     pipeline; the tag is the sibling's. Until 2026-10-16 the job read the
     tag name alone and deleted the sibling's release (webmcp 1.6.6,
     crowdsec-bridge 0.10.2).
  2. Is the tag still ours? The remote tag has to point at the report's
     `tag_sha`. A later release that cut the same name on another commit is
     left alone.
  3. Is there a token? The job token cannot delete a tag.

Exit 0 when there is nothing to withdraw, 1 when the release was withdrawn
(the pipeline is red either way: a publish job failed) or could not be.

Environment: CI_API_V4_URL, CI_PROJECT_ID, CI_DEFAULT_BRANCH (GitLab's own),
RELEASE_STATUS (from `yasrt next`), the token in the variable named by
YASRT_TAG_TOKEN_VAR, and `release-report.json` in the working directory.
"""
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request


def _https_only():
    """An opener that speaks https and nothing else.

    `urlopen` uses the default opener, which also handles file:// and
    http://, and the URL is assembled from CI variables. Building the
    director by hand instead of with `build_opener` is the point: that
    helper keeps the default handlers and merely adds to them, so the
    file handler would survive. Here nothing is registered for any other
    scheme, which is why the check in `request` is a readable
    precondition rather than the only thing standing in the way.
    """
    opener = urllib.request.OpenerDirector()
    opener.add_handler(urllib.request.HTTPSHandler())
    opener.add_handler(urllib.request.HTTPDefaultErrorHandler())
    opener.add_handler(urllib.request.HTTPErrorProcessor())
    opener.add_handler(urllib.request.HTTPRedirectHandler())
    # Without this, an unsupported scheme makes `open` fall off the end of
    # the handler chain and answer None, which the caller would meet as an
    # AttributeError. UnknownHandler says what happened instead.
    opener.add_handler(urllib.request.UnknownHandler())
    return opener


def request(method, url, token):
    """One GitLab API call. DELETE answers nothing; the rest answer JSON.

    Only https is possible: the opener registers no other scheme, and the
    check below says so where a reader looks first.
    """
    if urllib.parse.urlsplit(url).scheme != "https":
        raise ValueError(f"refusing a non-https URL: {url}")
    req = urllib.request.Request(url, headers={"PRIVATE-TOKEN": token}, method=method)
    with _https_only().open(req, timeout=60) as r:
        return None if method == "DELETE" else json.load(r)


RESUMED_DETAIL = "already on the remote at this commit"


def cut_by_this_run(report):
    """True when the tag is this release's: this run pushed it, or a previous
    run of the same release did and this one resumed (the tag step is skipped
    because the tag was already on the remote at the released commit).

    A resumed run's publish can fail as well, and leaving its tag standing is
    the shape GDG-44 described: a tagged version nobody can pull. Whether the
    tag still points where this release put it is decided by the tag_sha
    comparison in withdraw(). A lost race (lost_to) is never ours.
    """
    if report.get("lost_to"):
        return False
    if report.get("tag_resumed"):
        return True
    return any(
        s.get("name") == "tag"
        and (s.get("status") == "done" or (s.get("status") == "skipped" and s.get("detail") == RESUMED_DETAIL))
        for s in report.get("steps", [])
    )


def withdraw(report, env, call=request):
    tag = report["tag"]
    if not cut_by_this_run(report):
        lost = report.get("lost_to", "")
        why = f"a sibling pipeline holds it at {lost[:12]}" if lost else "the tag step did not complete"
        print(f"a publish job failed, but {tag} was not cut by this run ({why}): nothing to withdraw")
        return 0
    var = env.get("YASRT_TAG_TOKEN_VAR", "")
    token = env.get(var, "") if var else ""
    if not token:
        print(f"a publish job failed after {tag} was cut, and {var or 'the token variable'} is empty: the job token cannot delete a tag. "
              "The version is tagged in git and not published; delete the tag and the release by hand, or retry the job.")
        return 1
    api, pid = env["CI_API_V4_URL"], env["CI_PROJECT_ID"]
    qtag = urllib.parse.quote(tag, safe="")
    base = f"{api}/projects/{pid}"
    want = report.get("tag_sha") or env.get("CI_COMMIT_SHA", "")
    try:
        current = (call("GET", f"{base}/repository/tags/{qtag}", token).get("commit") or {}).get("id") or ""
    except urllib.error.HTTPError as e:
        if e.code == 404:
            print(f"a publish job failed after {tag} was cut, but the tag is already gone: nothing to withdraw")
            return 0
        raise
    if want and current != want:
        print(f"a publish job failed after {tag} was cut, but the tag now points at {current[:12]}, not at {want[:12]}: "
              "a later release cut it again; leaving it alone")
        return 0
    print(f"a publish job failed after {tag} was cut: taking the tag and the release back")
    for what, path in (("release", f"releases/{qtag}"), ("tag", f"repository/tags/{qtag}")):
        try:
            call("DELETE", f"{base}/{path}", token)
            print(f"  deleted {what} {tag}")
        except urllib.error.HTTPError as e:
            print(f"  DELETE {path}: HTTP {e.code} {e.read().decode(errors='replace')[:300]}")
    print(f"release {tag} withdrawn: fix the publish; the next push to {env.get('CI_DEFAULT_BRANCH', 'main')} cuts it again")
    return 1


def main():
    status = os.environ.get("RELEASE_STATUS")
    if not status:
        print("RELEASE_STATUS is not set: this job does not receive the dotenv report of version - "
              "add { job: version, artifacts: true } to its needs", file=sys.stderr)
        return 1
    if status != "release":
        print(f"skip: {status}")
        return 0
    if not os.path.exists("release-report.json"):
        print("no release-report.json: release did not run, nothing to withdraw")
        return 0
    with open("release-report.json") as f:
        report = json.load(f)
    return withdraw(report, os.environ)


if __name__ == "__main__":
    sys.exit(main())
