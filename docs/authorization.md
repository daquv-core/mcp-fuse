# Authorization

**English** | [한국어](authorization.ko.md) | [日本語](authorization.ja.md)

mcp-fuse is two things at once, and keeps them apart.

- To an **agent** it is an ordinary MCP resource server. The agent logs in at an authorization server that
  mcp-fuse names, and the token it gets is **for mcp-fuse**.
- To each **backend** it is an ordinary MCP client. It holds, per person and per backend, a token **issued by that
  backend's own authorization server, for that person**.

The token an agent presents is checked and then used for nothing else. It is never sent to a backend. The
[MCP authorization specification](https://modelcontextprotocol.io/specification/latest/basic/authorization) forbids a
server to accept or pass on tokens not issued for it ("token passthrough"); this is how mcp-fuse avoids it, and why a
backend needs nothing beyond being a standard MCP server.

## What to set up

### 1. The authorization server agents log in at (`authorization_servers`)

It must:

- issue access tokens whose audience is mcp-fuse's `resource` (the agent asks for it with the resource indicator,
  [RFC 8707](https://www.rfc-editor.org/rfc/rfc8707));
- offer an introspection endpoint ([RFC 7662](https://www.rfc-editor.org/rfc/rfc7662)) to the client named in
  `auth`, returning `active`, `sub`, `aud` and optionally `iss` and `exp`;
- support authorization code with PKCE (S256) for that client, with redirect URI
  `<resource origin><resource path>/connect/identify` — it is used when a person connects a backend, to prove who
  they are.

The person's identity inside mcp-fuse is a hash of the introspected `iss` and `sub`.

### 2. Each backend's authorization server

mcp-fuse must be known to it as an OAuth client with:

- the authorization code grant with PKCE (S256), and refresh tokens;
- redirect URI `<resource origin><resource path>/connect/callback`;
- the backend's own address accepted as the `resource` (audience) of the tokens it issues.

Either register the client by hand and put `client_id` (and `client_secret_file`, if it is confidential) under the
backend, or let the server accept [dynamic client registration](https://www.rfc-editor.org/rfc/rfc7591) and mcp-fuse
registers itself once. A confidential client's secret is sent as the server's metadata says it takes it
(`client_secret_basic` by default, `client_secret_post` if that is all it offers).

mcp-fuse does **not** identify itself with a
[client ID metadata document](https://modelcontextprotocol.io/specification/latest/basic/authorization/client-registration#client-id-metadata-documents)
(an HTTPS URL used as the `client_id`, which the authorization server fetches). The MCP authorization specification
names that as the preferred way for a client to identify itself, and keeps dynamic registration as a fallback for
servers that do not support it; an authorization server advertises the document form with
`client_id_metadata_document_supported`. An authorization server that offers only that, and no
`registration_endpoint`, needs the client registered by hand: without a configured `client_id` mcp-fuse reports that the
server offers no client registration.

mcp-fuse finds the server the way the specification has a client do it: the backend's protected resource metadata
(from the `WWW-Authenticate` challenge, or the well-known address), then the authorization server metadata
([RFC 8414](https://www.rfc-editor.org/rfc/rfc8414)). It refuses a server that does not advertise PKCE S256, whose
metadata names a different issuer, or whose protected resource metadata names a different resource.

Nothing about the backend changes: it validates its own tokens, for its own audience, as any MCP server does.

## Connecting a backend

A backend is connected once per person. Until then it is absent from that person's lists, and `initialize` carries a
link for it in the instructions.

1. The person opens the link in a browser. It carries a signed, 10-minute **ticket** that names a person and a
   backend. A ticket says whom a link was made for; it proves nothing about who opens it.
2. **Identify.** The person signs in at the authorization server of step 1 above. The token that sign-in produces is
   introspected once, to learn who signed in, and discarded. Only if that is the person the ticket names does the flow
   go on. A link passed to someone else connects nothing.
3. **Backend.** The person is sent to the backend's authorization server (authorization code, PKCE, `resource` set to
   the backend) and signs in there.
4. The authorization server sends the person back to `.../connect/callback`; mcp-fuse exchanges the code and keeps
   the tokens, encrypted, under that person and that backend.

A cookie set when the link is opened ties steps 2–4 to that browser, and each `state` value is used once. So the
backend's sign-in address, handed to someone else, cannot be finished by them: without the cookie the callback is
refused. See [security](security.md) for why both measures are needed.

## Refresh and revocation

An access token is refreshed when it is about to lapse, or when a backend answers `401` to it. If the authorization
server refuses the refresh token (`invalid_grant`), or the backend still refuses a fresh token, the grant is dropped.
The backend disappears from that person's lists and the link reappears in `initialize`.
