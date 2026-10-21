# Adding your MCP server

You have an MCP server — an application's actions, a data-query server, a tool server — and want it to appear
behind a single endpoint together with others. **You do not change it for mcp-fuse.** Build it to the MCP standard,
let mcp-fuse be known to its authorization server, and add it to mcp-fuse's configuration.

## Who does what in authorization

Authorization for an MCP server has two halves, and they are usually done by different software.

| Half | What it does | Who provides it |
|---|---|---|
| **Authorization server** | Signs the person in, registers clients, runs the authorization code flow with PKCE, issues and refreshes tokens, publishes its metadata and signing keys. | You build it, or you run a ready-made one. |
| **Resource server** (your MCP server) | Answers `401` with a pointer to its metadata, publishes its protected resource metadata, and checks every token it is given: signature, issuer, expiry, and that **it is the audience**. | Always your MCP server. |

The second half never moves to the authorization server, whichever one you use. Only your server knows its own address
as the audience, and only your application knows what the identity in a token means: which of your users it is, and what
that user may do. Section 1 lists what it takes.

### Where the authorization server comes from

If you do not already have an authorization server that follows the standard, **we recommend running
[Keycloak](https://github.com/keycloak/keycloak)**. It is open source (Apache 2.0), it does all of the authorization-server
work in the table above, and it documents how to serve MCP
([Keycloak's MCP guide](https://www.keycloak.org/securing-apps/mcp-authz-server)). Building your own is possible and is
described below, but it is security-sensitive code that you review and patch for as long as the product lives.

#### If you use Keycloak, you

1. **Run it and keep it.** Give it a production database and protect its backups: besides the realm's configuration and
   users, a default setup keeps the realm's signing keys there. Plan for upgrades: Keycloak supports its latest minor
   release ([release policy](https://github.com/keycloak/keycloak/blob/main/RELEASES.md)).
2. **Connect your existing sign-in to it.** Add your sign-in as an identity provider through OpenID Connect, or put a small
   bridge in front of it if your sign-in is not OpenID Connect.
3. **Make mcp-fuse a client of it:** authorization code with PKCE `S256`, refresh tokens, redirect URI
   `<mcp-fuse resource origin><resource path>/connect/callback`. Dynamic client registration exists but is closed to
   anonymous requests by default; create the client by hand (and give mcp-fuse its `client_id`, section 3) or issue an
   initial access token.
4. **Make the audience your server's URL.** Keycloak's guide shows how: a client scope with an *Audience* mapper whose
   *Included Custom Audience* is your server's URL, requested through `scope` (list that scope in your protected resource
   metadata as `scopes_supported`, which is what mcp-fuse asks for when the challenge names none). Keycloak can also act
   on the `resource` parameter itself; its guide says which release has it and how it is enabled. Why this needs
   checking is the first of the two points below.
5. **Make the identity claim trustworthy.** If your server identifies people by their email address, accept only the
   address your identity provider verifies (the identity provider's *trust email* setting) and do not let users change
   their own address (the *email* attribute's edit permission in the realm's user profile). Your server acts on that value;
   see the second of the two points below.
6. **Do the resource-server half** in the table above: section 1 lists it.

#### If you use something else, you

- **An authorization server you already have:** check it against sections 1 and 2 and the two points below, and use it.
  Then do the resource-server half.
- **One you build:** you take on the authorization endpoint with PKCE `S256`; the token endpoint with refresh tokens and
  the `resource` parameter; client registration (dynamic, or by hand); a consent step; the metadata document; and
  publishing and rotating the signing keys. You also connect it to your existing sign-in and follow the revisions of the
  MCP authorization specification. Then do the resource-server half.

The same authorization server may serve the callers' sign-in (`authorization_servers` in mcp-fuse's configuration) and a
backend; mcp-fuse still obtains a separate token for each backend ([authorization](authorization.md)). What sharing buys
is that, if the server keeps a sign-in session for the browser, the person is not asked to sign in again when they
connect the backend. What they are shown beyond that is up to the server's consent settings.

### Two things a ready-made server does not make obvious

What the authorization server must publish and accept is in section 1 (metadata, PKCE `S256`, the `resource` parameter,
refresh tokens) and section 2 (registering mcp-fuse as a client). Beyond that:

1. **The audience a ready-made server puts in a token is not necessarily your server.** Its default may name some other
   client or service. Decode a token it issues and read `aud`; if the server does not act on `resource`, the audience has
   to come from its configuration.
2. **The claim your server uses to identify the person has to be in the access token**, not only in the ID token, and has
   to be the verified value (for an email address, the one the server marks as verified).

## 1. Build it to the standard

"The standard" is the [MCP specification](https://modelcontextprotocol.io/specification/latest). What mcp-fuse relies
on, as any MCP client would:

**Transport — [Streamable HTTP](https://modelcontextprotocol.io/specification/latest/basic/transports/streamable-http).**
One endpoint that accepts a `POST` of a JSON-RPC message and answers with JSON, or with an event stream whose last
frame is the response. A server that keeps no session needs no `Mcp-Session-Id`; one that does may issue it (mcp-fuse
sends it back and, if the server answers `404` to a session it has forgotten, opens a new one).

**Handshake.** mcp-fuse sends `initialize` with protocol revision `2025-11-25` and no capabilities, then
`notifications/initialized`. Answer with the highest revision you support that is **not newer** than `2025-11-25`; a
server that answers with a newer one is refused. (A server that speaks only the handshake-less revision 2026-07-28
cannot be fused yet; see [protocol](protocol.md).)

**Methods.** `tools/list` and `tools/call`. `prompts/*` and `resources/*` if you have them; a method you do not
implement may answer `-32601` and is simply treated as empty. A paginated list (`nextCursor`) is followed.

**Authorization — [the MCP authorization specification](https://modelcontextprotocol.io/specification/latest/basic/authorization).**
Your server is an OAuth resource server, and mcp-fuse finds everything it needs the way that specification says a
client does:

- A request without a token is answered `401` with `WWW-Authenticate: Bearer resource_metadata="<url>"`
  (or the metadata is served at `/.well-known/oauth-protected-resource/<path>`).
- That protected resource metadata ([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)) has `resource` equal to your
  server's URL — the one you give mcp-fuse — and `authorization_servers`.
- The authorization server publishes metadata ([RFC 8414](https://www.rfc-editor.org/rfc/rfc8414) or OpenID
  discovery) whose `issuer` is exactly the one listed, that advertises PKCE `S256`, and has an
  `authorization_endpoint` and a `token_endpoint`. If it adds `iss` to authorization responses
  ([RFC 9207](https://www.rfc-editor.org/rfc/rfc9207)) it says so with `authorization_response_iss_parameter_supported`.
- It accepts the `resource` parameter ([RFC 8707](https://www.rfc-editor.org/rfc/rfc8707)) and issues tokens whose
  audience is your server, plus refresh tokens.
- Your server validates every token (signature, expiry, **audience is itself**) and answers `401` to a bad one.

**One thing about lists.** Each list is requested with the token of the person asking, so it may differ from person
to person. That is how mcp-fuse lets you decide who sees what.

What mcp-fuse sends you is only what an MCP client sends: `Content-Type`, `Accept`, `User-Agent`,
`MCP-Protocol-Version`, your own `Mcp-Session-Id`, your own `Authorization`, and an `X-Request-Id` to match logs.
There is no `X-Forwarded-*` and nothing the end user's client chose.

Tool, prompt and resource **names** should not collide with the other servers' names. Where they do, the server listed
first in mcp-fuse's `backends` keeps the name.

## 2. Let mcp-fuse be known to its authorization server

mcp-fuse is an OAuth client of your authorization server. Either

- turn on [dynamic client registration](https://www.rfc-editor.org/rfc/rfc7591) and it registers itself once; or
- register a client by hand: authorization code grant with PKCE and refresh tokens, redirect URI
  `<mcp-fuse resource origin><resource path>/connect/callback` (for `https://mcp.example.com/mcp` that is
  `https://mcp.example.com/mcp/connect/callback`). If the client is confidential, give mcp-fuse its secret in a file.

## 3. Add it to mcp-fuse

In the configuration ([reference](configuration.md)):

```json
{
  "backends": [
    {"name": "orders", "url": "https://orders.example.com/mcp"}
  ]
}
```

With a pre-registered client, add `"client_id"` and, if confidential, `"client_secret_file"`. Restart mcp-fuse. Each
person then connects the server once, from the link they are given ([authorization](authorization.md)), and from then
on its tools are in their list. For a server that asks for no token (while you are trying things out), set
`"auth": "none"`.

## Check your server before you add it

```sh
# 1. Without a token it is refused, and says where its metadata is.
curl -si -X POST https://orders.example.com/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"check","version":"0"}}}' \
  | head -20
#    expect: HTTP/1.1 401 and a WWW-Authenticate header carrying resource_metadata="…"

# 2. The metadata names this very server as its resource.
curl -s "<the resource_metadata URL>" | jq '{resource, authorization_servers}'

# 3. The authorization server supports what mcp-fuse needs.
curl -s https://auth.example.com/.well-known/oauth-authorization-server \
  | jq '{issuer, code_challenge_methods_supported, authorization_endpoint, token_endpoint, registration_endpoint}'
#    expect: issuer as listed in step 2, "S256" among the methods
```

If all three pass, mcp-fuse can find your authorization server and ask it for a token for your server.
