# mcp-fuse

A reverse proxy that presents several MCP servers as one. It does nothing else.

## Boundary
- It merges `initialize`, the lists (`tools`, `prompts`, `resources`) and routes the follow-up request
  (`tools/call`, `prompts/get`, `resources/read`) to the backend that listed the thing. Everything else is passed
  through untouched.
- **Out of scope, permanently:** authentication, token issuing/validation/storage, policy, masking, result
  transformation, caching, retries, LLM. A request for any of these belongs in a backend. Say so and stop.
- No database, no state that cannot be lost. The only memory is the per-caller routing index (`routes.go`).

## Form
Go, standard library only (no dependencies - keep `go.mod` free of `require`). One static binary, one JSON file.
Built and published by CI on every push to main; deployed by control-plane. **This repo never touches
production** - handing a version string to CP is the whole deployment interface (`docs/cp_share/contract.md`).

## Invariants the tests lock
- A tool call's request and reply pass through byte for byte.
- Nothing a caller sent or a backend answered is logged: tool name, backend, latency, status only.
- `Authorization` goes to every backend unchanged; the session id goes to the stateful backend only.
- All headers are forwarded except hop-by-hop ones. Do not turn this into an allow-list.
- One backend failing leaves the others' tools in place.

## Documentation model
- `README.md` - what it is, what it does not do, how to run it.
- `docs/cp_share/` - the contract with control-plane. Self-contained: it never points at a document that lives in
  another repository.
- Never write the same fact in two places.
