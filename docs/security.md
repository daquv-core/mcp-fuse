# Security model

mcp-fuse sits between an agent and the systems that hold your data, so what it does with credentials and with
bodies matters more than what it does with tools. This page lists what it guarantees, why, and what it leaves to
you. To report a vulnerability, see [SECURITY.md](../SECURITY.md).

## What it guarantees

**It never passes a token on.** The token an agent presents is for mcp-fuse: it is introspected, must have mcp-fuse's
`resource` as an audience, and is used to learn who the caller is. A backend only ever receives a token issued by its
own authorization server, for that person, with that backend as the audience. Forwarding the caller's token would
have a backend accept a token not meant for it, or an attacker with a token for one service reuse it at another —
the confused-deputy problem the MCP authorization specification forbids ("token passthrough").

**A backend is sent what an MCP client sends, and nothing the caller chose.** `Content-Type`, `Accept`, `User-Agent`,
`MCP-Protocol-Version`, the backend's own `Mcp-Session-Id`, its own `Authorization`, and an `X-Request-Id` so that a
backend's log can be matched to ours. No caller header is copied, and no `X-Forwarded-*` is added. A caller-supplied
request id is passed on only if it is plainly an id (`[A-Za-z0-9._-]`, up to 64 characters).

**Tool calls pass through unchanged.** The arguments and the reply are not parsed beyond the JSON-RPC frame, not
re-encoded, and not stored. Byte for byte, what the agent sent is what the backend receives, and the reverse.

**Nothing sensitive is logged.** Logs carry the tool name, backend, latency, status and request id. Never a request
or reply body, a token, a secret, or the text of an error from a backend or authorization server (only its class or
code).

**One person's connection is never another's.** Grants are keyed by the person the introspection establishes. The
routing index is per person. Connecting a backend needs two proofs:

- *Who is connecting.* The person signs in at the authorization server agents log in at, and must be the person the
  link was made for. A connect link is a signed, 10-minute ticket; it is a claim, not an identity. Without this
  step, anyone given the link could sign in to the backend as themselves and have that token stored as the link
  owner's — letting the link owner act as them.
- *Which browser.* A cookie (`HttpOnly`, `SameSite=Lax`, scoped to the connect pages) set when the link is opened
  must come back at every step, and every `state` value is used once. Without this, the backend's sign-in address
  could be handed to someone else, whose token would then be stored as the first person's.

**Grants are encrypted at rest.** `state_file` is AES-256-GCM, with a key derived from `secret_file`; it is created
`0600` and written through a temporary file so that a crash leaves the old state. Connect tickets are signed with a
different key derived from the same secret.

**An authorization server must earn the trust.** mcp-fuse refuses one that does not advertise PKCE S256, whose
metadata names another issuer, or whose protected resource metadata names another resource. It checks the `iss`
parameter of authorization responses ([RFC 9207](https://www.rfc-editor.org/rfc/rfc9207)).

## What it leaves to you

- **The host.** Anyone who can read `state_file` and `secret_file` can use the stored tokens. Protect both files, and
  the service user. Back them up with the same care: the state file holds live credentials.
- **TLS to backends** uses the system's trusted roots. A backend with a private CA needs that CA in the system trust
  store (or in the container image).
- **The reverse proxy.** mcp-fuse binds loopback by default and expects TLS to be ended in front of it. Do not expose
  `listen` directly.
- **What a backend returns.** mcp-fuse does not inspect results. A backend that leaks data in a result leaks it
  through mcp-fuse; masking and authorization belong in the backend.
- **Availability of the authorization server.** If introspection is unreachable, callers get `503` (not `401`, which
  would send the client to log in again). A successful introspection is cached for up to 60 seconds, or until the
  token's `exp`, so a token revoked at the authorization server can still work for that long.
- **One instance.** Grants live in a local file and pending connects in memory. Two instances would split them.

## What it deliberately does not do

Policy, masking, result transformation, caching of results, retries beyond re-opening a lost session and refreshing a
token, and any LLM. These belong in the backends; see [CONTRIBUTING](../CONTRIBUTING.md).
