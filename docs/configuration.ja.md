# Configuration

[English](configuration.md) | [한국어](configuration.ko.md) | **日本語**

mcp-fuse は `-config` で指定した JSON ファイルを 1 つ読みます（既定は `config.json`）。変更は再起動で反映されます。
未知のキーはエラーになるため、綴りを間違えたオプションが黙って既定値に戻ることはありません。プロセスは 0 以外の
コードで終了し、何が間違っているかを伝えます。

完全な例は [`examples/config.json`](../examples/config.json) です。

## キー

| キー | 必須 | 意味 |
|---|---|---|
| `listen` | いいえ | バインドするアドレス。既定は `127.0.0.1:8080`。loopback にバインドし、前段にリバースプロキシを置きます（[deployment](deployment.ja.md)）。 |
| `resource` | はい | エージェントが接続する公開 URL。例: `https://mcp.example.com/mcp`。MCP はこのパスで提供され、人が backend を接続するページもその下にあります。呼び出し元のトークンが持つべき audience であり、保護リソースメタデータの `resource` です。リクエストの `Host` が代わりに使われることはありません。 |
| `authorization_servers` | はい | エージェントがログインできる認可サーバー。保護リソースメタデータ（[RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)）で知らせます。最初の 1 つは、人が backend を接続するときにサインインする場所でもあります（[authorization](authorization.ja.md)）。 |
| `auth` | 下記参照 | 呼び出し元のトークンをどう確認するか。 |
| `auth.introspection_url` | `auth` があれば必須 | [RFC 7662](https://www.rfc-editor.org/rfc/rfc7662) のエンドポイント。トークンが active で、subject を持ち、audience に `resource` が含まれていれば受け付けます。 |
| `auth.client_id` | `auth` があれば必須 | その認可サーバーでの mcp-fuse の OAuth クライアント。introspection エンドポイントへの認証と、backend 接続時の人のサインインに使います。 |
| `auth.client_secret_file` | いいえ | クライアントのシークレットを入れたファイル。なければクライアントは public で、id だけを送ります。 |
| `state_file` | `oauth` の backend があるとき | grant を暗号化して保管する場所。失うと全員が再接続することになりますが、これに依存するものは他にありません。 |
| `secret_file` | `oauth` の backend があるとき | 32 バイト以上。`state_file` を暗号化する鍵と接続リンクに署名する鍵が、それぞれの用途のためにここから導出されます。 |
| `backends` | はい | 統合する MCP サーバー。優先順位順です（下記参照）。 |
| `backends[].name` | はい | 一意であること。ログ、instructions、接続リンクに現れるため、`/`、`?`、`#`、空白を含めてはいけません。 |
| `backends[].url` | はい | backend の MCP エンドポイント（http または https）。 |
| `backends[].auth` | いいえ | `"oauth"`（既定）: 人ごとに backend を接続し、mcp-fuse はその人自身のトークンを使います。`"none"`: backend はトークンを要求しません。 |
| `backends[].client_id` | いいえ | backend の認可サーバーに mcp-fuse 用として事前登録したクライアント。なければ mcp-fuse が自分で登録（[RFC 7591](https://www.rfc-editor.org/rfc/rfc7591)）し、結果を記憶します。 |
| `backends[].client_secret_file` | いいえ | そのクライアントが confidential の場合の、シークレットを入れたファイル。`client_id` が必要です。 |
| `list_timeout_seconds` | いいえ | `initialize` と一覧が backend を待つ時間。既定は 15。 |

`auth` を省略できるのは、すべての backend が `"auth": "none"` のときだけです。保管する人ごとのトークンがなければ、
識別する相手もいないからです。その場合、mcp-fuse は起動時に、呼び出し元が認証されないという警告をログに出します。

## 順序が重要です

`backends` は優先順位のリストです。2 つの backend が同じ名前のツール、プロンプト、リソースを提供する場合、先に書かれた
ほうが名前を保持し、もう一方は統合した一覧から外れ、衝突はログに記録されます。特定の backend に属さないリクエスト
（mcp-fuse が知らないメソッド）は、その人が接続した最初の backend に送られます。

## シークレットはファイルです

設定ファイルにシークレットは入っておらず、ファイルがその場所を指します。各シークレットファイルはサービスユーザーに
だけ与えてください（`0600`）。state ファイルは `0600` で作られます。

## 登録するリダイレクト URI

mcp-fuse は `resource` から 2 つのリダイレクト URI を作ります。`https://mcp.example.com/mcp` の場合:

| 登録する場所 | リダイレクト URI |
|---|---|
| 呼び出し元がログインする認可サーバーの `auth.client_id` クライアント | `https://mcp.example.com/mcp/connect/identify` |
| 各 backend の認可サーバーにある mcp-fuse クライアント（自分で登録する場合を除く） | `https://mcp.example.com/mcp/connect/callback` |

各登録で何を許可する必要があるかは [authorization](authorization.ja.md) を参照してください。

## 上限

コードに固定されている値です。リクエスト本文は最大 16 MiB、全体を読み込む backend の応答（一覧、`initialize`）は
最大 32 MiB、backend が一覧をページ分割する場合は最大 20 ページ、接続リンクの有効期間は 10 分です。
