# Configuration

**English** | [한국어](configuration.ko.md) | [日本語](configuration.ja.md)

mcp-fuse reads one JSON file, given with `-config` (default `config.json`). A change takes effect on restart. An
unknown key is an error, so a misspelled option cannot silently fall back to a default; the process exits non-zero
and says what is wrong.

A complete example is [`examples/config.json`](../examples/config.json).

## Keys

| Key | Required | Meaning |
|---|---|---|
| `listen` | no | Address to bind. Default `127.0.0.1:8080`. Bind loopback and put a reverse proxy in front ([deployment](deployment.md)). |
| `resource` | yes | The public URL agents connect to, e.g. `https://mcp.example.com/mcp`. MCP is served on its path, and the pages where people connect backends are under it. It is the audience a caller's token must have, and the `resource` of the protected resource metadata. The request's `Host` is never used instead. |
| `authorization_servers` | yes | The authorization servers an agent may log in at, advertised in the protected resource metadata ([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)). The first one is also where a person signs in when connecting a backend ([authorization](authorization.md)). |
| `auth` | see below | How a caller's token is checked. |
| `auth.introspection_url` | yes, with `auth` | An [RFC 7662](https://www.rfc-editor.org/rfc/rfc7662) endpoint. A token is accepted when it is active, names a subject, and has `resource` among its audiences. |
| `auth.client_id` | yes, with `auth` | The OAuth client mcp-fuse is at that authorization server. It authenticates to the introspection endpoint and signs a person in when a backend is connected. |
| `auth.client_secret_file` | no | File holding the client's secret. Without it the client is public and sends only its id. |
| `state_file` | with an `oauth` backend | Where grants are kept, encrypted. Losing it means everyone connects again; nothing else depends on it. |
| `secret_file` | with an `oauth` backend | At least 32 bytes. The key that encrypts `state_file` and the key that signs connect links are derived from it, each for its own purpose. |
| `backends` | yes | The MCP servers to merge, in priority order (see below). |
| `backends[].name` | yes | Unique. Shown in logs, in the instructions and in the connect link, so it must be free of `/`, `?`, `#` and spaces. |
| `backends[].url` | yes | The backend's MCP endpoint (http or https). |
| `backends[].auth` | no | `"oauth"` (default): each person connects the backend and mcp-fuse uses that person's own token. `"none"`: the backend asks for no token. |
| `backends[].client_id` | no | A client pre-registered for mcp-fuse at the backend's authorization server. Without it mcp-fuse registers itself ([RFC 7591](https://www.rfc-editor.org/rfc/rfc7591)) and remembers the result. |
| `backends[].client_secret_file` | no | File holding that client's secret, if it is confidential. Needs `client_id`. |
| `list_timeout_seconds` | no | How long `initialize` and the lists wait for the backends. Default 15. |

`auth` may be left out only when every backend is `"auth": "none"`: with no per-person token to keep there is no one
to identify. mcp-fuse then logs a warning at startup that callers are not authenticated.

## Order matters

`backends` is a priority list. If two backends offer a tool, prompt or resource with the same name, the one listed
first keeps it, the other is left out of the merged list, and the clash is logged. A request that belongs to no
backend in particular (a method mcp-fuse does not know) goes to the first backend the person has connected.

## Secrets are files

No secret is in the configuration file; the file names them. Give each secret file to the service user only
(`0600`). The state file is created `0600`.

## Redirect URIs to register

mcp-fuse derives two redirect URIs from `resource`. For `https://mcp.example.com/mcp`:

| Where to register it | Redirect URI |
|---|---|
| The `auth.client_id` client, at the authorization server callers log in at | `https://mcp.example.com/mcp/connect/identify` |
| The mcp-fuse client at each backend's authorization server (unless it registers itself) | `https://mcp.example.com/mcp/connect/callback` |

See [authorization](authorization.md) for what each registration needs to allow.

## Limits

These are fixed in the code: a request body up to 16 MiB; a backend reply read in full (lists, `initialize`) up to
32 MiB; at most 20 pages when a backend paginates a list; a connect link is valid for 10 minutes.
