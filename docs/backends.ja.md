# Adding your MCP server

[English](backends.md) | [한국어](backends.ko.md) | **日本語**

MCP サーバー（アプリケーションの操作、データ照会サーバー、ツールサーバーなど）があり、それを他のサーバーと一緒に
1 つのエンドポイントの背後に見せたいとします。**mcp-fuse のためにサーバーを変更する必要はありません。** MCP 標準に
従って作り、サーバーの認可サーバーに mcp-fuse を知らせ、mcp-fuse の設定に追加します。

## 認可で誰が何をするか

MCP サーバーの認可は 2 つの部分に分かれ、通常は別々のソフトウェアが担います。

| 部分 | 行うこと | 誰が提供するか |
|---|---|---|
| **認可サーバー** | 人をサインインさせ、クライアントを登録し、PKCE 付きの authorization code フローを実行し、トークンを発行・更新し、メタデータと署名鍵を公開します。 | 自分で作るか、既製のものを運用します。 |
| **リソースサーバー**（あなたの MCP サーバー） | メタデータを指す `401` を返し、保護リソースメタデータを公開し、受け取ったすべてのトークンを確認します。署名、issuer、有効期限、そして**自分が audience であること**です。 | 常にあなたの MCP サーバーです。 |

2 番目の部分は、どの認可サーバーを使っても認可サーバー側へは移りません。自分のアドレスが audience であることを知って
いるのはあなたのサーバーだけで、トークン内の ID が何を意味するか（どのユーザーで、そのユーザーが何をしてよいか）を
知っているのはあなたのアプリケーションだけだからです。必要なことは 1 節にあります。

### 認可サーバーをどこから得るか

標準に従う認可サーバーがまだない場合は、**[Keycloak](https://github.com/keycloak/keycloak) の運用をお勧めします。**
オープンソース（Apache 2.0）で、上の表の認可サーバーの仕事をすべて行い、MCP の提供方法も文書化されています
（[Keycloak の MCP ガイド](https://www.keycloak.org/securing-apps/mcp-authz-server)）。自作も可能で下に説明しますが、
製品が続く限りレビューとパッチ適用を続けるセキュリティ上敏感なコードになります。

#### Keycloak を使う場合にすること

1. **運用して維持します。** 本番用のデータベースを用意してバックアップを保護してください。既定の構成では、realm の
   設定とユーザーに加えて、realm の署名鍵もそこに保管されます。アップグレードを計画してください。Keycloak は最新の
   マイナーリリースをサポートします（[リリースポリシー](https://github.com/keycloak/keycloak/blob/main/RELEASES.md)）。
2. **既存のサインインを接続します。** OpenID Connect で既存のサインインを identity provider として追加します。既存の
   サインインが OpenID Connect でなければ、その前に小さなブリッジを置きます。
3. **mcp-fuse を Keycloak のクライアントにします。** PKCE `S256` 付きの authorization code、リフレッシュトークン、
   リダイレクト URI `<mcp-fuse resource origin><resource path>/connect/callback`。動的クライアント登録はありますが、
   既定では匿名のリクエストには閉じられています。クライアントを手動で作る（mcp-fuse にその `client_id` を渡します。
   3 節）か、initial access token を発行してください。
4. **audience をサーバーの URL にします。** Keycloak のガイドに方法があります。*Audience* マッパーを持つクライアント
   スコープを作り、その *Included Custom Audience* をサーバーの URL にして、`scope` で要求されるようにします（その
   スコープを保護リソースメタデータの `scopes_supported` に書いてください。チャレンジにスコープがないとき mcp-fuse が
   要求するのがこれです）。Keycloak は `resource` パラメーターを直接処理することもできます。どのリリースにあり、どう
   有効にするかは Keycloak のガイドにあります。これを確認する必要がある理由は、下の 2 点のうち最初のものです。
5. **ID クレームを信頼できるものにします。** サーバーがメールアドレスで人を識別するなら、identity provider が検証した
   アドレスだけを受け入れ（identity provider の *trust email* 設定）、ユーザーが自分のアドレスを変更できないように
   してください（realm の user profile で *email* 属性の edit 権限）。サーバーはその値に基づいて動作します。下の 2 点の
   うち 2 番目を参照してください。
6. **上の表のリソースサーバーの部分を行います。** 1 節にまとめられています。

#### それ以外を使う場合にすること

- **すでにある認可サーバー:** 1 節、2 節と下の 2 点に照らして確認し、そのまま使います。そのうえでリソースサーバーの
  部分を行います。
- **自分で作る認可サーバー:** PKCE `S256` 付きの authorization エンドポイント、リフレッシュトークンと `resource`
  パラメーターを扱う token エンドポイント、クライアント登録（動的または手動）、同意の手順、メタデータドキュメント、
  署名鍵の公開とローテーションを引き受けます。既存のサインインとの接続や、MCP 認可仕様の改訂への追従も行います。
  そのうえでリソースサーバーの部分を行います。

同じ認可サーバーが、呼び出し元のサインイン（mcp-fuse の設定の `authorization_servers`）と backend の両方を担うこと
もできます。mcp-fuse はそれでも backend ごとに別のトークンを取得します（[authorization](authorization.ja.md)）。共有して
得られるのは、サーバーがブラウザーのサインインセッションを保持していれば、backend の接続時に再度サインインを求め
られないことです。それ以外に何が表示されるかは、サーバーの同意設定によります。

エージェントがあなたの Keycloak でサインインするなら、エージェントが client ID metadata document で自分を知らせるように
してください（[authorization](authorization.ja.md#keycloak-の場合)）。

### 既製のサーバーでは分かりにくい 2 点

認可サーバーが何を公開し、何を受け付ける必要があるかは、1 節（メタデータ、PKCE `S256`、`resource` パラメーター、
リフレッシュトークン）と 2 節（mcp-fuse をクライアントとして登録する）にあります。それ以外に:

1. **既製のサーバーがトークンに入れる audience が、あなたのサーバーとは限りません。** 既定値が別のクライアントや
   サービスを指していることがあります。サーバーが発行したトークンをデコードして `aud` を読んでください。サーバーが
   `resource` を処理しない場合、audience は設定から与える必要があります。
2. **サーバーが人を識別するのに使うクレームは、ID トークンだけでなくアクセストークンに入っている**必要があり、
   検証済みの値である必要があります（メールアドレスなら、サーバーが検証済みとしているもの）。

## 1. 標準に従って作る

「標準」とは [MCP 仕様](https://modelcontextprotocol.io/specification/latest)です。mcp-fuse が、他の MCP クライアント
と同じように前提とするのは次のことです。

**トランスポート — [Streamable HTTP](https://modelcontextprotocol.io/specification/latest/basic/transports/streamable-http)。**
JSON-RPC メッセージの `POST` を受け付け、JSON で、または最後のフレームが応答であるイベントストリームで答える
エンドポイントが 1 つ。セッションを持たないサーバーは `Mcp-Session-Id` が不要で、持つサーバーは発行して構いません
（mcp-fuse はそれを送り返し、サーバーが忘れたセッションに `404` で答えた場合は新しいセッションを開きます）。

**ハンドシェイク。** mcp-fuse はプロトコルリビジョン `2025-11-25` と空の capability で `initialize` を送り、続いて
`notifications/initialized` を送ります。対応するリビジョンのうち `2025-11-25` より**新しくない**最も高いもので答えて
ください。それより新しいもので答えるサーバーは拒否されます。（ハンドシェイクのない 2026-07-28 リビジョンしか話さない
サーバーは、まだ統合できません。[protocol](protocol.ja.md) を参照してください。）

**メソッド。** `tools/list` と `tools/call`。あれば `prompts/*` と `resources/*`。実装していないメソッドは `-32601`
で答えて構わず、単に空として扱われます。ページ分割された一覧（`nextCursor`）は最後までたどります。

**認可 — [MCP 認可仕様](https://modelcontextprotocol.io/specification/latest/basic/authorization)。**
サーバーは OAuth リソースサーバーであり、mcp-fuse はその仕様がクライアントに定める方法で、必要なものをすべて見つけ
ます。

- トークンのないリクエストには、`WWW-Authenticate: Bearer resource_metadata="<url>"` を付けた `401` で答えます
  （またはメタデータを `/.well-known/oauth-protected-resource/<path>` で提供します）。
- その保護リソースメタデータ（[RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)）の `resource` はサーバーの URL
  （mcp-fuse に渡すもの）と等しく、`authorization_servers` を持ちます。
- 認可サーバーはメタデータ（[RFC 8414](https://www.rfc-editor.org/rfc/rfc8414) または OpenID discovery）を公開し、
  その `issuer` は上に挙げたものと正確に一致し、PKCE `S256` を示し、`authorization_endpoint` と `token_endpoint` を
  持ちます。認可レスポンスに `iss` を加える場合（[RFC 9207](https://www.rfc-editor.org/rfc/rfc9207)）は、
  `authorization_response_iss_parameter_supported` でそのことを示します。
- `resource` パラメーター（[RFC 8707](https://www.rfc-editor.org/rfc/rfc8707)）を受け付け、audience がサーバーである
  トークンとリフレッシュトークンを発行します。
- サーバーはすべてのトークンを検証し（署名、有効期限、**audience が自分であること**）、不正なものには `401` で
  答えます。

**一覧について 1 点。** 各一覧は要求した人のトークンで取得されるため、人によって異なることがあります。mcp-fuse で
誰に何を見せるかを決める方法がこれです。

mcp-fuse が送るのは MCP クライアントが送るものだけです。`Content-Type`、`Accept`、`User-Agent`、
`MCP-Protocol-Version`、サーバー自身の `Mcp-Session-Id`、サーバー自身の `Authorization`、ログを突き合わせるための
`X-Request-Id` です。`X-Forwarded-*` はなく、エンドユーザーのクライアントが選んだものもありません。

ツール、プロンプト、リソースの**名前**は、他のサーバーの名前と衝突しないようにしてください。衝突した場合は、
mcp-fuse の `backends` で先に書かれたサーバーがその名前を保持します。

## 2. mcp-fuse を認可サーバーに知らせる

mcp-fuse はサーバーの認可サーバーに対する OAuth クライアントです。次のどちらかを行います。

- [動的クライアント登録](https://www.rfc-editor.org/rfc/rfc7591)を有効にすると、mcp-fuse が自分で 1 回登録します。
- クライアントを手動で登録します。PKCE とリフレッシュトークン付きの authorization code grant、リダイレクト URI
  `<mcp-fuse resource origin><resource path>/connect/callback`（`https://mcp.example.com/mcp` なら
  `https://mcp.example.com/mcp/connect/callback`）。クライアントが confidential なら、mcp-fuse にそのシークレットを
  ファイルで渡します。

mcp-fuse は [client ID metadata document](https://modelcontextprotocol.io/specification/latest/basic/authorization/client-registration)
を使いません。仕様は動的登録よりこちらを推奨しています。認可サーバーがそれしか対応していない場合は、クライアントを
手動で登録してください（[authorization](authorization.ja.md)）。

## 3. mcp-fuse に追加する

設定（[リファレンス](configuration.ja.md)）に:

```json
{
  "backends": [
    {"name": "orders", "url": "https://orders.example.com/mcp"}
  ]
}
```

事前登録したクライアントがあれば `"client_id"` を、confidential なら `"client_secret_file"` も加えます。mcp-fuse を
再起動してください。その後、人ごとに渡されたリンクでサーバーを 1 回接続し、それ以降はそのサーバーのツールが一覧に
現れます（[authorization](authorization.ja.md)）。トークンを要求しないサーバーなら（試している間）、`"auth": "none"`
を設定してください。

## 追加する前にサーバーを確認する

```sh
# 1. トークンがなければ拒否され、メタデータの場所が示されます。
curl -si -X POST https://orders.example.com/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"check","version":"0"}}}' \
  | head -20
#    期待: HTTP/1.1 401 と、resource_metadata="…" を含む WWW-Authenticate ヘッダー

# 2. メタデータがまさにこのサーバーを resource として名乗っています。
curl -s "<the resource_metadata URL>" | jq '{resource, authorization_servers}'

# 3. 認可サーバーが mcp-fuse に必要なものに対応しています。
curl -s https://auth.example.com/.well-known/oauth-authorization-server \
  | jq '{issuer, code_challenge_methods_supported, authorization_endpoint, token_endpoint, registration_endpoint}'
#    期待: 手順 2 に挙がった issuer、対応方式に "S256"
```

3 つとも通れば、mcp-fuse はサーバーの認可サーバーを見つけ、そのサーバー向けのトークンを要求できます。
