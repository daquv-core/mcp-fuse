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

#### エージェントがそのサーバーに自分を知らせる方法

エージェントはこの認可サーバーの OAuth クライアントであり、誰かがサインインする前に自分を知らせます。
[MCP 認可仕様](https://modelcontextprotocol.io/specification/latest/basic/authorization/client-registration)はその方法に
順序を定めています。事前に登録されたクライアント、次に **client ID metadata document**、そしてどちらにも対応しない
サーバーのための代替手段として[動的クライアント登録](https://www.rfc-editor.org/rfc/rfc7591)です。

client ID metadata document は、エージェントの提供元が HTTPS の URL に公開する JSON ドキュメントで、その URL が
エージェントの `client_id` です。ドキュメントにはエージェントの名前とリダイレクト URI があります。このような
`client_id` を受け取った認可サーバーはドキュメントを取得し、その中の `client_id` が取得元の URL と同じであることを
確かめ、人をドキュメントに書かれたリダイレクト URI にだけ戻します。これが動的登録より先に来る理由です。

- **接続ごとに登録しません。** エージェントは初めてのサーバーに次々と出会います。動的登録では、インストールごと、
  サーバーごとに新しいクライアントが登録され、サーバーはそのどれを削除してよいか分かりません。ドキュメントは
  エージェントごとに 1 つの ID で、どのサーバーでも同じです。
- **ドメインが ID です。** リダイレクト URI はリクエストの送り手ではなく提供元のドメインにあるドキュメントから
  来るので、管理者はそのドメインでエージェントを許可します。
- **提供元が最新に保ちます。** エージェントのリダイレクト URI や名前の変更はドキュメント 1 か所で行われ、各サーバーは
  手元の写しが期限切れになると読み直します。

認可サーバーに求められるのは、ドキュメントを受け付けるホストへの外向きの HTTPS です。隔離されたネットワークにあって
そのホストに届かないサーバーは、エージェントを事前に登録するか動的登録を使います。ドキュメントを受け付けるサーバーは
メタデータの `client_id_metadata_document_supported` でそれを知らせ、エージェントはその値を見ます。

#### Keycloak の場合

Keycloak は client ID metadata document を受け付けます。
[Keycloak の MCP ガイド](https://www.keycloak.org/securing-apps/mcp-authz-server)に手順とエージェントごとの設定があります。
概要は次のとおりです。

1. **機能を有効にします。** 起動時に `--features=cimd` を指定します。サポートレベルはガイドに書かれています。
   Keycloak が実験的とする機能はどのリリースでも変わりうるので、アップグレードのたびにエージェントのサインインを
   試し直してください。機能を有効にすると、サーバーのメタデータに `client_id_metadata_document_supported` が載ります。
2. **クライアントプロファイル**（*Realm settings* → *Client policies* → *Profiles*）に `client-id-metadata-document`
   エグゼキューターを追加します。*Trusted domains*：受け付けるドキュメントのホストと、ドキュメント内のアドレス
   （リダイレクト URI、ロゴ）のホスト。*Allow http scheme*：オフ。*Only Allow Confidential Client*：オフ。デスクトップの
   エージェントは public クライアントです。*Restrict same domain*：自分のコンピューター（`localhost`）に戻る
   エージェントではオフ、リダイレクト URI が提供元のドメインにあるエージェントではオン。
3. **クライアントポリシー**（*Client policies* → *Policies*）に `client-id-uri` 条件を追加します。scheme は `https`、
   *Trusted domains* は `client_id` URL のホストです。上のプロファイルと関連付けます。`client_id` URL のドメインが同じ
   エージェントは互いのポリシーに当てはまるので、1 つのポリシーと、すべてを満たす設定の 1 つのプロファイルで許可して
   ください。
4. **Keycloak がそのホストに HTTPS で届くようにします。** 外向きの通信がプロキシを通るなら、そのプロキシ経由です。
5. **audience に mcp-fuse の `resource` を入れます。** Keycloak はドキュメントごとにクライアントを作り、レルムの
   デフォルトのクライアントスコープを付けるので、レルムのデフォルトのクライアントスコープに mcp-fuse の `resource` を
   入れる *Audience* マッパーを置けば、これらのエージェントにも適用されます。ドキュメントに `scope` があると、既定では
   basic スコープだけが付きます。そのようなエージェントの audience は `resource` パラメーターから来る必要があり、
   Keycloak は `resource-indicators` 機能でそれを扱います。エージェントが受け取ったトークンをデコードして `aud` を
   読んでください。

Keycloak はドキュメントの URL ごとにクライアントを 1 つだけ持ち、ドキュメントから更新するので、接続ごとにクライアントが
増えることはありません。

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
