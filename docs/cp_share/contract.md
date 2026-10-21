# The mcp-fuse <-> control-plane contract

> Self-contained. Values that are not settled are marked `(pending)`.

## 1. Layout

```
AI agent --TLS--> [LB] --> nginx (public MCP host)
                             ├─ /mcp                                         -> mcp-fuse (127.0.0.1:8080)
                             └─ /.well-known/oauth-protected-resource[/mcp]  -> mcp-fuse

mcp-fuse ──HTTP──> backend 1 (QUVI data surface, stateful)
         ──HTTP──> backend 2.. (execution MCP of the customer or solution vendor, stateless)
```

| Owner | What |
|---|---|
| **nginx (CP-owned)** | TLS, the two routes above, buffering and timeout settings (§4) |
| **mcp-fuse (this repo)** | merging the backends' MCP surfaces, forwarding calls, the discovery document |
| **backends** | everything about the person: validating the token, what they may see, masking |

## 2. Runtime contract

1. **Bind loopback only.** Default `127.0.0.1:8080`; `listen` in the configuration changes it.
2. **No state on disk, no database, no secrets.** The configuration holds no credential. Nothing is written
   anywhere except standard output.
3. **`Authorization` is forwarded unchanged to every backend.** mcp-fuse never issues, validates or keeps a token.
4. **Logs are on standard output as JSON lines** and carry the tool name, backend, latency, status and request id
   - never a request or reply body, never a credential.
5. **`SIGTERM` stops it gracefully** (in-flight calls get up to 30 seconds).
6. **Health:** `GET /healthz` -> `200 {"status":"ok","version":"<sha>"}`. It does not probe backends.
7. A backend that does not answer is left out of the merged lists and logged; it never takes the others down.

## 3. Configuration

One JSON file, passed with `-config <path>` (suggested: `/etc/mcp-fuse/config.json`). Changes take effect on
restart. Unknown keys are an error and the process exits non-zero.

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

| Key | Meaning |
|---|---|
| `resource` | The public URL agents connect to. Its path is where mcp-fuse serves MCP. It is advertised as `resource` in the discovery document and used in 401 challenges - the request's `Host` is never used. |
| `authorization_servers` | Where an agent logs in. A deployment decision, e.g. the QUVI deployment's public base URL. |
| `backends[].name` | Appears in logs and in the instructions header. Unique. |
| `backends[].url` | The backend's MCP endpoint, reachable from this host. Not exposed publicly. |
| `backends[].stateful` | The backend keeps an MCP session (`Mcp-Session-Id`). **At most one.** |
| `list_timeout_seconds` | How long the merged requests (`initialize`, lists) wait for the backends. Default 15. |

## 4. The front proxy (nginx)

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

- Pass every request header through. Do not strip or rename `Authorization`, `Mcp-Session-Id`,
  `MCP-Protocol-Version` or any `X-*` header; a header that does not arrive is a header the backend never sees.
- A load balancer or WAF in front must allow `POST` and `GET`, keep idle connections open at least as long as
  `proxy_read_timeout`, and not buffer event streams.

## 5. Build and handover

- Every push to `main` runs `.github/workflows/publish.yml`: gofmt, vet and tests run inside the image build, then
  `ghcr.io/daquv-core/mcp-fuse:<sha>` is published. Publishing is not deploying.
- The image is `FROM scratch` with one file, `/mcp-fuse`, the static binary. Copy it out of the layer; the image
  is not meant to run.
- **Handover is a version string:** CP pins a `<sha>` and installs that binary. `mcp-fuse -version` prints it.
- Needs: a self-hosted runner labelled `mcp-fuse` for the workflow, and pull access to the package for CP.
  `(pending - to be requested from CP)`

## 6. Prerequisites outside this repository

- When the QUVI deployment is the agents' login target, QUVI must accept `resource` (the mcp-fuse URL) as an
  audience when it issues tokens. QUVI accepts only its own surface addresses today, so a token requested for the
  mcp-fuse URL is refused with `invalid_target`. `(pending - change in QUVI)`
- Every backend must accept the token the agent presents. How an execution MCP validates a QUVI-issued token is
  decided there. `(pending)`

## 7. Non-goals

Authentication, token handling of any kind, policy, masking, result transformation, caching, retries, LLM, request or
reply logging. They belong in the backends.
