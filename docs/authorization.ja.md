# Authorization

[English](authorization.md) | [한국어](authorization.ko.md) | **日本語**

mcp-fuse は 2 つの役割を同時に担い、その 2 つを混ぜません。

- **エージェント**から見ると、標準的な MCP リソースサーバーです。エージェントは mcp-fuse が指定した認可サーバーで
  ログインし、そこで得たトークンは **mcp-fuse 向け**のものです。
- 各 **backend** から見ると、標準的な MCP クライアントです。人ごと、backend ごとに、**その backend 自身の認可
  サーバーがその人のために発行したトークン**を保持します。

エージェントが提示したトークンは確認にだけ使い、他には使いません。backend へ送ることもありません。
[MCP 認可仕様](https://modelcontextprotocol.io/specification/latest/basic/authorization)は、サーバーが自分宛てでない
トークンを受け入れたり他へ渡したりすること（「トークンパススルー」）を禁じています。mcp-fuse はこのようにしてそれを
避けており、そのため backend は標準的な MCP サーバーであれば十分です。

## 設定すること

### 1. エージェントがログインする認可サーバー（`authorization_servers`）

この認可サーバーは次のことを行う必要があります。

- audience が mcp-fuse の `resource` であるアクセストークンを発行します（エージェントはリソースインジケーター、
  [RFC 8707](https://www.rfc-editor.org/rfc/rfc8707)でこれを要求します）。
- `auth` に指定されたクライアントに対して introspection エンドポイント（[RFC 7662](https://www.rfc-editor.org/rfc/rfc7662)）
  を提供し、`active`、`sub`、`aud`、必要に応じて `iss` と `exp` を返します。
- そのクライアントに対して PKCE（S256）付きの authorization code に対応します。リダイレクト URI は
  `<resource origin><resource path>/connect/identify` で、人が backend を接続するときに本人であることを証明する
  ために使われます。

mcp-fuse 内部での本人の ID は、introspection で得た `iss` と `sub` のハッシュです。

### 2. 各 backend の認可サーバー

その認可サーバーに、mcp-fuse が次の条件の OAuth クライアントとして登録されている必要があります。

- PKCE（S256）付きの authorization code grant と、リフレッシュトークン。
- リダイレクト URI `<resource origin><resource path>/connect/callback`。
- 発行するトークンの `resource`（audience）として backend 自身のアドレスを受け付けること。

クライアントを手動で登録して backend の下に `client_id`（クライアントが confidential なら `client_secret_file` も）を
書くか、サーバーに[動的クライアント登録](https://www.rfc-editor.org/rfc/rfc7591)を受け付けさせれば、mcp-fuse が自分で
1 回登録します。confidential クライアントのシークレットは、サーバーのメタデータが受け付けるとする方式で送ります
（既定は `client_secret_basic`、サーバーが `client_secret_post` しか提供しない場合はそちら）。

mcp-fuse は、[client ID metadata document](https://modelcontextprotocol.io/specification/latest/basic/authorization/client-registration#client-id-metadata-documents)
で自分を知らせることは**しません**（HTTPS の URL を `client_id` として使い、認可サーバーがそれを取得する方式です）。
MCP 認可仕様はこの方式をクライアントが自分を知らせる推奨の方法とし、それに対応しないサーバーのために動的登録を
代替手段として残しています。認可サーバーは `client_id_metadata_document_supported` でドキュメント方式への対応を
知らせます。この方式だけを提供し `registration_endpoint` がない認可サーバーには、クライアントを手動で登録する必要が
あります。設定された `client_id` がない場合、mcp-fuse はそのサーバーがクライアント登録を提供していないと報告します。

mcp-fuse は、仕様がクライアントに定める方法でサーバーを見つけます。backend の保護リソースメタデータ
（`WWW-Authenticate` チャレンジ、または well-known アドレス）を見て、続いて認可サーバーメタデータ
（[RFC 8414](https://www.rfc-editor.org/rfc/rfc8414)）を見ます。PKCE S256 を示さないサーバー、メタデータが別の
issuer を名乗るサーバー、保護リソースメタデータが別の resource を名乗るサーバーは拒否します。

backend 側で変わることはありません。他の MCP サーバーと同じように、自分のトークンを自分の audience で検証するだけです。

## backend を接続する

backend は人ごとに 1 回接続します。それまではその人の一覧になく、`initialize` の instructions にその backend の
リンクが含まれます。

1. 人がリンクをブラウザーで開きます。リンクには、人と backend を指定した、署名付きで 10 分間有効な **ticket** が
   含まれます。ticket はリンクが誰のために作られたかを示すだけで、誰が開くかは証明しません。
2. **本人確認。** 人が上の 1 の認可サーバーでサインインします。このサインインで得たトークンは、誰がサインインした
   かを知るために 1 回だけ introspect して破棄します。その人が ticket の指定する人である場合にだけ、フローが
   続きます。他の人に渡したリンクでは何も接続されません。
3. **backend。** 人は backend の認可サーバー（authorization code、PKCE、`resource` は backend）へ送られ、そこで
   サインインします。
4. 認可サーバーが人を `.../connect/callback` へ戻すと、mcp-fuse がコードを交換し、トークンをその人とその backend
   の下に暗号化して保管します。

リンクを開くときに設定される cookie が 2〜4 をそのブラウザーに結びつけ、`state` の値は 1 回しか使えません。そのため
backend のサインイン用アドレスを他の人に渡しても、その人は完了できません。cookie がなければ callback は拒否されます。
両方の対策が必要な理由は [security](security.ja.md) を参照してください。

## 更新と失効

アクセストークンは、期限が近づいたとき、または backend が `401` を返したときに更新します。認可サーバーがリフレッシュ
トークンを拒否した場合（`invalid_grant`）、または新しいトークンでも backend が拒否し続ける場合は、grant を破棄します。
するとその backend はその人の一覧から消え、リンクが `initialize` に再び現れます。
