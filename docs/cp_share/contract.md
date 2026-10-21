# The mcp-fuse <-> control-plane contract

> Self-contained. It does not point at documents in other repositories.

## 1. Layout

```
AI agent --TLS--> [LB] --> nginx (public MCP host)
                             └─ /mcp  (and /mcp/connect/..)                  -> mcp-fuse (127.0.0.1:8080)
                             └─ /.well-known/oauth-protected-resource[/mcp]  -> mcp-fuse

mcp-fuse ──HTTP(S)──> backend 1 (e.g. QUVI data surface)       each backend is reached as a plain MCP client,
         ──HTTP(S)──> backend 2.. (execution MCP, gateway ..)   with a token its own authorization server issued
person's browser ───> the backend's authorization server       (to connect a backend, once per person)
```

| Owner | What |
|---|---|
| **nginx (CP-owned)** | TLS, the routes above, buffering and timeout settings (§5) |
| **mcp-fuse (this repo)** | merging the backends' MCP surfaces, forwarding calls, checking callers' tokens, holding per-person backend tokens |
| **backends** | everything about the person: validating their own tokens, what they may see, masking. They need nothing beyond a standard MCP server |
| **authorization servers** | issuing tokens: one for callers of mcp-fuse (`authorization_servers`), and each backend's own |

## 2. Runtime contract

1. **Bind loopback only.** Default `127.0.0.1:8080`; `listen` in the configuration changes it.
2. **One instance.** Grants are in a local file and pending connects in memory; two instances would split them.
3. **The caller's token is for mcp-fuse only.** It is checked by introspection (RFC 7662) - it must be active, name a
   subject and have `resource` as an audience - and goes no further. **mcp-fuse never passes a token on.**
4. **Toward a backend it is an ordinary MCP client.** Headers sent: `Content-Type`, `Accept`, `User-Agent`,
   `MCP-Protocol-Version`, the backend's own `Mcp-Session-Id`, its own `Authorization`, `X-Request-Id`. Never
   `X-Forwarded-*`, never anything the caller supplied.
5. **State:** one file (`state_file`), AES-256-GCM, key derived from `secret_file`. It holds per-person refresh and
   access tokens per backend and the client ids from dynamic registration. It is created `0600`. Losing it means
   every person connects again; nothing else depends on it.
6. **Logs are on standard output as JSON lines** and carry the tool name, backend, latency, status and request id
   - never a request or reply body, never a token or secret.
7. **`SIGTERM` stops it gracefully** (in-flight calls get up to 30 seconds).
8. **Health:** `GET /healthz` -> `200 {"status":"ok","version":"<sha>"}`. It does not probe backends.
9. A backend that does not answer is left out of the merged lists and logged; it never takes the others down.

## 3. Configuration

One JSON file, passed with `-config <path>` (suggested: `/etc/mcp-fuse/config.json`). Changes take effect on
restart. Unknown keys are an error and the process exits non-zero. A complete example is `config.example.json`;
the keys are documented in `README.md`.

**Secrets are not in the file; the file names them** (`secret_file`, `auth.client_secret_file`,
`backends[].client_secret_file`). CP delivers those files (sops/age) with owner-only permissions.

## 4. What CP registers where

- **At the authorization server that callers log in at** (`authorization_servers`): mcp-fuse's address as a resource
  (audience), and an introspection client (`auth.client_id` and its secret) allowed to introspect tokens issued for it.
- **At each backend's authorization server**: mcp-fuse as an OAuth client, with
  - redirect URI `<resource origin><resource path>/connect/callback`, e.g. `https://mcp.example.com/mcp/connect/callback`;
  - authorization code + PKCE (S256), refresh tokens, and the backend's address as the resource (RFC 8707).

  If that server offers dynamic client registration (RFC 7591) nothing is needed; otherwise register a client and
  put its id (and secret, if confidential) in `backends[].client_id` / `client_secret_file`.
- Backends that ask for no token are configured `"auth": "none"`.

## 5. The front proxy (nginx)

```nginx
location /mcp {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_buffering off;                 # tool replies may be event streams
    proxy_read_timeout 600s;             # a tool call may run for minutes
    client_max_body_size 16m;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
location /.well-known/oauth-protected-resource {
    proxy_pass http://127.0.0.1:8080;
}
```

- This is the only place `X-Forwarded-*` belongs: between nginx and mcp-fuse. mcp-fuse does not forward it on.
- Pass every request header through to mcp-fuse; `Authorization` and `MCP-Protocol-Version` must arrive.
- `/mcp/connect/..` is opened by a person in a browser and must be reachable from there (it is under `/mcp`).
- A load balancer or WAF in front must allow `POST` and `GET`, keep idle connections open at least as long as
  `proxy_read_timeout`, and not buffer event streams.

## 6. Build and handover

- Every push to `main` runs `.github/workflows/publish.yml`: gofmt, vet and tests run inside the image build, then
  `ghcr.io/daquv-core/mcp-fuse:<sha>` is published. Publishing is not deploying.
- The image is `FROM scratch` with one file, `/mcp-fuse`, the static binary. Copy it out of the layer; the image
  is not meant to run.
- **Handover is a version string:** CP pins a `<sha>` and installs that binary. `mcp-fuse -version` prints it.
- Needs: a self-hosted runner labelled `mcp-fuse` for the workflow, and pull access to the package for CP.

## 7. Prerequisites outside this repository

- The authorization server that callers log in at must issue tokens whose audience is mcp-fuse's `resource` and
  offer an introspection endpoint (RFC 7662) to the client named in `auth`.
- Each backend's authorization server must accept mcp-fuse as a client (§4). Nothing changes in the backend.

## 8. Non-goals

Passing a token on, policy, masking, result transformation, caching, retries, LLM, request or reply logging. They
belong in the backends.
