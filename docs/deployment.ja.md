# Deployment

[English](deployment.md) | [한국어](deployment.ko.md) | **日本語**

mcp-fuse は静的バイナリ 1 つ（またはコンテナ 1 つ）、JSON ファイル 1 つ、state ファイル 1 つです。データベースは
不要です。**インスタンスは 1 つだけ**実行します。

```
agent ──TLS──> load balancer / reverse proxy ──HTTP──> mcp-fuse (127.0.0.1:8080) ──HTTP(S)──> backends
person's browser ───────────────────────────────────────────────────────────────> backends' authorization servers
```

## リリース

GitHub の各リリース `vX.Y.Z` には、静的バイナリ `mcp-fuse-linux-amd64` と `SHA256SUMS` が含まれます。

```sh
sha256sum -c SHA256SUMS
chmod +x mcp-fuse-linux-amd64 && ./mcp-fuse-linux-amd64 -version
```

リリースは何も再ビルドしません。`main` のすべてのコミットはビルド・テストされ、`ghcr.io/daquv-core/mcp-fuse:<commit>`
として公開されます。バイナリだけを入れたイメージ（`FROM scratch`、実行はできません）です。リリースには、タグが指す
コミットのイメージから取り出したバイナリが入ります。そのため `-version` はタグではなくリリースの元になったコミットを
表示し、リリースノートにそのコミットが書かれています。

## ビルド

```sh
./build.sh          # gofmt check, go vet, then a static binary: ./mcp-fuse
./mcp-fuse -version
```

Go 1.22 以降が必要です。標準ライブラリ以外の依存はありません。`go test -race ./...` でテストを実行します。

コンテナイメージ:

```sh
docker build -t mcp-fuse --build-arg VERSION=$(git describe --tags --always) .
docker run -v "$PWD/config:/etc/mcp-fuse:ro" -v mcp-fuse-state:/var/lib/mcp-fuse \
           -p 127.0.0.1:8080:8080 mcp-fuse
```

イメージは root 以外のユーザー（uid 65532）で動くため、state ボリュームはそのユーザーが書き込める必要があります。
https の backend に必要な CA 証明書を含む最小限のイメージを元にしています。

## ファイル

| ファイル | 内容 | 権限 |
|---|---|---|
| `config.json` | 設定 | サービスユーザーが読めること |
| `secret_file` | ランダムな 32 バイト以上 | サービスユーザーのみ |
| `auth.client_secret_file`、`backends[].client_secret_file` | OAuth クライアントのシークレット | サービスユーザーのみ |
| `state_file` | 暗号化された grant | `0600` で作成。ディレクトリは書き込み可能であること |

シークレットは、たとえば `head -c 48 /dev/urandom | base64 > /etc/mcp-fuse/secret` で作ります。変更すると既存の state
ファイルが読めなくなり、全員が再接続することになります。

[`examples/mcp-fuse.service`](../examples/mcp-fuse.service) は堅牢化した systemd unit です。専用ユーザー
（`useradd --system --no-create-home --shell /usr/sbin/nologin mcp-fuse`）で動き、state ファイルを
`/var/lib/mcp-fuse` に置きます。

## リバースプロキシ

[`examples/nginx.conf`](../examples/nginx.conf) が動作する設定です。何を使うにしても重要なのは次の点です。

- mcp-fuse へ**2 つのパスをルーティング**します。`resource` のパス（人がブラウザーで開く `<path>/connect/...` の
  ページも含みます）と `/.well-known/oauth-protected-resource` です。
- **レスポンスをバッファリングしません。** ツールの応答が server-sent event ストリームのことがあります。
- **長い呼び出しを許可します。** ツール呼び出しが数分かかることがあるので、read timeout をそれに合わせ、前段の
  ロードバランサーがアイドル接続をそれより早く閉じないようにします。
- **ヘッダーをそのまま通します。** `Authorization` と `MCP-Protocol-Version` が届く必要があり、接続ページが使う
  cookie も同様です。`X-Forwarded-*` を設定するのはプロキシと mcp-fuse の間であるここです。mcp-fuse はそれを先へ
  転送しません。
- `POST` を許可します。mcp-fuse は MCP パスへの `GET` と `DELETE` に `405` を返します。
- エージェントに統合されたエンドポイントだけを見せたい場合は、backend を別に公開しません。

## 運用

- **ヘルスチェック:** `GET /healthz` が `{"status":"ok","version":"…"}` を返します。backend は確認しません。
- **ログ**は標準出力の JSON 行です。`msg`、`level`、呼び出しには `method`、`tool`、`backend`、`status`、`ms`、
  `request_id` が入ります。backend の失敗と backend 名の衝突は `WARN` で記録されます。
- **停止:** `SIGTERM` を受けると新しいリクエストの受け付けをやめ、処理中の呼び出しに最大 30 秒を与えます。
- **アップグレード:** バイナリを置き換えて再起動します。grant は残ります（state ファイルにあります）。backend の
  セッションは必要に応じて開き直され、進行中だった接続はやり直す必要があります。
- **バックアップ:** state ファイルとシークレットファイルを一緒に。片方だけでは役に立ちません。
- **backend が 1 つ停止すると**、応答が戻るまでその backend のツールだけが一覧から外れます。

## mcp-fuse を他の場所に登録する

人が backend を接続できるようになるには、2 つの登録が必要です（詳細は [authorization](authorization.ja.md)）。

1. エージェントがログインする認可サーバーに: `auth.client_id` クライアント。リダイレクト URI は
   `<resource>/connect/identify` で、mcp-fuse 向けに発行されたトークンを introspect できること。
2. 各 backend の認可サーバーに: mcp-fuse をクライアントとして。リダイレクト URI は `<resource>/connect/callback`
   です（サーバーが動的登録に対応していれば、自分で登録させても構いません）。
