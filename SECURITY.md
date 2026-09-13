<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
SPDX-License-Identifier: MIT
-->

# Security policy

## Reporting a vulnerability

Please do not open a public issue or merge request for a security problem.
Send a report to <security@ole-hartwig.eu>; a PGP key for attachments is
published at <https://ole-hartwig.eu/.well-known/openpgpkey> (RFC 9580).

You will get an acknowledgement within 72 hours on business days and a
triage result within 7 days. Findings stay embargoed until a fix is released,
90 days after first response at the latest, and the reporter is credited
unless they prefer not to be.

## What counts

In scope: the `yasrt` binary, its CI component templates and the container
image built from this repository — in particular anything that lets a job
tag, push or publish something other than what it built, leak the job token
or the signing key, or run code that a repository's configuration did not
ask for.

Out of scope: the forges yasrt talks to (report those to GitLab, GitHub or
Forgejo), and denial of service against the CI runner.

## Supported versions

The latest minor release. Fixes are released as patch versions and noted in
the changelog with a `security` mention.
