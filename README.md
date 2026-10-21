# mcp-fuse

**English** | [한국어](README.ko.md) | [日本語](README.ja.md)

A reverse proxy that presents several MCP servers as one.

## What it does

An AI agent (ChatGPT, Gemini, Claude, or an in-house assistant) connects to **one** MCP endpoint. `mcp-fuse`
merges what the backends behind it offer and forwards each call to the backend that owns the thing called.

Toward the agent, `mcp-fuse` is an ordinary MCP resource server: the agent logs in at the authorization server
that `mcp-fuse` names, and the token it gets is **for `mcp-fuse`**. Toward each backend, `mcp-fuse` is an ordinary
MCP client: it holds a token **issued by that backend's own authorization server, for that person**. The token the
agent presented is never sent to a backend, so a backend needs nothing beyond a standard MCP server.

| The agent sends | mcp-fuse does |
|---|---|
| `initialize` | opens a session with every backend the person has connected; answers with the oldest protocol version among them, the union of their capabilities, and their instructions one after another under each backend's name. Backends not yet connected are described there too, with the link that connects them |
| `tools/list`, `prompts/list`, `resources/list` | lists every connected backend **as that person** and merges the answers; a backend that does not answer is left out |
| `tools/call`, `prompts/get`, `resources/read` | sends the request, unchanged, to the backend that listed the name; the reply is streamed back unchanged |
| `notifications/cancelled` and other notifications | passes them to every connected backend that has a session |
| anything else | passes it to the first backend the person has connected |

- Names are kept as they are. If two backends offer the same name, the one earlier in the configuration keeps it
  and the clash is logged.
- Arguments and results pass through byte for byte.
- Each backend decides what a given person may see. `mcp-fuse` adds no filter.
- One backend being down does not remove the tools of the others.
- Sessions are between `mcp-fuse` and each backend. The agent is given none; if a backend forgets one,
  `mcp-fuse` opens a new one and repeats the request.

### Connecting a backend

A backend is connected once per person.

1. The agent calls `initialize`. For every backend the person has not connected, the instructions carry a link.
2. The person opens the link in a browser. They first sign in at the authorization server that callers of
   `mcp-fuse` log in at. That proves who is connecting, and it must be the person the link was made for: a link
   that was passed to someone else connects nothing.
3. They are then sent to the backend's own authorization server (found through the backend's protected resource
   metadata, RFC 9728; authorization code with PKCE, bound to the backend by the resource indicator, RFC 8707).
4. After signing in there, the person is sent back to `mcp-fuse`, which keeps the tokens, encrypted, for that person
   and that backend. The agent starts the conversation again and the backend's tools are there.

The whole flow has to be finished in the browser that opened the link (a cookie ties the steps together), so the
backend's sign-in address is of no use to anyone else.

If the authorization server stops honouring the refresh token, the backend drops out of the person's list and the
link appears in the instructions again.

## What it does not do

These are out of scope and will stay that way. Requests for them belong in the backends.

- Passing a token on. The token an agent presents to `mcp-fuse` is checked and used for nothing else.
- Policy, masking, result transformation, caching, retries, or any LLM.
- Understanding what a tool means.
- Logging request or response bodies, or any token. Logs carry the tool name, backend, latency and status only.

## Run

```sh
./build.sh                                   # gofmt + vet + a static binary: ./mcp-fuse
cp config.example.json config.json           # edit it
./mcp-fuse -config config.json
```

`./mcp-fuse -version` prints the version. There are no dependencies beyond the Go standard library. Run **one
instance**: the grants live in a local file.

### Configuration

One JSON file; a change takes effect on restart. Unknown keys are an error.

```json
{
  "listen": "127.0.0.1:8080",
  "resource": "https://mcp.example.com/mcp",
  "authorization_servers": ["https://auth.example.com"],
  "auth": {
    "introspection_url": "https://auth.example.com/oauth/introspect",
    "client_id": "mcp-fuse",
    "client_secret_file": "/etc/mcp-fuse/introspection-secret"
  },
  "state_file": "/var/lib/mcp-fuse/state.bin",
  "secret_file": "/etc/mcp-fuse/secret",
  "backends": [
    {"name": "quvi", "url": "https://quvi.internal/v3/mcp/nl2sql"},
    {"name": "exec", "url": "https://exec-mcp.internal/mcp",
     "client_id": "mcp-fuse", "client_secret_file": "/etc/mcp-fuse/exec-client-secret"}
  ],
  "list_timeout_seconds": 15
}
```

| Key | |
|---|---|
| `listen` | Address to bind. Default `127.0.0.1:8080`. |
| `resource` | The public URL agents connect to; MCP is served on its path. It is the audience a caller's token must have. |
| `authorization_servers` | Where an agent logs in. Which one is a deployment decision; mcp-fuse only advertises it. |
| `auth.introspection_url`, `auth.client_id`, `auth.client_secret_file` | How a caller's token is checked (RFC 7662): it must be active, name a subject, and have `resource` as an audience. The same client signs a person in when a backend is connected (authorization code with PKCE); register `<resource origin><resource path>/connect/identify` as its redirect URI. Omit `auth` only if every backend is `"auth": "none"`. |
| `state_file` | Where grants are kept, encrypted. Losing it means every person connects again. |
| `secret_file` | At least 32 bytes. The key for `state_file` and for the connect links is derived from it. |
| `backends[].name` | Unique; shown in logs, in the instructions and in the connect link. No `/`, `?`, `#` or spaces. |
| `backends[].url` | The backend's MCP endpoint. |
| `backends[].auth` | `"oauth"` (default): each person connects it. `"none"`: the backend asks for no token. |
| `backends[].client_id`, `client_secret_file` | A client pre-registered for mcp-fuse at the backend's authorization server. Without `client_id`, mcp-fuse registers itself (RFC 7591) and remembers the result. |
| `list_timeout_seconds` | How long `initialize` and the lists wait for the backends. Default 15. |

The redirect URI to register at a backend's authorization server is `<resource origin><resource path>/connect/callback`,
for the example above `https://mcp.example.com/mcp/connect/callback`.

### Endpoints

| | |
|---|---|
| `POST <resource path>` | MCP. `GET` and `DELETE` answer 405. |
| `GET <resource path>/connect/<backend>?t=…` | Starts connecting a backend. The link is handed out in the instructions and is valid for 10 minutes. |
| `GET <resource path>/connect/identify` | Where the sign-in at the callers' authorization server returns. |
| `GET <resource path>/connect/callback` | Where the backend's authorization server sends the person back. |
| `GET /.well-known/oauth-protected-resource[/<resource path>]` | Protected resource metadata ([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)): `resource` and `authorization_servers` from the configuration. |
| `GET /healthz` | `{"status":"ok","version":"…"}`. It does not probe backends. |

### What a backend is sent

Only what a standard MCP client sends: `Content-Type`, `Accept`, `User-Agent`, `MCP-Protocol-Version`, its own
`Mcp-Session-Id`, its own `Authorization`, and an `X-Request-Id` to match logs. Nothing the caller supplied is copied
across, and no `X-Forwarded-*` is added.

## Protocol

Behavior follows the [MCP specification](https://modelcontextprotocol.io/specification/latest).

- **Supported:** revisions up to 2025-11-25 over Streamable HTTP: the `initialize` handshake, the optional
  `Mcp-Session-Id`, replies as JSON or as an event stream. Batches are refused, as in 2025-06-18.
- **Not supported:** revision 2026-07-28, which drops the handshake and sessions. A request carrying
  `MCP-Protocol-Version: 2026-07-28` or later is answered `400`, which a client that supports both revisions takes
  as the cue to fall back to `initialize`.
- **Not offered:** the standing `GET` event stream and server-to-client requests (sampling, elicitation), so
  connect links travel in the instructions rather than as a URL-mode elicitation. Resource templates are not
  merged.

## Deployment shape

A single static binary or container, one configuration file, one state file, one instance. It runs inside the
network next to its backends; the web tier and load balancer in front of it only terminate TLS and route one path.
[`docs/cp_share/contract.md`](docs/cp_share/contract.md) is the full operational contract.

## Translations

[`README.ko.md`](README.ko.md) and [`README.ja.md`](README.ja.md) follow this file. When this file changes, change
all three.
