# Adding your MCP server

You have an MCP server — an application's actions, a data-query server, a tool server — and want it to appear
behind a single endpoint together with others. **You do not change it for mcp-fuse.** Build it to the MCP standard,
let mcp-fuse be known to its authorization server, and add it to mcp-fuse's configuration.

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
