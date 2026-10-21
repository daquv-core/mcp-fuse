# mcp-fuse

[English](README.md) | [한국어](README.ko.md) | **日本語**

複数の MCP サーバーを 1 つの MCP サーバーのように見せるリバースプロキシです。

> **ステータス: 最初の実装で、まだリリースはありません。** MCP 仕様のリビジョン 2025-11-25 までに対応しています
> （[プロトコル](#プロトコル)を参照）。
>
> このドキュメントは [`README.md`](README.md)（英語）に従います。内容が異なる場合は英語版が正です。

## できること

AI エージェント（ChatGPT、Gemini、Claude、社内の AI アシスタントなど）は **1 つの** MCP エンドポイントにだけ接続します。
`mcp-fuse` は背後にある backend が提供するものをまとめて見せ、呼び出しはそれを持つ backend に転送します。

エージェントから見た `mcp-fuse` は、ごく普通の MCP リソースサーバーです。エージェントは `mcp-fuse` が示す認可
サーバーでログインし、受け取るトークンは **`mcp-fuse` 宛て**です。各 backend から見た `mcp-fuse` は、ごく普通の
MCP クライアントです。**その backend 自身の認可サーバーが、その人のために発行したトークン**を別に保管して使います。
エージェントが提示したトークンはどの backend にも送らないので、backend は標準的な MCP サーバーであれば十分です。

| エージェントが送るもの | mcp-fuse の動作 |
|---|---|
| `initialize` | その人が接続済みのすべての backend とセッションを開き、その中で最も古いプロトコルバージョン、capability の和集合、backend 名を付けた instructions を順に連結して返します。まだ接続していない backend も、接続用リンクとともにここに記載します |
| `tools/list`、`prompts/list`、`resources/list` | 接続済みのすべての backend の一覧を **その人の ID で** 取得して統合します。応答しない backend は除かれます |
| `tools/call`、`prompts/get`、`resources/read` | その名前を一覧に載せた backend へリクエストを **そのまま** 送り、応答もそのままストリーミングで返します |
| `notifications/cancelled` などの通知 | セッションを持つ接続済みのすべての backend に渡します |
| それ以外 | その人が接続した最初の backend に渡します |

- 名前は変更しません。2 つの backend が同じ名前を提供する場合、設定で先に書かれた backend がその名前を持ち、
  衝突はログに記録されます。
- 引数と結果はバイト単位でそのまま通過します。
- 誰に何を見せるかは各 backend が決めます。`mcp-fuse` はフィルターを追加しません。
- backend が 1 つ停止しても、他のツールは残ります。
- セッションは `mcp-fuse` と各 backend の間のものです。エージェントにはセッションを渡さず、backend がセッションを
  忘れた場合は `mcp-fuse` が開き直してリクエストを再送します。

### backend を接続する

backend は人ごとに 1 回接続します。

1. エージェントが `initialize` を呼びます。その人がまだ接続していない backend ごとに、instructions にリンクが
   含まれます。
2. その人がリンクをブラウザーで開きます。リンクはその backend の認可サーバーにつながります（backend の保護
   リソースメタデータ、RFC 9728 で見つけます。PKCE 付きの authorization code で、resource indicator、RFC 8707 に
   より backend に紐づけます）。
3. そこでログインすると `mcp-fuse` に戻り、`mcp-fuse` がその人とその backend のトークンを暗号化して保管します。
   エージェントが会話を始め直すと、その backend のツールが見えます。

認可サーバーが refresh token を受け付けなくなると、その backend はその人の一覧から外れ、リンクが instructions に
再び現れます。

## しないこと

以下は対象外であり、今後も変わりません。必要な場合は backend 側で対応します。

- トークンを他へ渡すこと。エージェントが `mcp-fuse` に提示したトークンは確認にだけ使い、他には使いません。
- ポリシー、マスキング、結果の加工、キャッシュ、リトライ、LLM。
- ツールが何を意味するかの理解。
- リクエスト・レスポンス本文やトークンのログ出力。ログに残るのはツール名、backend、遅延、ステータスだけです。

## 実行

```sh
./build.sh                                   # gofmt + vet + 静的バイナリ: ./mcp-fuse
cp config.example.json config.json           # 編集する
./mcp-fuse -config config.json
```

`./mcp-fuse -version` でバージョンを表示します。Go 標準ライブラリ以外の依存はありません。grant をローカルファイルに
保持するため、**インスタンスは 1 つだけ**実行してください。

### 設定

JSON ファイル 1 つで、変更は再起動で反映されます。未知のキーはエラーになります。

```json
{
  "listen": "127.0.0.1:8080",
  "resource": "https://mcp.example.com/mcp",
  "authorization_servers": ["https://auth.example.com"],
  "auth": {
    "introspection_url": "https://auth.example.com/oauth/introspect",
    "client_id": "mcp-fuse",
    "client_secret_file": "/etc/mcp-fuse/introspection-secret"
  },
  "state_file": "/var/lib/mcp-fuse/state.bin",
  "secret_file": "/etc/mcp-fuse/secret",
  "backends": [
    {"name": "quvi", "url": "https://quvi.internal/v3/mcp/nl2sql"},
    {"name": "exec", "url": "https://exec-mcp.internal/mcp",
     "client_id": "mcp-fuse", "client_secret_file": "/etc/mcp-fuse/exec-client-secret"}
  ],
  "list_timeout_seconds": 15
}
```

| キー | |
|---|---|
| `listen` | バインドするアドレス。既定値は `127.0.0.1:8080`。 |
| `resource` | エージェントが接続する公開 URL。MCP はこの URL のパスで提供されます。呼び出し元のトークンが持つべき audience です。 |
| `authorization_servers` | エージェントがログインする先。どれにするかはデプロイ時の判断で、mcp-fuse は通知するだけです。 |
| `auth.introspection_url`、`auth.client_id`、`auth.client_secret_file` | 呼び出し元のトークンを確認する方法（RFC 7662）。トークンは有効で、subject を持ち、`resource` を audience に含む必要があります。すべての backend が `"auth": "none"` の場合に限り `auth` を省略できます。 |
| `state_file` | grant を暗号化して保管するファイル。失うと全員が接続し直すことになります。 |
| `secret_file` | 32 バイト以上。`state_file` と接続リンクの鍵はここから導出されます。 |
| `backends[].name` | 重複不可。ログ、instructions、接続リンクに表示されます。`/`、`?`、`#`、空白は使えません。 |
| `backends[].url` | backend の MCP エンドポイント。 |
| `backends[].auth` | `"oauth"`（既定値）: 人ごとに接続します。`"none"`: backend はトークンを要求しません。 |
| `backends[].client_id`、`client_secret_file` | backend の認可サーバーに mcp-fuse 用として事前登録した client。`client_id` がなければ mcp-fuse が自分で登録し（RFC 7591）、結果を記憶します。 |
| `list_timeout_seconds` | `initialize` と一覧の取得が backend を待つ時間。既定値は 15。 |

backend の認可サーバーに登録する redirect URI は `<resource の origin><resource のパス>/connect/callback` で、上の例では
`https://mcp.example.com/mcp/connect/callback` です。

### エンドポイント

| | |
|---|---|
| `POST <resource のパス>` | MCP。`GET` と `DELETE` には 405 を返します。 |
| `GET <resource のパス>/connect/<backend>?t=…` | backend の接続を開始します。リンクは instructions で渡され、10 分間有効です。 |
| `GET <resource のパス>/connect/callback` | backend の認可サーバーが人を戻す先。 |
| `GET /.well-known/oauth-protected-resource[/<resource のパス>]` | 保護リソースメタデータ（[RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)）: 設定の `resource` と `authorization_servers`。 |
| `GET /healthz` | `{"status":"ok","version":"…"}`。backend の疎通は確認しません。 |

### backend に送られるもの

標準的な MCP クライアントが送るものだけです: `Content-Type`、`Accept`、`User-Agent`、`MCP-Protocol-Version`、自身の
`Mcp-Session-Id`、自身の `Authorization`、そしてログを突き合わせるための `X-Request-Id`。呼び出し元が付けたものは
一切コピーせず、`X-Forwarded-*` も付けません。

## プロトコル

動作は [MCP 仕様](https://modelcontextprotocol.io/specification/latest)に従います。

- **対応:** Streamable HTTP 上のリビジョン 2025-11-25 まで: `initialize` ハンドシェイク、任意の `Mcp-Session-Id`、
  JSON またはイベントストリームでの応答。2025-06-18 と同様にバッチは拒否します。
- **未対応:** ハンドシェイクとセッションを廃止したリビジョン 2026-07-28。`MCP-Protocol-Version: 2026-07-28` 以降を
  付けたリクエストには `400` を返します。両方のリビジョンに対応するクライアントはこれを `initialize` に
  切り替える合図として扱います。
- **提供しないもの:** 常時接続の `GET` イベントストリーム、サーバーからクライアントへのリクエスト（sampling、
  elicitation）。そのため接続リンクは URL 方式の elicitation ではなく instructions で渡します。resource template は
  統合しません。

## デプロイ形態

静的バイナリまたはコンテナ 1 つ、設定ファイル 1 つ、状態ファイル 1 つ、インスタンス 1 つ。backend と同じネットワーク内で
動かし、手前の web 層とロードバランサーは TLS 終端と 1 つのパスの転送だけを行います。運用上の契約の全文は
[`docs/cp_share/contract.md`](docs/cp_share/contract.md)（英語）です。

## 翻訳

[`README.md`](README.md)（英語）が原本で、このファイルと [`README.ko.md`](README.ko.md) がそれに従います。原本を
変更するときは 3 つのファイルを一緒に直します。
