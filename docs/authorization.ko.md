# Authorization

[English](authorization.md) | **한국어** | [日本語](authorization.ja.md)

mcp-fuse는 두 가지 역할을 동시에 하며, 둘을 서로 섞지 않습니다.

- **에이전트**에게는 표준 MCP resource server입니다. 에이전트는 mcp-fuse가 지정한 인가 서버에서 로그인하고, 그렇게
  받은 token은 **mcp-fuse를 위한 것**입니다.
- 각 **backend**에게는 표준 MCP client입니다. 사람별, backend별로 **그 backend의 인가 서버가 그 사람을 위해
  발급한 token**을 보관합니다.

에이전트가 내민 token은 확인한 뒤 다른 곳에는 쓰지 않으며, backend로 보내지 않습니다.
[MCP 인가 규격](https://modelcontextprotocol.io/specification/latest/basic/authorization)은 서버가 자신을 위해
발급되지 않은 token을 받거나 다른 곳으로 넘기는 일("token passthrough")을 금지합니다. mcp-fuse는 이렇게 그것을
피하며, 그래서 backend는 표준 MCP 서버이기만 하면 됩니다.

## 무엇을 설정하나

### 1. 에이전트가 로그인하는 인가 서버(`authorization_servers`)

이 서버는 다음을 해야 합니다.

- audience가 mcp-fuse의 `resource`인 access token을 발급합니다(에이전트는 resource indicator,
  [RFC 8707](https://www.rfc-editor.org/rfc/rfc8707)로 이를 요청합니다).
- `auth`에 적힌 client에게 introspection 엔드포인트([RFC 7662](https://www.rfc-editor.org/rfc/rfc7662))를
  제공하고, `active`, `sub`, `aud`와 선택적으로 `iss`, `exp`를 돌려줍니다.
- 그 client에 대해 PKCE(S256)를 쓰는 authorization code를 지원하며, redirect URI는
  `<resource origin><resource path>/connect/identify`입니다. 사람이 backend를 연결할 때 누구인지 증명하는 데 쓰입니다.

mcp-fuse 안에서 그 사람의 신원은 introspection으로 얻은 `iss`와 `sub`의 해시입니다.

### 2. 각 backend의 인가 서버

그 서버에 mcp-fuse가 다음 조건의 OAuth client로 알려져 있어야 합니다.

- PKCE(S256)를 쓰는 authorization code grant와 refresh token;
- redirect URI `<resource origin><resource path>/connect/callback`;
- 발급하는 token의 `resource`(audience)로 backend 자신의 주소를 받아들일 것.

client를 직접 등록하고 backend 아래에 `client_id`(client가 confidential이면 `client_secret_file`도)를 적거나,
서버가 [동적 client 등록](https://www.rfc-editor.org/rfc/rfc7591)을 받아 주게 하면 mcp-fuse가 스스로 한 번
등록합니다. confidential client의 secret은 서버의 메타데이터가 받겠다고 하는 방식으로 보냅니다(기본은
`client_secret_basic`, 서버가 `client_secret_post`만 제공하면 그쪽).

mcp-fuse는 [client ID metadata document](https://modelcontextprotocol.io/specification/latest/basic/authorization/client-registration#client-id-metadata-documents)로
자신을 알리지 **않습니다**(HTTPS URL을 `client_id`로 쓰고 인가 서버가 그것을 가져가는 방식입니다). MCP 인가 규격은 이
방식을 client가 자신을 알리는 선호 방법으로 두고, 이를 지원하지 않는 서버를 위해 동적 등록을 대체 수단으로
남겨 둡니다. 인가 서버는 `client_id_metadata_document_supported`로 문서 방식을 지원한다고 알립니다. 이 방식만
제공하고 `registration_endpoint`가 없는 인가 서버에는 client를 직접 등록해야 합니다. 설정된 `client_id`가 없으면
mcp-fuse는 그 서버가 client 등록을 제공하지 않는다고 알립니다.

mcp-fuse는 규격이 client에게 정한 방식으로 서버를 찾습니다. backend의 보호 자원 메타데이터(`WWW-Authenticate`
challenge, 또는 well-known 주소)를 보고, 이어서 인가 서버 메타데이터([RFC 8414](https://www.rfc-editor.org/rfc/rfc8414))를
봅니다. PKCE S256을 알리지 않는 서버, 메타데이터가 다른 issuer를 말하는 서버, 보호 자원 메타데이터가 다른
resource를 말하는 서버는 거부합니다.

backend 쪽에서 바뀌는 것은 없습니다. 여느 MCP 서버처럼 자기 token을 자기 audience로 검증하면 됩니다.

## backend 연결하기

backend는 사람마다 한 번 연결합니다. 그 전에는 그 사람의 목록에 없고, `initialize`의 instructions에 그
backend의 링크가 들어 있습니다.

1. 사람이 링크를 브라우저에서 엽니다. 링크에는 사람과 backend를 지정한, 서명된 10분짜리 **ticket**이 들어
   있습니다. ticket은 링크가 누구를 위해 만들어졌는지 말할 뿐, 누가 여는지는 증명하지 않습니다.
2. **신원 확인.** 사람이 위 1단계의 인가 서버에서 로그인합니다. 이 로그인이 만든 token은 누가 로그인했는지
   알기 위해 한 번 introspect한 뒤 버립니다. 그 사람이 ticket이 지정한 사람일 때만 흐름이 이어집니다. 다른
   사람에게 건넨 링크로는 아무것도 연결되지 않습니다.
3. **backend.** 사람은 backend의 인가 서버로 보내져(authorization code, PKCE, `resource`는 backend) 거기서
   로그인합니다.
4. 인가 서버가 사람을 `.../connect/callback`으로 돌려보내면, mcp-fuse가 code를 교환하고 token을 그 사람과 그
   backend 아래에 암호화해 보관합니다.

링크를 열 때 심는 cookie가 2~4단계를 그 브라우저에 묶고, `state` 값은 한 번만 쓸 수 있습니다. 그래서 backend의
로그인 주소를 다른 사람에게 건네도 그 사람은 끝낼 수 없습니다. cookie가 없으면 callback이 거부됩니다. 두 장치가
모두 필요한 이유는 [security](security.ko.md)를 보세요.

## 갱신과 폐기

access token은 만료가 가까워지거나 backend가 `401`로 답하면 갱신합니다. 인가 서버가 refresh token을
거부하거나(`invalid_grant`), 새 token도 backend가 계속 거부하면 grant를 버립니다. 그러면 그 backend는 그 사람의
목록에서 사라지고, 링크가 `initialize`에 다시 나타납니다.
