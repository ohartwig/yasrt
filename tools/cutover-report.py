#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
# SPDX-License-Identifier: MIT
"""How the repositories that moved to yasrt are doing, and how fast.

For every project whose default-branch pipelines carry a `version` job, list
the latest such pipelines with the outcome of `version` and `release`, and
compare the time the old chain spent in `release:semver` (script section,
from the job log) with what `version` + `release` spend now.

Usage: tools/cutover-report.py [--since 2026-09-13] [--group devops] [--per-project 5]
"""
import argparse, concurrent.futures as cf, json, re, subprocess, sys
from statistics import median

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

SECTION = re.compile(r"section_(start|end):(\d+):step_script")

def script_seconds(pid, jid):
    try:
        raw = subprocess.check_output(["glab", "api", f"projects/{pid}/jobs/{jid}/trace"], stderr=subprocess.DEVNULL).decode(errors="replace")
    except subprocess.CalledProcessError:
        return None
    marks = {k: int(v) for k, v in SECTION.findall(raw)}
    return marks["end"] - marks["start"] if {"start", "end"} <= marks.keys() else None

def look(p, since, n):
    rows = []
    try:
        pls = api(f"projects/{p['id']}/pipelines?ref={p.get('default_branch','main')}&per_page=20&order_by=updated_at&sort=desc")
    except subprocess.CalledProcessError:
        return rows
    for pl in pls:
        if since and pl["created_at"] < since:
            continue
        jobs = api(f"projects/{p['id']}/pipelines/{pl['id']}/jobs?per_page=100")
        by = {j["name"]: j for j in jobs}
        if "version" not in by and "release:semver" not in by:
            continue
        tool = "yasrt" if "version" in by else "semantic-release"
        rel = by.get("release") or by.get("release:semver")
        row = {"project": p["path_with_namespace"], "pipeline": pl["id"], "status": pl["status"], "tool": tool,
               "version": (by.get("version") or {}).get("status"), "release": rel["status"] if rel else None,
               "release_script_s": None, "version_script_s": None, "url": pl["web_url"]}
        if tool == "yasrt":
            if by.get("version", {}).get("status") == "success":
                row["version_script_s"] = script_seconds(p["id"], by["version"]["id"])
            if rel and rel["status"] == "success":
                row["release_script_s"] = script_seconds(p["id"], rel["id"])
        elif rel and rel["status"] == "success":
            row["release_script_s"] = script_seconds(p["id"], rel["id"])
        rows.append(row)
        if len(rows) >= n:
            break
    return rows

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--since", default="")
    ap.add_argument("--group", default="")
    ap.add_argument("--per-project", type=int, default=5)
    a = ap.parse_args()
    rows = []
    with cf.ThreadPoolExecutor(8) as ex:
        for r in ex.map(lambda p: look(p, a.since, a.per_project), projects(a.group)):
            rows += r
    rows.sort(key=lambda r: (r["tool"], r["project"], -r["pipeline"]))
    for r in rows:
        t = f"v={r['version_script_s']}s r={r['release_script_s']}s" if r["tool"] == "yasrt" else f"sr={r['release_script_s']}s"
        print(f"{r['tool']:17} {r['project']:50} {r['pipeline']} {r['status']:8} version={r['version']} release={r['release']} {t}")
    sr = [r["release_script_s"] for r in rows if r["tool"] == "semantic-release" and r["release_script_s"]]
    y = [(r["version_script_s"] or 0) + (r["release_script_s"] or 0) for r in rows if r["tool"] == "yasrt" and r["release"] == "success" and r["release_script_s"] is not None]
    print()
    if sr:
        print(f"semantic-release script time, n={len(sr)}: median {median(sr):.0f}s, min {min(sr)}s, max {max(sr)}s")
    if y:
        print(f"yasrt version+release script time, n={len(y)}: median {median(y):.0f}s, min {min(y)}s, max {max(y)}s")
    return 0

if __name__ == "__main__":
    sys.exit(main())
