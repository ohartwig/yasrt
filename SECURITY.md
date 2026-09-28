<!--
  Maintainers: this file is distributed by devops/repo-templates and is
  identical in every repository that carries it. Edit it there; a local edit
  is undone by the next sync. It is also read on public mirrors (GitHub,
  Packagist), so it must not depend on anything only reachable internally,
  and its scope is stated relative to "this repository" rather than as a list
  of groups.
-->

# Security Policy

## Reporting a vulnerability

Please **do not** report security problems through public issues, merge
requests, pull requests or discussions — on GitLab, on GitHub, or on any other
mirror of this repository. Instead, send a report to:

- **Email**: <security@ole-hartwig.eu>
- **PGP**: download the security key from
  <https://ole-hartwig.eu/.well-known/openpgpkey> (RFC 9580) and encrypt
  attachments
- **Signal**: on request

Please include the repository or package name and the affected version (tag,
commit or release), a description of the issue, and steps to reproduce it.
Reports in German or English are equally welcome.

We commit to:

| Stage                                 | SLA                                 |
| ------------------------------------- | ----------------------------------- |
| First acknowledgement                 | within **72 hours** (business days) |
| Vulnerability triage                  | within **7 days**                   |
| Fix plan for Critical                 | within **14 days**                  |
| Coordinated disclosure window default | **90 days** after first response    |

If you don't hear back within the first acknowledgement window, please
escalate via <support@ole-hartwig.eu>.

## Coordinated disclosure

We follow the [CVD principles](https://en.wikipedia.org/wiki/Coordinated_vulnerability_disclosure):
findings stay embargoed until a patch is available, then we publish a security
advisory (see [below](#machine-readable-advisories-csaf-20)) and, where
applicable, request a CVE. The fixed version is named in the release notes of
the affected repository. The reporter is credited unless they ask to remain
anonymous.

## Scope

In scope:

- The source code in this repository, wherever you read it — the primary
  repository or a public mirror of it
- Every artefact built and published from this repository — for example
  Composer or npm packages, container images and release archives — whichever
  registry or mirror you obtained it from
- Other repositories, packages and images maintained by Kai Ole Hartwig: they
  carry this same policy, so report them the same way
- Production sites operated by Kai Ole Hartwig

Out of scope:

- Findings that require physical access to a device operated by Kai Ole Hartwig
- Denial-of-service via volumetric attacks
- Social engineering, phishing, or attacks against Kai Ole Hartwig or engaged contractors
- Third-party services we use (please report directly to the vendor)
- Vulnerabilities in third-party dependencies as such (please report them
  upstream); tell us if the way this repository uses a dependency makes a
  vulnerability exploitable

## Machine-readable advisories (CSAF 2.0)

In addition to this human-readable policy, Kai Ole Hartwig publishes
machine-readable security advisories per **BSI TR-03191 / OASIS CSAF 2.0**
under <https://ole-hartwig.eu/.well-known/csaf/>:

- **Provider metadata**: <https://ole-hartwig.eu/.well-known/csaf/provider-metadata.json>
- **Signing key**: <https://ole-hartwig.eu/.well-known/csaf/openpgp-key.asc>

The key fingerprint is not repeated here. The provider metadata carries it as
the single source of truth, and it is still `TBD-PRE-FIRST-ADVISORY` there — a
fingerprint written down in every repository of the fleet before that is a
claim nobody can check.

Tooling that consumes CSAF feeds (vulnerability scanners, SBOM diff tools, etc.)
can discover the feed via the well-known URL and verify the OpenPGP signature
on each advisory. The role declared in `provider-metadata.json` is
`csaf_publisher`.

## Vulnerability handling process

1. Report received → automatic acknowledgement
2. Maintainer assigned within 72h → triage
3. CVSS scored + severity confirmed → tracking issue created (confidential)
4. Fix developed in a private branch + tested
5. Embargo end approaches → coordinated release: tag + security advisory
6. CVE published where applicable, reporter credited

This file is the canonical source of truth for the vulnerability
disclosure policy of Kai Ole Hartwig and is identical in every repository
under my control. Corrections to the policy itself are welcome at
<security@ole-hartwig.eu>.
