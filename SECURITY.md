# Security policy

## Reporting a vulnerability

Please report it privately, not in a public issue or pull request: use **"Report a vulnerability"** on the
repository's Security tab (GitHub private vulnerability reporting).

Include what you saw, how to reproduce it, and which version (`mcp-fuse -version`). Do not include real tokens,
secrets or customer data.

## What counts

mcp-fuse is built around a few guarantees, described in [docs/security.md](docs/security.md). A report that shows
one of them failing is a vulnerability:

- a token presented by a caller reaching a backend;
- a backend receiving a header the caller chose, or `X-Forwarded-*`;
- a request or reply body, or any token or secret, appearing in a log or being altered in transit;
- one person's connection, grant or tools becoming visible or usable to another, including by connecting a backend
  through a link or sign-in address that belongs to someone else;
- the state file being readable without the secret.

Weaknesses that need a compromised host, or that come from a backend or an authorization server rather than from
mcp-fuse, are worth reporting to their owners; tell us too if mcp-fuse could have prevented them.
