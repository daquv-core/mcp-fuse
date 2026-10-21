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

| エージェントが送るもの | mcp-fuse の動作 |
|---|---|
| `initialize` | すべての backend に問い合わせ、その中で最も古いプロトコルバージョン、capability の和集合、backend 名を付けた instructions を順に連結して返します |
| `tools/list`、`prompts/list`、`resources/list` | **呼び出し元の ID で** すべての backend の一覧を取得して統合します。応答しない backend は除かれます |
| `tools/call`、`prompts/get`、`resources/read` | その名前を一覧に載せた backend へリクエストを **そのまま** 送り、応答もそのままストリーミングで返します |
| `notifications/cancelled` などの通知 | すべての backend に渡します（`notifications/initialized` は stateful な backend のみ） |
| それ以外 | stateful な backend（なければ最初の backend）に渡します |

- 名前は変更しません。2 つの backend が同じ名前を提供する場合、設定で先に書かれた backend がその名前を持ち、
  衝突はログに記録されます。
- 引数と結果はバイト単位でそのまま通過します。
- 誰に何を見せるかは各 backend が決めます。`mcp-fuse` はフィルターを追加しません。
- backend が 1 つ停止しても、他のツールは残ります。
- stateful な backend が呼び出し元のセッションを知らない場合は `404` を返します。クライアントが不完全な一覧を
  知らずに受け取るのではなく、`initialize` をやり直せるようにするためです。

## しないこと

以下は対象外であり、今後も変わりません。必要な場合は backend 側で対応します。

- 認証、トークンの発行・検証・保管。呼び出し元の `Authorization` ヘッダーはそのまま転送します。
- ポリシー、マスキング、結果の加工、キャッシュ、リトライ、LLM。
- ツールが何を意味するかの理解。
- リクエスト・レスポンス本文のログ出力。ログに残るのはツール名、backend、遅延、ステータスだけです。

## 実行

```sh
./build.sh                                   # gofmt + vet + 静的バイナリ: ./mcp-fuse
cp config.example.json config.json           # 編集する
./mcp-fuse -config config.json
```

`./mcp-fuse -version` でバージョンを表示します。Go 標準ライブラリ以外の依存はありません。

### 設定

JSON ファイル 1 つで、変更は再起動で反映されます。未知のキーはエラーになります。

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

| キー | |
|---|---|
| `listen` | バインドするアドレス。既定値は `127.0.0.1:8080`。 |
| `resource` | エージェントが接続する公開 URL。MCP はこの URL のパスで提供されます。ディスカバリ文書と 401 のチャレンジで使われます。 |
| `authorization_servers` | エージェントがログインする先。どれにするかはデプロイ時の判断で、mcp-fuse は通知するだけです。 |
| `backends[].name` | 重複不可。ログと instructions に表示されます。 |
| `backends[].url` | backend の MCP エンドポイント。 |
| `backends[].stateful` | backend が MCP セッション（`Mcp-Session-Id`）を保持します。最大 1 つ。セッション ID はその backend にだけ送られます。 |
| `list_timeout_seconds` | `initialize` と一覧の取得が backend を待つ時間。既定値は 15。 |

### エンドポイント

| | |
|---|---|
| `POST <resource のパス>` | MCP。`GET` と `DELETE` には 405 を返します。 |
| `GET /.well-known/oauth-protected-resource[/<resource のパス>]` | 保護リソースメタデータ（[RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)）: 設定の `resource` と `authorization_servers`。 |
| `GET /healthz` | `{"status":"ok","version":"…"}`。backend の疎通は確認しません。 |

backend の `401` は、このサーバーのメタデータ文書を指す `WWW-Authenticate` を付けた `401` に置き換えられます。
エージェントが実際に接続したアドレスを基準に認可を受けられるようにするためです。

## プロトコル

動作は [MCP 仕様](https://modelcontextprotocol.io/specification/latest)に従います。

- **対応:** Streamable HTTP 上のリビジョン 2025-11-25 まで: `initialize` ハンドシェイク、任意の `Mcp-Session-Id`、
  JSON またはイベントストリームでの応答。2025-06-18 と同様にバッチは拒否します。
- **未対応:** ハンドシェイクとセッションを廃止したリビジョン 2026-07-28。`MCP-Protocol-Version: 2026-07-28` 以降を
  付けたリクエストには `400` を返します。両方のリビジョンに対応するクライアントはこれを `initialize` に
  切り替える合図として扱います。
- **提供しないもの:** 常時接続の `GET` イベントストリーム、サーバーからクライアントへのリクエスト（sampling、
  elicitation）。resource template は統合しません。

## デプロイ形態

静的バイナリまたはコンテナ 1 つ、設定ファイル 1 つ、データベースなし。backend と同じネットワーク内で動かし、
手前の web 層とロードバランサーは TLS 終端と 1 つのパスの転送だけを行います。運用上の契約の全文は
[`docs/cp_share/contract.md`](docs/cp_share/contract.md)（英語）です。

## 翻訳

[`README.md`](README.md)（英語）が原本で、このファイルと [`README.ko.md`](README.ko.md) がそれに従います。原本を
変更するときは 3 つのファイルを一緒に直します。
