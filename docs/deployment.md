# Deployment

mcp-fuse is one static binary (or one container), one JSON file, one state file. It needs no database. Run **one
instance**.

```
agent ──TLS──> load balancer / reverse proxy ──HTTP──> mcp-fuse (127.0.0.1:8080) ──HTTP(S)──> backends
person's browser ───────────────────────────────────────────────────────────────> backends' authorization servers
```

## Build

```sh
./build.sh          # gofmt check, go vet, then a static binary: ./mcp-fuse
./mcp-fuse -version
```

Go 1.22 or newer. There are no dependencies beyond the standard library. `go test -race ./...` runs the tests.

A container image:

```sh
docker build -t mcp-fuse --build-arg VERSION=$(git describe --tags --always) .
docker run -v "$PWD/config:/etc/mcp-fuse:ro" -v mcp-fuse-state:/var/lib/mcp-fuse \
           -p 127.0.0.1:8080:8080 mcp-fuse
```

The image runs as a non-root user (uid 65532); the state volume must be writable by it. It is based on a minimal
image that includes CA certificates, which https backends need.

## Files

| File | Holds | Permissions |
|---|---|---|
| `config.json` | the configuration | readable by the service user |
| `secret_file` | at least 32 random bytes | the service user only |
| `auth.client_secret_file`, `backends[].client_secret_file` | OAuth client secrets | the service user only |
| `state_file` | the encrypted grants | created `0600`; its directory must be writable |

Generate the secret with, for example, `head -c 48 /dev/urandom | base64 > /etc/mcp-fuse/secret`. Changing it makes
the existing state file unreadable: everyone connects again.

[`examples/mcp-fuse.service`](../examples/mcp-fuse.service) is a hardened systemd unit. It runs as a dedicated user
(`useradd --system --no-create-home --shell /usr/sbin/nologin mcp-fuse`) and keeps the state file in `/var/lib/mcp-fuse`.

## The reverse proxy

[`examples/nginx.conf`](../examples/nginx.conf) is a working configuration. What matters, whatever you use:

- **Route two paths** to mcp-fuse: the path of `resource` (which also covers `<path>/connect/...`, the pages a
  person opens in a browser) and `/.well-known/oauth-protected-resource`.
- **Do not buffer responses.** A tool's reply may be a server-sent event stream.
- **Allow long calls.** A tool call may run for minutes; set the read timeout accordingly, and make sure a load
  balancer in front does not close idle connections sooner.
- **Pass headers through.** `Authorization` and `MCP-Protocol-Version` must arrive, and so must the cookie the connect
  pages use. Setting `X-Forwarded-*` belongs here, between the proxy and mcp-fuse; mcp-fuse does not forward it on.
- Allow `POST`. mcp-fuse answers `GET` and `DELETE` on the MCP path with `405`.
- Do not expose the backends separately if agents should only see the fused endpoint.

## Operating it

- **Health:** `GET /healthz` returns `{"status":"ok","version":"…"}`. It does not probe backends.
- **Logs** are JSON lines on standard output: `msg`, `level`, and for calls `method`, `tool`, `backend`, `status`,
  `ms`, `request_id`. Backend failures and clashes between backends' names are logged at `WARN`.
- **Stopping:** `SIGTERM` stops accepting requests and gives in-flight calls up to 30 seconds.
- **Upgrading:** replace the binary and restart. Grants survive (they are in the state file). Backend sessions are
  reopened on demand, and a connect that was in progress has to be started again.
- **Backing up:** the state file and the secret file together. One without the other is useless.
- **A backend down** removes only its tools from the lists, until it answers again.

## Registering mcp-fuse elsewhere

Before people can connect a backend, two registrations must exist (details in [authorization](authorization.md)):

1. at the authorization server agents log in at: the `auth.client_id` client, with redirect URI
   `<resource>/connect/identify`, allowed to introspect tokens issued for mcp-fuse;
2. at each backend's authorization server: mcp-fuse as a client, with redirect URI `<resource>/connect/callback`
   (or let it register itself, if the server supports dynamic registration).
