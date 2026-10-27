# A complete setup with Keycloak

[English](keycloak.md) | [한국어](keycloak.ko.md) | **日本語**

[Keycloak](https://github.com/keycloak/keycloak) の realm ひとつが、エージェントがログインする認可サーバーと、すべての
backend の認可サーバーを兼ねます。このドキュメントでは、Keycloak の起動、管理コンソールで設定すること、それに合わせた
mcp-fuse の設定、結果の確認方法を扱います。Keycloak の実験的機能は使いません。各要素がなぜ必要かは
[authorization](authorization.ja.md) と [adding your MCP server](backends.ja.md) にあり、ここでは繰り返しません。

例では次の名前を使います。自分の値に置き換えてください。

| | 例 |
|---|---|
| Keycloak | `https://auth.example.com`、realm `example` → issuer `https://auth.example.com/realms/example` |
| mcp-fuse `resource` | `https://mcp.example.com/mcp` |
| Backend | `orders` は `https://orders.example.com/mcp`、`analytics` は `https://analytics.example.com/mcp` |

## 出来上がる構成

```
                 token, aud = mcp-fuse              token, aud = orders
agent ─────────────────────────────────> mcp-fuse ─────────────────────> orders
                                            └──────────────────────────> analytics
                                                    token, aud = analytics
person's browser ──── sign in ────> Keycloak realm `example` (issues all three kinds of token)
```

| realm の中のもの | 名前 | 用途 |
|---|---|---|
| Client scope、**既定** | `mcp-fuse` | エージェントのトークンの `aud` に mcp-fuse の `resource` を入れます |
| Client scope、backend ごとにひとつ | `orders`、`analytics` | 求められたときだけ、その backend の URL を `aud` に入れます |
| Client | `mcp-fuse` | エージェントのトークンを確認し（introspection）、backend を接続する人が誰かを確認します |
| Client | `mcp-fuse-backends` | すべての backend に対して mcp-fuse が使う client |
| Client、エージェントごとにひとつ | 例：`desktop-agent` | エージェント自身 |

## 1. Keycloak を起動する

Keycloak とそのデータベースを、2 つのコンテナとして起動します。どちらのイメージもバージョンを固定します。

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
# keycloak.env（root だけが読めるように：secret が 2 つ入っています）
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

前段には `auth.example.com` の TLS を終端するリバースプロキシを置きます。

- `/` を `127.0.0.1:8080` へ転送し、`X-Forwarded-For`、`-Proto`、`-Host`、`-Port` を設定します。Keycloak の
  リダイレクトはヘッダーが長いので、ヘッダーバッファを大きくします（nginx なら `proxy_buffer_size 128k; proxy_buffers 4 256k;`）。
- **`/admin/` と `/realms/master/` には `404` を返します。** 管理コンソールにサインインできる人は、誰の名義でもトークンを
  発行できます。上のように `KC_HOSTNAME_ADMIN` を設定すると、管理コンソールはトンネル経由で使います：
  `ssh -L 8080:127.0.0.1:8080 <keycloak host>` のあと `http://localhost:8080/admin/`。

データベースをバックアップしてください。realm の署名鍵がそこにあります。Keycloak は最新のマイナーリリースだけを
サポートし、アップグレードはローリングではないので、アップグレードごとにメンテナンス時間を取ります
（[リリースポリシー](https://github.com/keycloak/keycloak/blob/main/RELEASES.md)）。

## 2. 管理コンソールで realm を設定する

画面名は Keycloak 26 のものです。表ひとつが画面ひとつです。どこへ行き、何を設定するかを書いています。

### 2.1 Realm

| どこで | 設定 |
|---|---|
| Realm 一覧 → **Create realm** | *Realm name* `example` |
| **Realm settings** → *Login* | *User registration* Off、*Forgot password* Off、*Remember me* Off — 人は既存のサインインから来ます（2.2） |
| **Realm settings** → *Tokens* | *Access Token Lifespan* `5` 分。無効にした人も、トークンが期限切れになるまではそのトークンを使えるので、この値が無効化が反映されるまでの時間です |

### 2.2 人

Keycloak は既存のユーザーストアを置き換えません。それを読むか、人を既存のサインインへ送ります。

| どこで | 設定 |
|---|---|
| **Identity providers** → *OpenID Connect v1.0*（または *SAML v2.0*） | 既存のサインイン。メールアドレスを検証しているなら *Trust Email* On |
| または **User federation** → *LDAP* | ディレクトリをその場で読みます |
| **Authentication** → *first broker login* → *Review Profile* | 既存のサインインがすべての属性を埋めるなら *Update profile on first login* `off` |
| **Realm settings** → *User profile* → `email` | *Who can edit* は **Admin** のみ |
| **Realm settings** → *User profile* → **Create attribute** | backend が判断に使うすべての属性（例：`tenant_id`）：*Who can view* と *Who can edit* は **Admin** のみ |

backend はトークンに書かれたとおりに動き、署名は誰が発行したかしか証明しません。本人が編集できる属性は、本人が
自分の見られる範囲を変えるのに使える属性です。

そうした属性をトークンに入れるには：

| どこで | 設定 |
|---|---|
| **Client scopes** → **Create client scope** | *Name* `tenant`、*Type* **Default**、*Include in token scope* On |
| `tenant` → *Mappers* → **Configure a new mapper** → *User Attribute* | *User Attribute* `tenant_id`、*Token Claim Name* `tenant_id`、*Add to access token* On、*Add to token introspection* On |

### 2.3 Audience

Keycloak は既定では `resource` パラメータを処理しないので、audience は client scope で決めます。mcp-fuse 用にひとつ、
backend ごとにひとつです。

| どこで | 設定 |
|---|---|
| **Client scopes** → **Create client scope** | *Name* `mcp-fuse`、*Type* **Default**、*Include in token scope* On |
| `mcp-fuse` → *Mappers* → **Configure a new mapper** → *Audience* | *Name* `aud mcp-fuse`、*Included Custom Audience* `https://mcp.example.com/mcp`、*Add to access token* On、*Add to ID token* Off、*Add to token introspection* On |
| **Client scopes** → **Create client scope** | *Name* `orders` — backend が protected resource metadata の `scopes_supported` に挙げている scope — *Type* **None**、*Include in token scope* On |
| `orders` → *Mappers* → *Audience* | *Included Custom Audience* `https://orders.example.com/mcp`。backend のメタデータの `resource` と一字一句同じに。3 つのスイッチは上と同じです |

`analytics` について最後の 2 行を繰り返します。

backend の scope は既定の scope に**しません**。既定の scope はすべての client のトークンに入るので、そうすると
エージェントのトークンが mcp-fuse を通らずに backend を直接開けるようになります。

### 2.4 Client `mcp-fuse`

| どこで | 設定 |
|---|---|
| **Clients** → **Create client** → *General settings* | *Client type* OpenID Connect、*Client ID* `mcp-fuse` |
| → *Capability config* | *Client authentication* On、*Standard flow* On、*Direct access grants* Off、*PKCE Method* `S256` |
| → *Login settings* | *Valid redirect URIs* `https://mcp.example.com/mcp/connect/identify` |
| `mcp-fuse` → *Settings* → *Login settings* | *Consent required* Off |
| `mcp-fuse` → *Credentials* | *Client Secret* を `auth.client_secret_file` が指すファイルに入れます |

### 2.5 Client `mcp-fuse-backends`

| どこで | 設定 |
|---|---|
| **Clients** → **Create client** | 2.4 と同じにし、*Client ID* `mcp-fuse-backends`、*Valid redirect URIs* `https://mcp.example.com/mcp/connect/callback` |
| `mcp-fuse-backends` → *Client scopes* | `mcp-fuse` を外します。backend 用のトークンは mcp-fuse のためのものではありません |
| `mcp-fuse-backends` → *Client scopes* → **Add client scope** | `orders`、`analytics` を **Optional** で追加 |
| `mcp-fuse-backends` → *Credentials* | *Client Secret* を各 backend の `client_secret_file` が指すファイルに入れます |

standard flow の client では refresh token が既定で有効です。有効のままにします。

### 2.6 エージェント

エージェントは realm の client です。`mcp-fuse` scope を受け取るよう、2.3 のあとに作ります。

| 方法 | どこで | 設定 |
|---|---|---|
| **事前登録** | **Clients** → **Create client** | エージェントが求める *Client ID* と *Valid redirect URIs*。デスクトップのエージェント（public client）は *Client authentication* Off、*PKCE Method* `S256` |
| **動的登録** | **Clients** → *Client registration* → *Anonymous access policies* → *Trusted Hosts* | 登録を許すホスト。一覧を空にしておくと登録は閉じたままで、これが既定です |

client ID metadata document で自分を知らせるエージェントには実験的機能が必要です。
[authorization](authorization.ja.md#keycloak-の場合) を参照してください。

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

すべてのキーは [configuration](configuration.ja.md) に、リバースプロキシとサービスは [deployment](deployment.ja.md) に
あります。

## 4. 各 backend

各 backend は同じ realm の resource server です（[adding your MCP server](backends.ja.md)）。

- issuer `https://auth.example.com/realms/example` のトークンを受け付け、鍵は
  `https://auth.example.com/realms/example/protocol/openid-connect/certs` から読みます。
- 自分の URL が `aud` にあるトークンだけを受け付けます。
- protected resource metadata の `authorization_servers` に realm を、`scopes_supported` に自分の scope（`orders`）を
  挙げます。

## 5. 確認

| 確認すること | 方法 | 期待する結果 |
|---|---|---|
| エージェントのトークン | **Clients** → そのエージェント → *Client scopes* → *Evaluate*、ユーザーを選ぶ → *Generated access token* | `aud` に `https://mcp.example.com/mcp` があり、backend の URL はありません |
| backend のトークン | `mcp-fuse-backends` で同じ画面、optional scope に `orders` を選ぶ | `aud` に `https://orders.example.com/mcp` があります |
| 属性 | 上のどちらかのトークン | `tenant_id` があり、ユーザーは自分のアカウントページで変更できません |
| Introspection | `curl -u mcp-fuse:<secret> -d token=<エージェントのトークン> <introspection_url>` | `"active": true`、そして上の `aud` |
| 接続 | エージェントを接続し、`initialize` が示すリンクを開いてサインイン | backend のツールがエージェントの一覧に現れます |
| 拒否 | backend のトークンを mcp-fuse に、エージェントのトークンを backend に提示 | どちらも `401` で拒否されます |
