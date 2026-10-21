# mcp-fuse

**English** | [한국어](README.ko.md) | [日本語](README.ja.md)

A reverse proxy that presents several MCP servers as one.

> **Status: first implementation, not released.** It speaks MCP up to revision 2025-11-25 (see
> [Protocol](#protocol)).

## What it does

An AI agent (ChatGPT, Gemini, Claude, or an in-house assistant) connects to **one** MCP endpoint. `mcp-fuse`
merges what the backends behind it offer and forwards each call to the backend that owns the thing called.

| The agent sends | mcp-fuse does |
|---|---|
| `initialize` | asks every backend; answers with the oldest protocol version among them, the union of their capabilities, and their instructions one after another under each backend's name |
| `tools/list`, `prompts/list`, `resources/list` | lists every backend **as the caller** and merges the answers; a backend that does not answer is left out |
| `tools/call`, `prompts/get`, `resources/read` | sends the request, unchanged, to the backend that listed the name; the reply is streamed back unchanged |
| `notifications/cancelled` and other notifications | passes them to every backend (`notifications/initialized`: only to the stateful one) |
| anything else | passes it to the stateful backend (or the first one) |

- Names are kept as they are. If two backends offer the same name, the one earlier in the configuration keeps it
  and the clash is logged.
- Arguments and results pass through byte for byte.
- Each backend decides what a given person may see. `mcp-fuse` adds no filter.
- One backend being down does not remove the tools of the others.
- If the stateful backend no longer knows the caller's session, `mcp-fuse` answers `404`, so the client
  initializes again instead of quietly receiving an incomplete list.

## What it does not do

These are out of scope and will stay that way. Requests for them belong in the backends.

- Authentication, token issuing, token validation or token storage. The caller's `Authorization` header is
  forwarded as is.
- Policy, masking, result transformation, caching, retries, or any LLM.
- Understanding what a tool means.
- Logging request or response bodies. Logs carry the tool name, backend, latency and status only.

## Run

```sh
./build.sh                                   # gofmt + vet + a static binary: ./mcp-fuse
cp config.example.json config.json           # edit it
./mcp-fuse -config config.json
```

`./mcp-fuse -version` prints the version. There are no dependencies beyond the Go standard library.

### Configuration

One JSON file; a change takes effect on restart. Unknown keys are an error.

```json
{
  "listen": "127.0.0.1:8080",
  "resource": "https://mcp.example.com/mcp",
  "authorization_servers": ["https://auth.example.com"],
  "backends": [
    {"name": "quvi", "url": "http://127.0.0.1:8081/v3/mcp/nl2sql", "stateful": true},
    {"name": "exec", "url": "http://127.0.0.1:9000/mcp"}
  ],
  "list_timeout_seconds": 15
}
```

| Key | |
|---|---|
| `listen` | Address to bind. Default `127.0.0.1:8080`. |
| `resource` | The public URL agents connect to; MCP is served on its path. Used in the discovery document and in 401 challenges. |
| `authorization_servers` | Where an agent logs in. Which one is a deployment decision; mcp-fuse only advertises it. |
| `backends[].name` | Unique; shown in logs and in the instructions. |
| `backends[].url` | The backend's MCP endpoint. |
| `backends[].stateful` | The backend keeps an MCP session (`Mcp-Session-Id`). At most one. Only that backend is sent the session id. |
| `list_timeout_seconds` | How long `initialize` and the lists wait for the backends. Default 15. |

### Endpoints

| | |
|---|---|
| `POST <resource path>` | MCP. `GET` and `DELETE` answer 405. |
| `GET /.well-known/oauth-protected-resource[/<resource path>]` | Protected resource metadata ([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)): `resource` and `authorization_servers` from the configuration. |
| `GET /healthz` | `{"status":"ok","version":"…"}`. It does not probe backends. |

A backend's `401` becomes a `401` whose `WWW-Authenticate` points at this server's metadata document, so an agent
authorizes against the address it actually connected to.

## Protocol

Behavior follows the [MCP specification](https://modelcontextprotocol.io/specification/latest).

- **Supported:** revisions up to 2025-11-25 over Streamable HTTP: the `initialize` handshake, the optional
  `Mcp-Session-Id`, replies as JSON or as an event stream. Batches are refused, as in 2025-06-18.
- **Not yet:** revision 2026-07-28, which drops the handshake and sessions. A request carrying
  `MCP-Protocol-Version: 2026-07-28` or later is answered `400`, which a client that supports both revisions takes
  as the cue to fall back to `initialize`.
- **Not offered:** the standing `GET` event stream and server-to-client requests (sampling, elicitation).
  Resource templates are not merged.

## Deployment shape

A single static binary or container, one configuration file, no database. It runs inside the network next to its
backends; the web tier and load balancer in front of it only terminate TLS and route one path.
[`docs/cp_share/contract.md`](docs/cp_share/contract.md) is the full operational contract.

## Translations

[`README.ko.md`](README.ko.md) and [`README.ja.md`](README.ja.md) follow this file. When this file changes, change
all three.
