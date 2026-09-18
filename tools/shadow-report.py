#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
# SPDX-License-Identifier: Apache-2.0
"""Collect every shadow:compare verdict on a GitLab instance into one table.

Operator tooling for a migration from semantic-release: while the
release-tools/yasrt-shadow component runs beside semantic-release, this
reads every verdict it wrote and lists the disagreements first.

Usage: tools/shadow-report.py [--since 2026-09-12] [--group devops] [--json out.json]

Reads through glab, so it needs a logged-in glab (`glab auth login`) and
membership in the projects; --group restricts the walk to one group and
its subgroups. A verdict comes from the job log of shadow:compare; a
pipeline without that job is not a shadow pipeline and is ignored.
"""
import argparse, concurrent.futures as cf, json, re, subprocess, sys
from collections import Counter

def api(path):
    return json.loads(subprocess.check_output(["glab", "api", path], stderr=subprocess.DEVNULL))

def projects(group=""):
    page, out = 1, []
    base = f"groups/{group.replace('/', '%2F')}/projects?include_subgroups=true" if group else "projects?"
    while True:
        ps = api(f"{base}&archived=false&simple=true&per_page=100&page={page}&order_by=id&sort=asc")
        if not ps:
            return out
        out += ps
        page += 1

VERDICT = re.compile(r"(AGREE|DIFFER)[^\n]*")
LINE = re.compile(r"^\S+ \d\d[OE] ?(.*)$", re.M)

def verdicts(p, since):
    out = []
    try:
        jobs = api(f"projects/{p['id']}/jobs?scope[]=success&scope[]=failed&per_page=100")
    except subprocess.CalledProcessError:
        return out
    for j in jobs:
        if j["name"] != "shadow:compare" or (since and j["created_at"] < since):
            continue
        try:
            raw = subprocess.check_output(["glab", "api", f"projects/{p['id']}/jobs/{j['id']}/trace"],
                                          stderr=subprocess.DEVNULL).decode(errors="replace")
        except subprocess.CalledProcessError:
            continue
        text = "\n".join(re.sub(r"\x1b\[[0-9;]*[mK]", "", m.group(1)) for m in LINE.finditer(raw))
        m = VERDICT.search(text)
        sr = re.search(r"^semantic-release:\s*(\S.*)$", text, re.M)
        ya = re.search(r"^yasrt:\s*(\S.*)$", text, re.M)
        if not ya and not m:
            # Another tool's job of the same name (pinup/runner has one), or a
            # job that died before it could say anything -- not a verdict.
            continue
        out.append({
            "project": p["path_with_namespace"], "pipeline": j["pipeline"]["id"], "ref": j["ref"],
            "created_at": j["created_at"], "web_url": j["web_url"],
            "verdict": m.group(1) if m else "NONE", "detail": m.group(0) if m else "",
            "semantic_release": sr.group(1).strip() if sr else "", "yasrt": ya.group(1).strip() if ya else "",
        })
    return out

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--since", default="")
    ap.add_argument("--group", default="", help="restrict to one group and its subgroups")
    ap.add_argument("--json", default="")
    a = ap.parse_args()
    rows = []
    with cf.ThreadPoolExecutor(8) as ex:
        for r in ex.map(lambda p: verdicts(p, a.since), projects(a.group)):
            rows += r
    rows.sort(key=lambda r: (r["verdict"] != "DIFFER", r["project"], r["created_at"]))
    by = Counter(r["verdict"] for r in rows)
    print(f"{len(rows)} verdict(s) across {len({r['project'] for r in rows})} project(s): "
          + ", ".join(f"{k} {v}" for k, v in sorted(by.items())))
    print()
    for r in rows:
        print(f"{r['verdict']:6} {r['project']:55} {r['semantic_release']:18} | {r['yasrt']:28} {r['web_url']}")
        if r["verdict"] == "DIFFER":
            print(f"       {r['detail']}")
    if a.json:
        json.dump(rows, open(a.json, "w"), indent=1)
    return 0

if __name__ == "__main__":
    sys.exit(main())
