# Security Policy

## Reporting a vulnerability

Please **do not** open public GitLab issues or merge requests for security
problems. Instead, send a report to:

- **Email**: <security@ole-hartwig.eu>
- **PGP**: download the security key from
  <https://ole-hartwig.eu/.well-known/openpgpkey> (RFC 9580) and encrypt
  attachments
- **Signal**: on request

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
findings stay embargoed until a patch is available, then we publish a
GitLab Security Advisory + (if applicable) a CVE via our CNA. The reporter
is credited unless they ask to remain anonymous.

Public Security Advisories live at
<https://git.ole-hartwig.eu/groups/devops/-/security/advisories>.

## Scope

In scope:

- All repositories under `devops/**` and `development/**` on git.ole-hartwig.eu
- All container images under `registry.ole-hartwig.eu/devops/images/**` and
  `registry.ole-hartwig.eu/development/**`
- Production sites operated by Kai Ole Hartwig

Out of scope:

- Findings that require physical access to a device operated by Kai Ole Hartwig
- Denial-of-service via volumetric attacks
- Social engineering, phishing, or attacks against Kai Ole Hartwig or engaged contractors
- Third-party services we use (please report directly to the vendor)

## Machine-readable advisories (CSAF 2.0)

In addition to this human-readable policy, Kai Ole Hartwig publishes
machine-readable security advisories per **BSI TR-03191 / OASIS CSAF 2.0**:

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
6. CVE published, reporter credited

This file is the canonical source of truth for the vulnerability
disclosure policy of Kai Ole Hartwig and is mirrored at every repository
under my control.
For corrections to the policy itself, open an MR against
`devops/repo-templates/SECURITY.md`.
