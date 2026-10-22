# mcp-fuse

[English](README.md) | [한국어](README.ko.md) | **日本語**

複数の MCP サーバーを 1 つの MCP サーバーのように見せるリバースプロキシです。

AI エージェント（ChatGPT、Gemini、Claude、社内の AI アシスタントなど）は、MCP エンドポイントが 1 つのときに最も
うまく働きます。ところが使わせたいものは、アプリケーション自身のサーバー、データ照会サーバー、ツールサーバーの
ように、複数の MCP サーバーに分かれていることがよくあります。1 つずつ別々に接続すると、どのツールをどこで使うかの
判断がエージェントのプロンプトに任されます。`mcp-fuse` はそれらを 1 つのエンドポイントの背後にまとめ、提供される
ものを統合して見せ、呼び出しはそのツールを持つサーバーに送ります。サーバー側は変更しません。

## できること

- すべての backend の `initialize` と、ツール・プロンプト・リソースの一覧を統合します。名前は変更せず、2 つの
  backend が同じ名前を提供する場合は先に書かれた backend がその名前を持ちます。
- 呼び出しは、そのツールを一覧に載せた backend へ **そのまま** 送ります。引数と結果はバイト単位でそのまま通過します。
- 各 backend の一覧を **要求した人の ID で** 取得するため、誰に何を見せるかは各 backend が決めます。mcp-fuse は
  フィルターを追加せず、backend が 1 つ停止しても他のツールは残ります。
- エージェントから見ると標準的な MCP **リソースサーバー**、各 backend から見ると標準的な MCP **クライアント**です。
  エージェントが提示したトークンは確認にだけ使い、他へは渡しません。各 backend には、その backend 自身の認可
  サーバーがその人のために発行したトークンが渡ります。backend は標準的な MCP サーバーであれば十分です。

### backend を接続する

backend は人ごとに 1 回接続します。

1. エージェントが `initialize` を呼びます。その人がまだ接続していない backend ごとに、instructions にリンクが
   含まれます。
2. その人がリンクをブラウザーで開き、エージェントがログインする認可サーバーでログインします。これが「誰が接続して
   いるか」の証明になり、リンクが作られた本人でなければなりません。
3. 続いてその backend の認可サーバーに移り、そこでログインします。
4. mcp-fuse がその人とその backend のトークンを暗号化して保管します。

フロー全体は、リンクを開いたブラウザーで完了させる必要があります。[authorization](docs/authorization.ja.md)を
参照してください。

## 自分の MCP サーバーを追加する

サーバーを mcp-fuse のために変更する必要はありません。[MCP 仕様](https://modelcontextprotocol.io/specification/latest)に
従って作ってください。Streamable HTTP と、[認可仕様](https://modelcontextprotocol.io/specification/latest/basic/authorization)
（保護リソースメタデータ、PKCE に対応した認可サーバー、サーバー自身を audience とするトークン）です。その上で:

1. サーバーの認可サーバーに mcp-fuse を OAuth クライアントとして知らせます（または動的クライアント登録を有効にします）。
2. `backends` に `{"name": "orders", "url": "https://orders.example.com/mcp"}` を追加して再起動します。

人ごとにリンクから 1 回接続すると、そのサーバーのツールが他のサーバーのツールと統合されて、その人の一覧に現れます。
必要なことと、追加する前にサーバーを確認する方法は [Adding your MCP server](docs/backends.ja.md)にあります。

## しないこと

以下は対象外であり、今後も変わりません。必要な場合は backend 側で対応します。

- トークンを他へ渡すこと。呼び出し元のトークンは確認にだけ使い、他には使いません。
- ポリシー、マスキング、結果の加工、結果のキャッシュ、リトライ、LLM。
- ツールが何を意味するかの理解。
- リクエスト・レスポンス本文やトークンのログ出力。ログに残るのはツール名、backend、遅延、ステータスだけです。

## クイックスタート

```sh
./build.sh                                   # gofmt、vet、静的バイナリ: ./mcp-fuse
cp examples/config.json config.json          # 編集する
./mcp-fuse -config config.json
```

または [release](docs/deployment.ja.md#リリース) のバイナリ（`mcp-fuse-linux-amd64`、`SHA256SUMS` 付き）を使います。

最小の設定:

```json
{
  "resource": "https://mcp.example.com/mcp",
  "authorization_servers": ["https://auth.example.com"],
  "auth": {"introspection_url": "https://auth.example.com/oauth/introspect", "client_id": "mcp-fuse"},
  "state_file": "/var/lib/mcp-fuse/state.bin",
  "secret_file": "/etc/mcp-fuse/secret",
  "backends": [
    {"name": "orders", "url": "https://orders.example.com/mcp"},
    {"name": "analytics", "url": "https://analytics.example.com/mcp"}
  ]
}
```

手前に TLS を終端するリバースプロキシ（[`examples/nginx.conf`](examples/nginx.conf)）を置き、認可サーバーに
mcp-fuse を登録します（[authorization](docs/authorization.ja.md)）。**インスタンスは 1 つだけ**実行してください。
コンテナイメージは `docker build -t mcp-fuse .` で作れます。

## ドキュメント

`docs/` 以下のドキュメントは英語が原本で、それぞれ `*.ko.md` と `*.ja.md` がそれに従います。

- [Adding your MCP server](docs/backends.ja.md) — 何を作り、何を登録し、何を追加するか。
- [Configuration](docs/configuration.ja.md) — すべての設定キー。
- [Authorization](docs/authorization.ja.md) — 何をどこに登録するか、backend をどう接続するか。
- [Deployment](docs/deployment.ja.md) — ビルド、ファイル、リバースプロキシ、運用。
- [Security model](docs/security.ja.md) — 保証すること、利用者に任せること。
- [Protocol](docs/protocol.ja.md) — 扱うもの、対応する MCP リビジョン、エラー。
- [Contributing](CONTRIBUTING.md)、[脆弱性の報告](SECURITY.md)。

## ライセンス

Apache License 2.0 です。[LICENSE](LICENSE) を参照してください。

## 翻訳

[`README.md`](README.md)（英語）が原本で、このファイルと [`README.ko.md`](README.ko.md) がそれに従います。`docs/` 以下の
ドキュメントも英語が原本で、`docs/<name>.ko.md` と `docs/<name>.ja.md` がそれに従います。英語のファイルを変更するときは、
その 2 つの翻訳を一緒に直します。
