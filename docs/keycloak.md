# A complete setup with Keycloak

**English** | [한국어](keycloak.ko.md) | [日本語](keycloak.ja.md)

One [Keycloak](https://github.com/keycloak/keycloak) realm serves as both the authorization server agents log in at
and the authorization server of every backend. This page covers running it, what to set in its admin console, the
matching mcp-fuse configuration, and how to check the result. It needs no experimental Keycloak feature. Why each
piece is needed is explained in [authorization](authorization.md) and [adding your MCP server](backends.md); this page
does not repeat it.

The examples use these names. Replace them with yours.

| | Example |
|---|---|
| Keycloak | `https://auth.example.com`, realm `example` → issuer `https://auth.example.com/realms/example` |
| mcp-fuse `resource` | `https://mcp.example.com/mcp` |
| Backends | `orders` at `https://orders.example.com/mcp`, `analytics` at `https://analytics.example.com/mcp` |

## What you end up with

```
                 token, aud = mcp-fuse              token, aud = orders
agent ─────────────────────────────────> mcp-fuse ─────────────────────> orders
                                            └──────────────────────────> analytics
                                                    token, aud = analytics
person's browser ──── sign in ────> Keycloak realm `example` (issues all three kinds of token)
```

| In the realm | Name | What it is for |
|---|---|---|
| Client scope, **default** | `mcp-fuse` | Puts mcp-fuse's `resource` in the `aud` of agents' tokens |
| Client scope, one per backend | `orders`, `analytics` | Puts that backend's URL in `aud`, only when it is asked for |
| Client | `mcp-fuse` | Checks agents' tokens (introspection) and identifies a person who connects a backend |
| Client | `mcp-fuse-backends` | The client mcp-fuse is to every backend |
| Client, one per agent | e.g. `desktop-agent` | The agent itself |

## 1. Run Keycloak

Keycloak and its database, as two containers. Pin both images.

```yaml
# docker-compose.yml
services:
  db:
    image: postgres:16
    environment:
      POSTGRES_DB: keycloak
      POSTGRES_USER: keycloak
      POSTGRES_PASSWORD_FILE: /run/secrets/db-password
    volumes:
      - ./pgdata:/var/lib/postgresql/data
      - ./db-password:/run/secrets/db-password:ro

  keycloak:
    image: quay.io/keycloak/keycloak:26.8.1
    command: ["start"]
    env_file: [keycloak.env]
    depends_on: [db]
    ports: ["127.0.0.1:8080:8080"]
```

```sh
# keycloak.env (readable by root only: it holds two secrets)
KC_DB=postgres
KC_DB_URL=jdbc:postgresql://db:5432/keycloak
KC_DB_USERNAME=keycloak
KC_DB_PASSWORD=<database password>
KC_HOSTNAME=https://auth.example.com
KC_HOSTNAME_ADMIN=http://localhost:8080
KC_HTTP_ENABLED=true
KC_PROXY_HEADERS=xforwarded
KC_BOOTSTRAP_ADMIN_USERNAME=admin
KC_BOOTSTRAP_ADMIN_PASSWORD=<admin password>
```

In front of it, a TLS-terminating reverse proxy for `auth.example.com`:

- Forward `/` to `127.0.0.1:8080` with `X-Forwarded-For`, `-Proto`, `-Host` and `-Port` set. Give it large header
  buffers: Keycloak's redirects carry long headers (in nginx, `proxy_buffer_size 128k; proxy_buffers 4 256k;`).
- **Answer `404` for `/admin/` and `/realms/master/`.** Whoever can sign in to the admin console can issue a token as
  anyone. With `KC_HOSTNAME_ADMIN` as above, the console is used through a tunnel:
  `ssh -L 8080:127.0.0.1:8080 <keycloak host>`, then `http://localhost:8080/admin/`.

Back up the database: it holds the realm's signing keys. Keycloak supports only its latest minor release, and an
upgrade is not rolling, so plan each one as maintenance
([release policy](https://github.com/keycloak/keycloak/blob/main/RELEASES.md)).

## 2. The realm, in the admin console

Screen names are those of Keycloak 26. Each table is one screen: where to go, then what to set.

### 2.1 The realm

| Where | Set |
|---|---|
| Realm list → **Create realm** | *Realm name* `example` |
| **Realm settings** → *Login* | *User registration* Off, *Forgot password* Off, *Remember me* Off — people come from your sign-in (2.2) |
| **Realm settings** → *Tokens* | *Access Token Lifespan* `5` minutes. A person you disable keeps a token until it expires, so this is how long disabling takes |

### 2.2 People

Keycloak does not replace your user store. It reads it, or sends people to your sign-in.

| Where | Set |
|---|---|
| **Identity providers** → *OpenID Connect v1.0* (or *SAML v2.0*) | Your existing sign-in. *Trust Email* On if it verifies email addresses |
| or **User federation** → *LDAP* | Your directory, read in place |
| **Authentication** → *first broker login* → *Review Profile* | *Update profile on first login* `off`, if your sign-in fills every attribute |
| **Realm settings** → *User profile* → `email` | *Who can edit* **Admin** only |
| **Realm settings** → *User profile* → **Create attribute** | Every attribute a backend acts on (e.g. `tenant_id`): *Who can view* and *Who can edit* **Admin** only |

A backend acts on what a token says, and the signature proves only who issued it. An attribute a person can edit is
an attribute a person can use to change what they are allowed to see.

To put such an attribute in tokens:

| Where | Set |
|---|---|
| **Client scopes** → **Create client scope** | *Name* `tenant`, *Type* **Default**, *Include in token scope* On |
| `tenant` → *Mappers* → **Configure a new mapper** → *User Attribute* | *User Attribute* `tenant_id`, *Token Claim Name* `tenant_id`, *Add to access token* On, *Add to token introspection* On |

### 2.3 Audiences

Keycloak does not act on the `resource` parameter by default, so the audience comes from client scopes: one for
mcp-fuse, one per backend.

| Where | Set |
|---|---|
| **Client scopes** → **Create client scope** | *Name* `mcp-fuse`, *Type* **Default**, *Include in token scope* On |
| `mcp-fuse` → *Mappers* → **Configure a new mapper** → *Audience* | *Name* `aud mcp-fuse`, *Included Custom Audience* `https://mcp.example.com/mcp`, *Add to access token* On, *Add to ID token* Off, *Add to token introspection* On |
| **Client scopes** → **Create client scope** | *Name* `orders` — the scope the backend lists in its protected resource metadata as `scopes_supported` — *Type* **None**, *Include in token scope* On |
| `orders` → *Mappers* → *Audience* | *Included Custom Audience* `https://orders.example.com/mcp`, exactly as the backend's metadata gives its `resource`. Same three switches as above |

Repeat the last two rows for `analytics`.

A backend's scope is **not** a default scope. A default scope goes into every client's tokens, so an agent's token
would then open the backend directly, past mcp-fuse.

### 2.4 Client `mcp-fuse`

| Where | Set |
|---|---|
| **Clients** → **Create client** → *General settings* | *Client type* OpenID Connect, *Client ID* `mcp-fuse` |
| → *Capability config* | *Client authentication* On, *Standard flow* On, *Direct access grants* Off, *PKCE Method* `S256` |
| → *Login settings* | *Valid redirect URIs* `https://mcp.example.com/mcp/connect/identify` |
| `mcp-fuse` → *Settings* → *Login settings* | *Consent required* Off |
| `mcp-fuse` → *Credentials* | Copy *Client Secret* into the file named by `auth.client_secret_file` |

### 2.5 Client `mcp-fuse-backends`

| Where | Set |
|---|---|
| **Clients** → **Create client** | As in 2.4, with *Client ID* `mcp-fuse-backends` and *Valid redirect URIs* `https://mcp.example.com/mcp/connect/callback` |
| `mcp-fuse-backends` → *Client scopes* | Remove `mcp-fuse`: a backend's token is not for mcp-fuse |
| `mcp-fuse-backends` → *Client scopes* → **Add client scope** | `orders`, `analytics`, added as **Optional** |
| `mcp-fuse-backends` → *Credentials* | Copy *Client Secret* into the file named by each backend's `client_secret_file` |

Refresh tokens are on by default for a standard-flow client; leave them on.

### 2.6 Agents

An agent is a client of the realm. Create its client after 2.3 so that it gets the `mcp-fuse` scope.

| How | Where | Set |
|---|---|---|
| **Registered in advance** | **Clients** → **Create client** | The *Client ID* and *Valid redirect URIs* the agent asks for. *Client authentication* Off for a desktop agent (a public client), *PKCE Method* `S256` |
| **Dynamic registration** | **Clients** → *Client registration* → *Anonymous access policies* → *Trusted Hosts* | The hosts allowed to register. Leave the list empty to keep registration closed, which is the default |

Agents that identify themselves with a client ID metadata document need an experimental feature; see
[authorization](authorization.md#with-keycloak).

## 3. mcp-fuse

```json
{
  "resource": "https://mcp.example.com/mcp",
  "authorization_servers": ["https://auth.example.com/realms/example"],
  "auth": {
    "introspection_url": "https://auth.example.com/realms/example/protocol/openid-connect/token/introspect",
    "client_id": "mcp-fuse",
    "client_secret_file": "/etc/mcp-fuse/front-client-secret"
  },
  "state_file": "/var/lib/mcp-fuse/state.bin",
  "secret_file": "/etc/mcp-fuse/secret",
  "backends": [
    {"name": "orders", "url": "https://orders.example.com/mcp",
     "client_id": "mcp-fuse-backends", "client_secret_file": "/etc/mcp-fuse/backends-client-secret"},
    {"name": "analytics", "url": "https://analytics.example.com/mcp",
     "client_id": "mcp-fuse-backends", "client_secret_file": "/etc/mcp-fuse/backends-client-secret"}
  ]
}
```

Every key is in [configuration](configuration.md); the reverse proxy and the service are in
[deployment](deployment.md).

## 4. Each backend

Each backend is a resource server of the same realm ([adding your MCP server](backends.md)):

- It accepts tokens from issuer `https://auth.example.com/realms/example`, with the keys published at
  `https://auth.example.com/realms/example/protocol/openid-connect/certs`.
- It accepts a token only if its own URL is in `aud`.
- Its protected resource metadata lists the realm in `authorization_servers` and its scope (`orders`) in
  `scopes_supported`.

## 5. Check

| Check | How | Expect |
|---|---|---|
| An agent's token | **Clients** → the agent → *Client scopes* → *Evaluate*, pick a user → *Generated access token* | `aud` has `https://mcp.example.com/mcp` and no backend URL |
| A backend's token | The same on `mcp-fuse-backends`, with `orders` selected as an optional scope | `aud` has `https://orders.example.com/mcp` |
| The attributes | Either token above | `tenant_id` is there, and the user cannot change it on their account page |
| Introspection | `curl -u mcp-fuse:<secret> -d token=<an agent's token> <introspection_url>` | `"active": true` and the `aud` above |
| Connecting | Connect the agent; open the link `initialize` gives; sign in | The backend's tools appear in the agent's list |
| Refusals | Present a backend's token to mcp-fuse, and an agent's token to a backend | Both are refused with `401` |
