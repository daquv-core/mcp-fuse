# Protocol

[English](protocol.md) | [한국어](protocol.ko.md) | **日本語**

mcp-fuse は Streamable HTTP 上で MCP を話し、[MCP 仕様](https://modelcontextprotocol.io/specification/latest)に従います。

## 扱うもの

| エージェントが送ると | mcp-fuse の動作 |
|---|---|
| `initialize` | その人が接続したすべての backend とセッションを開きます。そのうち最も古いプロトコルバージョン、capability の和集合、各 backend の instructions を backend 名の下に順に並べて返します。その人が接続していない backend もそこに説明し、接続するためのリンクを付けます |
| `tools/list`、`prompts/list`、`resources/list` | 接続済みのすべての backend の一覧を**その人として**取得し（ページ分割をたどります）、結果を統合します。応答しない backend や、そのメソッドを持たない backend は何も追加しません |
| `tools/call`、`prompts/get`、`resources/read` | その名前を一覧に載せた backend へリクエストをそのまま送り、応答もそのままストリームで返します |
| `notifications/cancelled` などの通知 | その人のセッションがある、接続済みのすべての backend に渡します |
| `notifications/initialized` | 吸収します。mcp-fuse は backend と別にハンドシェイクを行います |
| `ping` | ローカルで返します |
| それ以外 | その人が接続した最初の backend に渡します |

名前は変更しません。複数の backend が同じ名前を提供する場合は、`backends` の中で先にあるほうが保持します。

## セッション

セッションは mcp-fuse と各 backend の間にあります。エージェントにはセッションを渡さず、エージェントが送る
`Mcp-Session-Id` は無視します。mcp-fuse は最初に必要になったとき（またはエージェントが `initialize` を呼んだとき）に
セッションを開き、backend が合意したプロトコルリビジョンを記憶して `MCP-Protocol-Version` として送り返します。
また、backend がもう知らないセッションに `404` で答えた場合は、新しいセッションを開いてリクエストを 1 回だけ
繰り返します。

## リビジョン

- **対応:** 2025-11-25 までの MCP リビジョン。`initialize` ハンドシェイク、任意の `Mcp-Session-Id`、JSON またはイベント
  ストリームの応答。バッチは 2025-06-18 と同様に拒否します。
- **非対応:** ハンドシェイクとプロトコルレベルのセッションを廃止した 2026-07-28 リビジョン。
  `MCP-Protocol-Version: 2026-07-28` 以降を含むリクエストには、本文がプレーンテキストの `400` を返します。両方の
  リビジョンに対応するクライアントは、これを `initialize` へ戻る合図として受け取ります。
- **提供しない:** 常時接続の `GET` イベントストリーム（MCP パスへの `GET` は `405`）、セッションの終了（`DELETE` は
  `405`）、サーバーからクライアントへのリクエスト（sampling、elicitation）、リソーステンプレート。elicitation を提供
  しないため、backend を接続するリンクは `initialize` の instructions に含めて渡します。

## エラー

mcp-fuse は、処理できないリクエストに JSON-RPC エラーを返し、HTTP ステータスがどちら側の失敗かを示します。

| 状況 | ステータス | 備考 |
|---|---|---|
| トークンがない、または mcp-fuse に対して有効でない | `401` | `WWW-Authenticate: Bearer resource_metadata="…"` がこのサーバー自身のメタデータを指します |
| 認可サーバーに問い合わせられない | `503` | `401` ではありません。`401` ではクライアントが再ログインに向かってしまいます |
| 接続済みのどの backend も提供しない名前 | `200` | JSON-RPC エラー `-32602` |
| その人が接続していない backend を直接呼び出した | `200` | JSON-RPC エラー `-32004` |
| 応答すべきすべての backend が失敗した | `502` | JSON-RPC エラー `-32002` |
| 対応より新しいプロトコルリビジョン | `400` | プレーンテキスト |
