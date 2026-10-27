# SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
# SPDX-License-Identifier: MIT
"""Tests for gitlab-release-rollback.py: `python3 -m unittest discover -s contrib`."""
import importlib.util
import io
import pathlib
import unittest
import urllib.error
from contextlib import redirect_stdout

SPEC = importlib.util.spec_from_file_location(
    "rollback", pathlib.Path(__file__).with_name("gitlab-release-rollback.py"))
rollback = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(rollback)

ENV = {"YASRT_TAG_TOKEN_VAR": "TAG_TOKEN", "TAG_TOKEN": "t", "CI_API_V4_URL": "https://g/api/v4",
       "CI_PROJECT_ID": "1", "CI_DEFAULT_BRANCH": "main"}
OURS = "a" * 40
THEIRS = "b" * 40


class FakeAPI:
    """Answers GET repository/tags with a commit id and records every DELETE."""

    def __init__(self, tag_at):
        self.tag_at = tag_at
        self.deleted = []

    def __call__(self, method, url, token):
        if method == "DELETE":
            self.deleted.append(url.rsplit("/projects/1/", 1)[1])
            return None
        if self.tag_at is None:
            raise urllib.error.HTTPError(url, 404, "Not Found", {}, io.BytesIO(b""))
        return {"commit": {"id": self.tag_at}}


def report(tag_done=True, lost_to=None):
    steps = [{"name": "notes", "status": "done"},
             {"name": "tag", "status": "done" if tag_done else "failed"}]
    r = {"version": "1.6.6", "tag": "v1.6.6", "tag_sha": OURS, "steps": steps}
    if lost_to:
        r["lost_to"] = lost_to
    return r


def run(rep, api, env=ENV):
    out = io.StringIO()
    with redirect_stdout(out):
        code = rollback.withdraw(rep, env, call=api)
    return code, out.getvalue()


class RollbackTests(unittest.TestCase):
    def test_deletes_the_tag_this_run_cut(self):
        api = FakeAPI(OURS)
        code, out = run(report(), api)
        self.assertEqual(code, 1)
        self.assertEqual(api.deleted, ["releases/v1.6.6", "repository/tags/v1.6.6"])
        self.assertIn("withdrawn", out)

    def test_leaves_a_lost_race_alone(self):
        # exit 5 in yasrt: the sibling pipeline cut v1.6.6 first.
        api = FakeAPI(THEIRS)
        code, out = run(report(tag_done=False, lost_to=THEIRS), api)
        self.assertEqual(code, 0)
        self.assertEqual(api.deleted, [])
        self.assertIn("not cut by this run", out)
        self.assertIn(THEIRS[:12], out)

    def test_leaves_a_tag_without_a_done_step_alone(self):
        api = FakeAPI(OURS)
        code, _ = run(report(tag_done=False), api)
        self.assertEqual(code, 0)
        self.assertEqual(api.deleted, [])

    def test_leaves_a_tag_at_another_commit_alone(self):
        # Our report says we cut it, but a later release moved the name.
        api = FakeAPI(THEIRS)
        code, out = run(report(), api)
        self.assertEqual(code, 0)
        self.assertEqual(api.deleted, [])
        self.assertIn("leaving it alone", out)

    def test_a_tag_already_gone_is_nothing_to_withdraw(self):
        api = FakeAPI(None)
        code, out = run(report(), api)
        self.assertEqual(code, 0)
        self.assertEqual(api.deleted, [])
        self.assertIn("already gone", out)

    def test_no_token_is_an_error_without_a_delete(self):
        api = FakeAPI(OURS)
        code, out = run(report(), api, env={**ENV, "TAG_TOKEN": ""})
        self.assertEqual(code, 1)
        self.assertEqual(api.deleted, [])
        self.assertIn("by hand", out)


    # AUD-030-F03: a resumed run found its own tag on the remote and skipped
    # the tag step; when its publish fails, the tag is still this release's.
    def test_withdraws_the_tag_of_a_resumed_run(self):
        for rep in (
            {"tag": "v1.6.6", "tag_sha": OURS, "steps": [{"name": "tag", "status": "skipped", "detail": "already on the remote at this commit"}]},
            {"tag": "v1.6.6", "tag_sha": OURS, "tag_resumed": True, "steps": [{"name": "tag", "status": "skipped"}]},
        ):
            api = FakeAPI(OURS)
            code, out = run(rep, api)
            self.assertEqual(code, 1)
            self.assertEqual(api.deleted, ["releases/v1.6.6", "repository/tags/v1.6.6"])
            self.assertIn("withdrawn", out)

    def test_a_resumed_run_leaves_a_moved_tag_alone(self):
        api = FakeAPI(THEIRS)
        rep = {"tag": "v1.6.6", "tag_sha": OURS, "tag_resumed": True, "steps": [{"name": "tag", "status": "skipped"}]}
        code, out = run(rep, api)
        self.assertEqual(code, 0)
        self.assertEqual(api.deleted, [])
        self.assertIn("leaving it alone", out)

    def test_a_resumed_lost_race_is_still_not_ours(self):
        api = FakeAPI(THEIRS)
        rep = {"tag": "v1.6.6", "tag_sha": OURS, "tag_resumed": True, "lost_to": THEIRS, "steps": [{"name": "tag", "status": "skipped"}]}
        code, _ = run(rep, api)
        self.assertEqual(code, 0)
        self.assertEqual(api.deleted, [])


if __name__ == "__main__":
    unittest.main()
