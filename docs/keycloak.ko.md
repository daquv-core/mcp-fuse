# A complete setup with Keycloak

[English](keycloak.md) | **한국어** | [日本語](keycloak.ja.md)

[Keycloak](https://github.com/keycloak/keycloak) realm 하나가 에이전트가 로그인하는 인가 서버이자 모든 backend의
인가 서버를 겸합니다. 이 문서는 Keycloak 실행, 관리 화면에서 설정할 것, 그에 맞는 mcp-fuse 설정, 결과 확인 방법을
다룹니다. Keycloak의 실험 기능은 쓰지 않습니다. 각 요소가 왜 필요한지는 [authorization](authorization.ko.md)과
[adding your MCP server](backends.ko.md)에 있으며 여기서 반복하지 않습니다.

예시는 아래 이름을 씁니다. 각자의 값으로 바꾸세요.

| | 예시 |
|---|---|
| Keycloak | `https://auth.example.com`, realm `example` → issuer `https://auth.example.com/realms/example` |
| mcp-fuse `resource` | `https://mcp.example.com/mcp` |
| Backend | `orders`는 `https://orders.example.com/mcp`, `analytics`는 `https://analytics.example.com/mcp` |

## 완성된 모습

```
                 token, aud = mcp-fuse              token, aud = orders
agent ─────────────────────────────────> mcp-fuse ─────────────────────> orders
                                            └──────────────────────────> analytics
                                                    token, aud = analytics
person's browser ──── sign in ────> Keycloak realm `example` (issues all three kinds of token)
```

| realm 안의 것 | 이름 | 용도 |
|---|---|---|
| Client scope, **기본** | `mcp-fuse` | 에이전트 토큰의 `aud`에 mcp-fuse의 `resource`를 넣습니다 |
| Client scope, backend마다 하나 | `orders`, `analytics` | 요청받았을 때만 그 backend의 URL을 `aud`에 넣습니다 |
| Client | `mcp-fuse` | 에이전트 토큰을 확인하고(introspection), backend를 연결하는 사람이 누구인지 확인합니다 |
| Client | `mcp-fuse-backends` | 모든 backend에 대해 mcp-fuse가 쓰는 client |
| Client, 에이전트마다 하나 | 예: `desktop-agent` | 에이전트 자신 |

## 1. Keycloak 실행

Keycloak과 그 데이터베이스를 컨테이너 두 개로 띄웁니다. 두 이미지 모두 버전을 고정합니다.

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
# keycloak.env (root만 읽을 수 있게: secret 두 개가 들어 있습니다)
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

앞에는 `auth.example.com`의 TLS를 처리하는 리버스 프록시를 둡니다.

- `/`를 `127.0.0.1:8080`으로 넘기고 `X-Forwarded-For`, `-Proto`, `-Host`, `-Port`를 설정합니다. Keycloak의
  리다이렉트는 헤더가 길므로 헤더 버퍼를 크게 잡습니다(nginx라면 `proxy_buffer_size 128k; proxy_buffers 4 256k;`).
- **`/admin/`과 `/realms/master/`에는 `404`로 답합니다.** 관리 화면에 로그인할 수 있는 사람은 누구의 이름으로든 토큰을
  발급할 수 있습니다. 위처럼 `KC_HOSTNAME_ADMIN`을 두면 관리 화면은 터널로 씁니다:
  `ssh -L 8080:127.0.0.1:8080 <keycloak host>` 후 `http://localhost:8080/admin/`.

데이터베이스를 백업하세요. realm의 서명 키가 거기에 있습니다. Keycloak은 최신 마이너 릴리스만 지원하고 업그레이드는
무중단(rolling)이 아니므로, 업그레이드마다 점검 시간을 잡습니다
([릴리스 정책](https://github.com/keycloak/keycloak/blob/main/RELEASES.md)).

## 2. 관리 화면에서 realm 설정

화면 이름은 Keycloak 26 기준입니다. 표 하나가 화면 하나입니다. 어디로 가서 무엇을 설정하는지 적었습니다.

### 2.1 Realm

| 어디서 | 설정 |
|---|---|
| Realm 목록 → **Create realm** | *Realm name* `example` |
| **Realm settings** → *Login* | *User registration* Off, *Forgot password* Off, *Remember me* Off — 사람은 기존 로그인에서 옵니다(2.2) |
| **Realm settings** → *Tokens* | *Access Token Lifespan* `5`분. 비활성화한 사람도 토큰이 만료될 때까지는 그 토큰을 쓸 수 있으므로, 이 값이 비활성화가 반영되는 시간입니다 |

### 2.2 사람

Keycloak은 기존 사용자 저장소를 대체하지 않습니다. 그것을 읽거나, 사람을 기존 로그인으로 보냅니다.

| 어디서 | 설정 |
|---|---|
| **Identity providers** → *OpenID Connect v1.0* (또는 *SAML v2.0*) | 기존 로그인. 이메일 주소를 검증한다면 *Trust Email* On |
| 또는 **User federation** → *LDAP* | 디렉터리를 그 자리에서 읽습니다 |
| **Authentication** → *first broker login* → *Review Profile* | 기존 로그인이 모든 속성을 채운다면 *Update profile on first login* `off` |
| **Realm settings** → *User profile* → `email` | *Who can edit*는 **Admin**만 |
| **Realm settings** → *User profile* → **Create attribute** | backend가 판단에 쓰는 모든 속성(예: `tenant_id`): *Who can view*와 *Who can edit*는 **Admin**만 |

backend는 토큰에 적힌 대로 동작하고, 서명은 누가 발급했는지만 증명합니다. 사람이 고칠 수 있는 속성은 그 사람이 자기가
볼 수 있는 범위를 바꾸는 데 쓸 수 있는 속성입니다.

그런 속성을 토큰에 넣으려면:

| 어디서 | 설정 |
|---|---|
| **Client scopes** → **Create client scope** | *Name* `tenant`, *Type* **Default**, *Include in token scope* On |
| `tenant` → *Mappers* → **Configure a new mapper** → *User Attribute* | *User Attribute* `tenant_id`, *Token Claim Name* `tenant_id`, *Add to access token* On, *Add to token introspection* On |

### 2.3 Audience

Keycloak은 기본 설정으로는 `resource` 파라미터를 처리하지 않으므로, audience는 client scope로 정합니다. mcp-fuse용
하나, backend마다 하나입니다.

| 어디서 | 설정 |
|---|---|
| **Client scopes** → **Create client scope** | *Name* `mcp-fuse`, *Type* **Default**, *Include in token scope* On |
| `mcp-fuse` → *Mappers* → **Configure a new mapper** → *Audience* | *Name* `aud mcp-fuse`, *Included Custom Audience* `https://mcp.example.com/mcp`, *Add to access token* On, *Add to ID token* Off, *Add to token introspection* On |
| **Client scopes** → **Create client scope** | *Name* `orders` — backend가 protected resource metadata의 `scopes_supported`에 올린 scope — *Type* **None**, *Include in token scope* On |
| `orders` → *Mappers* → *Audience* | *Included Custom Audience* `https://orders.example.com/mcp`. backend 메타데이터의 `resource`와 글자 하나까지 같게. 스위치 세 개는 위와 같습니다 |

`analytics`에 대해 마지막 두 줄을 반복합니다.

backend의 scope는 기본 scope로 두지 **않습니다**. 기본 scope는 모든 client의 토큰에 들어가므로, 그러면 에이전트의 토큰이
mcp-fuse를 거치지 않고 backend를 바로 열 수 있게 됩니다.

### 2.4 Client `mcp-fuse`

| 어디서 | 설정 |
|---|---|
| **Clients** → **Create client** → *General settings* | *Client type* OpenID Connect, *Client ID* `mcp-fuse` |
| → *Capability config* | *Client authentication* On, *Standard flow* On, *Direct access grants* Off, *PKCE Method* `S256` |
| → *Login settings* | *Valid redirect URIs* `https://mcp.example.com/mcp/connect/identify` |
| `mcp-fuse` → *Settings* → *Login settings* | *Consent required* Off |
| `mcp-fuse` → *Credentials* | *Client Secret*을 `auth.client_secret_file`이 가리키는 파일에 넣습니다 |

### 2.5 Client `mcp-fuse-backends`

| 어디서 | 설정 |
|---|---|
| **Clients** → **Create client** | 2.4와 같게 하되 *Client ID* `mcp-fuse-backends`, *Valid redirect URIs* `https://mcp.example.com/mcp/connect/callback` |
| `mcp-fuse-backends` → *Client scopes* | `mcp-fuse`를 제거합니다. backend용 토큰은 mcp-fuse를 위한 것이 아닙니다 |
| `mcp-fuse-backends` → *Client scopes* → **Add client scope** | `orders`, `analytics`를 **Optional**로 추가 |
| `mcp-fuse-backends` → *Credentials* | *Client Secret*을 각 backend의 `client_secret_file`이 가리키는 파일에 넣습니다 |

standard flow client는 refresh token이 기본으로 켜져 있습니다. 켜 둔 채로 둡니다.

### 2.6 에이전트

에이전트는 realm의 client입니다. `mcp-fuse` scope를 받도록 2.3 다음에 만듭니다.

| 방법 | 어디서 | 설정 |
|---|---|---|
| **미리 등록** | **Clients** → **Create client** | 에이전트가 요구하는 *Client ID*와 *Valid redirect URIs*. 데스크톱 에이전트(public client)는 *Client authentication* Off, *PKCE Method* `S256` |
| **동적 등록** | **Clients** → *Client registration* → *Anonymous access policies* → *Trusted Hosts* | 등록을 허용할 호스트. 목록을 비워 두면 등록이 닫혀 있고, 이것이 기본값입니다 |

client ID metadata document로 자신을 알리는 에이전트에는 실험 기능이 필요합니다.
[authorization](authorization.ko.md#keycloak에서)을 보세요.

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

모든 키는 [configuration](configuration.ko.md)에, 리버스 프록시와 서비스는 [deployment](deployment.ko.md)에 있습니다.

## 4. 각 backend

각 backend는 같은 realm의 resource server입니다([adding your MCP server](backends.ko.md)).

- issuer `https://auth.example.com/realms/example`의 토큰을 받고, 키는
  `https://auth.example.com/realms/example/protocol/openid-connect/certs`에서 읽습니다.
- 자기 URL이 `aud`에 있는 토큰만 받습니다.
- protected resource metadata의 `authorization_servers`에 realm을, `scopes_supported`에 자기 scope(`orders`)를
  올립니다.

## 5. 확인

| 확인할 것 | 방법 | 기대 결과 |
|---|---|---|
| 에이전트 토큰 | **Clients** → 그 에이전트 → *Client scopes* → *Evaluate*, 사용자 선택 → *Generated access token* | `aud`에 `https://mcp.example.com/mcp`가 있고 backend URL은 없습니다 |
| backend 토큰 | `mcp-fuse-backends`에서 같은 화면, optional scope로 `orders` 선택 | `aud`에 `https://orders.example.com/mcp`가 있습니다 |
| 속성 | 위 두 토큰 중 하나 | `tenant_id`가 있고, 사용자가 자기 계정 페이지에서 고칠 수 없습니다 |
| Introspection | `curl -u mcp-fuse:<secret> -d token=<에이전트 토큰> <introspection_url>` | `"active": true`, 그리고 위의 `aud` |
| 연결 | 에이전트를 연결하고, `initialize`가 준 링크를 열어 로그인 | backend의 도구가 에이전트 목록에 나타납니다 |
| 거절 | backend 토큰을 mcp-fuse에, 에이전트 토큰을 backend에 제시 | 둘 다 `401`로 거절됩니다 |
