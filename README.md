# mcp-fuse

**English** | [한국어](README.ko.md) | [日本語](README.ja.md)

A reverse proxy that presents several MCP servers as one.

An AI agent (ChatGPT, Gemini, Claude, an in-house assistant) works best with a single MCP endpoint, but what you
want it to use often lives in several MCP servers: an application's own, a data-query server, a tool server.
Connected one by one, the choice of which tool to use where is left to the agent's prompt. `mcp-fuse` puts them
behind one endpoint, merges what they offer, and sends each call to the server that owns the tool. The servers are
not changed.

## What it does

- Merges `initialize`, and the tool, prompt and resource lists, of every backend. Names are kept as they are; where
  two backends offer the same name, the one listed first keeps it.
- Sends each call, unchanged, to the backend that listed the tool. Arguments and results pass through byte for byte.
- Lists each backend **as the person asking**, so each backend decides what that person may see. mcp-fuse adds no
  filter, and one backend being down does not remove the others' tools.
- Is a standard MCP **resource server** to the agent, and a standard MCP **client** to each backend. The token an
  agent presents is checked and goes no further; every backend gets a token issued by its own authorization server,
  for that person. A backend needs nothing beyond being a standard MCP server.

### Connecting a backend

A backend is connected once per person.

1. The agent calls `initialize`. For every backend the person has not connected, the instructions carry a link.
2. The person opens the link in a browser and signs in at the authorization server agents log in at. That proves
   who is connecting; it must be the person the link was made for.
3. The person is sent to the backend's own authorization server and signs in there.
4. mcp-fuse keeps the resulting tokens, encrypted, for that person and that backend.

The whole flow must be finished in the browser that opened the link. See [authorization](docs/authorization.md).

## What it does not do

These are out of scope and will stay that way. Requests for them belong in the backends.

- Passing a token on. A caller's token is checked and used for nothing else.
- Policy, masking, result transformation, caching of results, retries, or any LLM.
- Understanding what a tool means.
- Logging request or response bodies, or any token. Logs carry the tool name, backend, latency and status only.

## Quick start

```sh
./build.sh                                   # gofmt, vet, and a static binary: ./mcp-fuse
cp examples/config.json config.json          # edit it
./mcp-fuse -config config.json
```

A minimal configuration:

```json
{
  "resource": "https://mcp.example.com/mcp",
  "authorization_servers": ["https://auth.example.com"],
  "auth": {"introspection_url": "https://auth.example.com/oauth/introspect", "client_id": "mcp-fuse"},
  "state_file": "/var/lib/mcp-fuse/state.bin",
  "secret_file": "/etc/mcp-fuse/secret",
  "backends": [
    {"name": "orders", "url": "https://orders.example.com/mcp"},
    {"name": "analytics", "url": "https://analytics.example.com/mcp"}
  ]
}
```

Put a TLS-terminating reverse proxy in front ([`examples/nginx.conf`](examples/nginx.conf)) and register mcp-fuse at
the authorization servers ([authorization](docs/authorization.md)). Run **one instance**. A container image:
`docker build -t mcp-fuse .`

## Documentation

- [Configuration](docs/configuration.md) — every key.
- [Authorization](docs/authorization.md) — what to register where, and how a backend is connected.
- [Deployment](docs/deployment.md) — build, files, reverse proxy, operating it.
- [Security model](docs/security.md) — what is guaranteed and what is left to you.
- [Protocol](docs/protocol.md) — what it handles, which MCP revisions, errors.
- [Contributing](CONTRIBUTING.md) and [reporting a vulnerability](SECURITY.md).

## Translations

[`README.ko.md`](README.ko.md) and [`README.ja.md`](README.ja.md) follow this file. When this file changes, change
all three. The documents under `docs/` are in English.
