# Security Policy

RemoteMaster is remote-control software: a vulnerability can translate
directly into unauthorized control of someone's machine. Reports are taken
seriously and handled promptly.

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report vulnerabilities privately via GitHub's private vulnerability reporting:

1. Go to the repository's **Security** tab.
2. Choose **Report a vulnerability** and fill in the advisory form.

If private reporting is unavailable to you, open a minimal issue that says
only "security issue, requesting a private channel" — a maintainer will
follow up. Do not include exploit details in the public issue.

Include where possible:

- Affected component (`server`, `client`, agent UI) and version/commit
- Reproduction steps or proof of concept
- Impact assessment (what an attacker gains)

You can expect an acknowledgement within a few days. Please allow a
reasonable window for a fix before public disclosure.

## Supported versions

Only the latest release (and `main`) receive security fixes.

## Deployment hardening

The threat model and operator guidance — TLS termination, proxy header
trust, origin checks, rate limiting, and session code handling — are
documented in [`docs/security.md`](docs/security.md).
