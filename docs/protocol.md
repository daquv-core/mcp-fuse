# Protocol

**English** | [한국어](protocol.ko.md) | [日本語](protocol.ja.md)

mcp-fuse speaks MCP over Streamable HTTP and follows the
[MCP specification](https://modelcontextprotocol.io/specification/latest).

## What it handles

| The agent sends | mcp-fuse does |
|---|---|
| `initialize` | opens a session with every backend the person has connected; answers with the oldest protocol version among them, the union of their capabilities, and their instructions one after another under each backend's name. A backend the person has not connected is described there too, with the link that connects it |
| `tools/list`, `prompts/list`, `resources/list` | lists every connected backend **as that person** (following pagination) and merges the answers; a backend that does not answer, or has no such method, adds nothing |
| `tools/call`, `prompts/get`, `resources/read` | sends the request, unchanged, to the backend that listed the name; the reply is streamed back unchanged |
| `notifications/cancelled` and other notifications | passes them to every connected backend that has a session for the person |
| `notifications/initialized` | absorbed: mcp-fuse does its own handshakes with the backends |
| `ping` | answered locally |
| anything else | passed to the first backend the person has connected |

Names are not changed. Where backends offer the same name, the one earlier in `backends` keeps it.

## Sessions

Sessions are between mcp-fuse and each backend. The agent is given none and any `Mcp-Session-Id` it sends is ignored.
mcp-fuse opens a session when it first needs one (or when the agent calls `initialize`), remembers the protocol
revision the backend agreed to and sends it back as `MCP-Protocol-Version`, and, if a backend answers `404` to a
session it no longer knows, opens a new one and repeats the request once.

## Revisions

- **Supported:** MCP revisions up to 2025-11-25: the `initialize` handshake, the optional `Mcp-Session-Id`, and
  replies as JSON or as an event stream. A batch is refused, as in 2025-06-18.
- **Not supported:** revision 2026-07-28, which drops the handshake and protocol-level sessions. A request that
  carries `MCP-Protocol-Version: 2026-07-28` or later is answered `400` with a plain body, which is what a client that
  supports both revisions takes as the cue to fall back to `initialize`.
- **Not offered:** the standing `GET` event stream (`GET` on the MCP path answers `405`), session termination
  (`DELETE` answers `405`), server-to-client requests (sampling, elicitation) and resource templates. Because
  elicitation is not offered, the link that connects a backend is carried in the `initialize` instructions.

## Errors

mcp-fuse answers a JSON-RPC error for a request it cannot serve; the HTTP status says which side failed.

| Situation | Status | Notes |
|---|---|---|
| No token, or one that is not valid for mcp-fuse | `401` | `WWW-Authenticate: Bearer resource_metadata="…"` points at this server's own metadata |
| The authorization server cannot be asked | `503` | not `401`, which would send the client to log in again |
| A name no connected backend offers | `200` | JSON-RPC error `-32602` |
| A backend the person has not connected, called directly | `200` | JSON-RPC error `-32004` |
| Every backend that should answer failed | `502` | JSON-RPC error `-32002` |
| A newer protocol revision than supported | `400` | plain text |
