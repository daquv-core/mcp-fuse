# mcp-fuse

A reverse proxy that presents several MCP servers as one. It does nothing else.

## Boundary
- Toward the agent it is an ordinary MCP **resource server**: the token a caller presents is for mcp-fuse
  (`resource` is its audience), is checked (`internal/front`), and is used for nothing else.
- Toward each backend it is an ordinary MCP **client** (`internal/oauthc`, `internal/fuse/backend.go`): it holds, per person and per
  backend, a token issued by *that backend's own authorization server*, and its own MCP session.
- It merges `initialize`, the lists (`tools`, `prompts`, `resources`) and routes the follow-up request
  (`tools/call`, `prompts/get`, `resources/read`) to the backend that listed the thing. Everything else is passed
  through untouched.
- **Never:** pass a token on (MCP authorization forbids token passthrough), add or copy headers to a backend
  request beyond what an MCP client sends, or ask anything of a backend beyond a standard MCP server.
- **Out of scope, permanently:** policy, masking, result transformation, caching, retries, LLM. A request for any
  of these belongs in a backend. Say so and stop.
- The only durable state is the encrypted grants file (`internal/state`). Everything else is memory that can be lost:
  the routing index (`internal/fuse/routes.go`), the backend sessions, the pending connects.

## Layout
```
cmd/mcp-fuse/      main: flags, wiring, graceful shutdown
internal/config/   the JSON file and its checks
internal/jsonrpc/  the JSON-RPC frame, reading event streams, writing replies
internal/front/    checking the token a caller presents (introspection)
internal/oauthc/   OAuth client of the backends: discovery, registration, tokens, refresh, PKCE
internal/state/    the encrypted grants file; signed connect tickets
internal/neterr/   naming transport errors without their text
internal/fuse/     the handler: merging, routing, backend sessions, the connect pages
```
Dependencies point one way: `fuse` uses the others; the others do not use `fuse`. Keep it that way.

## Form
Go, standard library only (no dependencies - keep `go.mod` free of `require`). One static binary, one JSON file,
one state file, one instance. Built and published by CI on every push to main; deployed by control-plane.
**This repo never touches production** - handing a version string to CP is the whole deployment interface
(`docs/cp_share/contract.md`).

## Invariants the tests lock
- A tool call's request and reply pass through byte for byte.
- A backend is sent only: `Content-Type`, `Accept`, `User-Agent`, `MCP-Protocol-Version`, its own `Mcp-Session-Id`,
  its own `Authorization`, `X-Request-Id`. Never anything the caller supplied, never `X-Forwarded-*`. Do not turn
  this into a copy of the caller's headers.
- The token a backend receives was issued by its own authorization server, for that person, with that backend as
  the audience. The caller's token never reaches a backend.
- Nothing a caller sent, a backend answered, or any token is logged: tool name, backend, latency, status only.
- Grants are encrypted at rest; a connect state is used once; a connect link is a signed, short-lived ticket.
- A ticket says whom a link was made for and proves nothing about who opens it. A connect stores a grant only after
  the person who signs in at the callers' authorization server is the person the ticket names, and only in the
  browser that opened the link (cookie). A link or a backend sign-in address handed to someone else connects nothing.
- One backend failing leaves the others' tools in place. A person sees only what they have connected.

## Documentation model
- `README.md` - what it is, what it does not do, how to run it. English is the source;
  `README.ko.md` and `README.ja.md` are translations that follow it. Change all three together.
- `docs/cp_share/` - the contract with control-plane. Self-contained: it never points at a document that lives in
  another repository.
- Never write the same fact in two places.
- Documents state what is true whatever the release: no "first implementation", "not released", "not yet" or
  "pending" notes. What is unsupported is listed as unsupported; what is open belongs in an issue.
