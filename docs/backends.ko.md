# Adding your MCP server

[English](backends.md) | **한국어** | [日本語](backends.ja.md)

MCP 서버가 있고(애플리케이션의 동작, 데이터 조회 서버, 도구 서버 등) 그것을 다른 서버들과 함께 엔드포인트 하나
뒤에 나타나게 하고 싶다고 합시다. **mcp-fuse 때문에 서버를 바꿀 필요는 없습니다.** MCP 표준대로 만들고, 서버의
인가 서버에 mcp-fuse를 알리고, mcp-fuse의 설정에 추가하면 됩니다.

## 인가에서 누가 무엇을 하나

MCP 서버의 인가는 두 부분으로 나뉘며, 보통 서로 다른 소프트웨어가 맡습니다.

| 부분 | 하는 일 | 누가 제공하나 |
|---|---|---|
| **인가 서버** | 사람을 로그인시키고, client를 등록하고, PKCE를 쓰는 authorization code 흐름을 실행하고, token을 발급·갱신하며, 메타데이터와 서명 키를 공개합니다. | 직접 만들거나, 만들어진 것을 운영합니다. |
| **Resource server**(당신의 MCP 서버) | 메타데이터를 가리키는 `401`로 답하고, 보호 자원 메타데이터를 공개하며, 받은 모든 token을 확인합니다. 서명, issuer, 만료, 그리고 **자신이 audience인지**입니다. | 언제나 당신의 MCP 서버입니다. |

두 번째 부분은 어떤 인가 서버를 쓰든 인가 서버로 넘어가지 않습니다. 자신의 주소가 audience라는 것은 당신의 서버만
알고, token 속 신원이 무엇을 뜻하는지, 즉 어느 사용자이고 그 사용자가 무엇을 할 수 있는지는 당신의 애플리케이션만
알기 때문입니다. 무엇이 필요한지는 1절에 있습니다.

### 인가 서버는 어디서 오나

이미 표준을 따르는 인가 서버가 없다면 **[Keycloak](https://github.com/keycloak/keycloak)을 운영하기를 권합니다.**
오픈소스(Apache 2.0)이고, 위 표의 인가 서버 일을 모두 하며, MCP를 서비스하는 방법이 문서화되어 있습니다
([Keycloak의 MCP 가이드](https://www.keycloak.org/securing-apps/mcp-authz-server)). 직접 만드는 것도 가능하고 아래에
설명하지만, 제품이 살아 있는 동안 계속 검토하고 패치해야 하는 보안에 민감한 코드입니다.

#### Keycloak을 쓴다면, 할 일

화면과 값을 처음부터 끝까지: [a complete setup with Keycloak](keycloak.ko.md).

1. **운영하고 유지합니다.** 운영용 데이터베이스를 주고 백업을 보호하세요. 기본 설정에서는 realm의 설정과 사용자
   외에 realm의 서명 키도 거기에 있습니다. 업그레이드를 계획하세요. Keycloak은 최신 마이너 릴리스를 지원합니다
   ([릴리스 정책](https://github.com/keycloak/keycloak/blob/main/RELEASES.md)).
2. **기존 로그인을 연결합니다.** OpenID Connect로 기존 로그인을 identity provider로 추가하거나, 기존 로그인이
   OpenID Connect가 아니라면 그 앞에 작은 브리지를 둡니다.
3. **mcp-fuse를 Keycloak의 client로 만듭니다.** PKCE `S256`을 쓰는 authorization code, refresh token, redirect URI
   `<mcp-fuse resource origin><resource path>/connect/callback`. 동적 client 등록은 있지만 기본적으로 익명 요청에는
   닫혀 있습니다. client를 직접 만들고(mcp-fuse에 그 `client_id`를 줍니다, 3절) 또는 initial access token을
   발급하세요.
4. **audience를 서버의 URL로 만듭니다.** Keycloak의 가이드가 방법을 보여 줍니다. *Audience* mapper가 달린 client
   scope를 만들고 그 *Included Custom Audience*를 서버의 URL로 하며, `scope`로 요청되게 합니다(그 scope를 보호 자원
   메타데이터의 `scopes_supported`에 적으세요. challenge에 scope가 없을 때 mcp-fuse가 요청하는 것이 이것입니다).
   Keycloak은 `resource` 파라미터를 직접 처리할 수도 있습니다. 어느 릴리스에 있고 어떻게 켜는지는 Keycloak의
   가이드에 있습니다. 이것을 확인해야 하는 이유는 아래 두 가지 중 첫 번째입니다.
5. **신원 claim을 믿을 수 있게 합니다.** 서버가 이메일 주소로 사람을 식별한다면, identity provider가 검증한 주소만
   받고(identity provider의 *trust email* 설정), 사용자가 자기 주소를 바꾸지 못하게 하세요(realm의 user profile에서
   *email* 속성의 edit 권한). 서버는 그 값대로 행동합니다. 아래 두 가지 중 두 번째를 보세요.
6. **위 표의 resource server 부분을 합니다.** 1절에 정리되어 있습니다.

#### 다른 것을 쓴다면, 할 일

- **이미 가진 인가 서버:** 1절, 2절과 아래 두 가지에 비추어 점검하고 그대로 씁니다. 그다음 resource server 부분을
  합니다.
- **직접 만드는 서버:** PKCE `S256`을 쓰는 authorization 엔드포인트, refresh token과 `resource` 파라미터를 다루는
  token 엔드포인트, client 등록(동적 또는 수동), 동의 단계, 메타데이터 문서, 서명 키의 공개와 교체를 떠안습니다.
  기존 로그인과 연결하고 MCP 인가 규격의 개정을 따라가는 일도 합니다. 그다음 resource server 부분을 합니다.

같은 인가 서버가 호출자의 로그인(mcp-fuse 설정의 `authorization_servers`)과 backend를 함께 맡을 수 있습니다.
mcp-fuse는 그래도 backend마다 별도의 token을 얻습니다([authorization](authorization.ko.md)). 공유해서 얻는 것은,
서버가 브라우저의 로그인 세션을 유지한다면 backend를 연결할 때 다시 로그인하라고 묻지 않는다는 점입니다. 그 밖에
무엇이 보이는지는 서버의 동의 설정에 달려 있습니다.

에이전트가 당신의 Keycloak에서 로그인한다면, 에이전트가 client ID metadata document로 자신을 알리게 하세요
([authorization](authorization.ko.md#keycloak에서)).

### 만들어진 서버가 분명히 알려 주지 않는 두 가지

인가 서버가 무엇을 공개하고 받아들여야 하는지는 1절(메타데이터, PKCE `S256`, `resource` 파라미터, refresh token)과
2절(mcp-fuse를 client로 등록하기)에 있습니다. 그 밖에:

1. **만들어진 서버가 token에 넣는 audience가 당신의 서버라는 보장은 없습니다.** 기본값이 다른 client나 서비스를
   가리킬 수 있습니다. 서버가 발급한 token을 디코드해 `aud`를 읽으세요. 서버가 `resource`를 처리하지 않는다면
   audience는 서버의 설정에서 나와야 합니다.
2. **서버가 사람을 식별하는 데 쓰는 claim은 ID token뿐 아니라 access token에 있어야** 하고, 검증된 값이어야
   합니다(이메일 주소라면 서버가 검증됨으로 표시한 것).

## 1. 표준대로 만들기

"표준"은 [MCP 규격](https://modelcontextprotocol.io/specification/latest)입니다. mcp-fuse가 여느 MCP client처럼
기대는 것은 다음입니다.

**전송 — [Streamable HTTP](https://modelcontextprotocol.io/specification/latest/basic/transports/streamable-http).**
JSON-RPC 메시지의 `POST`를 받아 JSON으로, 또는 마지막 프레임이 응답인 이벤트 스트림으로 답하는 엔드포인트 하나.
세션을 두지 않는 서버는 `Mcp-Session-Id`가 필요 없고, 두는 서버는 발급해도 됩니다(mcp-fuse는 그것을 돌려보내고,
서버가 잊어버린 세션에 `404`로 답하면 새 세션을 엽니다).

**Handshake.** mcp-fuse는 프로토콜 개정 `2025-11-25`와 빈 capability로 `initialize`를 보낸 뒤
`notifications/initialized`를 보냅니다. 지원하는 개정 중 `2025-11-25`보다 **새롭지 않은** 가장 높은 것으로 답하세요.
더 새로운 것으로 답하는 서버는 거부됩니다. (handshake가 없는 2026-07-28 개정만 말하는 서버는 아직 합칠 수 없습니다.
[protocol](protocol.ko.md)을 보세요.)

**메서드.** `tools/list`와 `tools/call`. 있다면 `prompts/*`와 `resources/*`. 구현하지 않은 메서드는 `-32601`로
답해도 되며 그냥 빈 것으로 취급합니다. 페이지로 나뉜 목록(`nextCursor`)은 끝까지 따라갑니다.

**인가 — [MCP 인가 규격](https://modelcontextprotocol.io/specification/latest/basic/authorization).**
서버는 OAuth resource server이고, mcp-fuse는 그 규격이 client에게 정한 방식으로 필요한 모든 것을 찾습니다.

- token이 없는 요청에는 `WWW-Authenticate: Bearer resource_metadata="<url>"`을 담은 `401`로 답합니다(또는 메타데이터를
  `/.well-known/oauth-protected-resource/<path>`에서 서비스합니다).
- 그 보호 자원 메타데이터([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728))의 `resource`는 서버의 URL(mcp-fuse에
  주는 것)과 같고, `authorization_servers`가 있습니다.
- 인가 서버는 메타데이터([RFC 8414](https://www.rfc-editor.org/rfc/rfc8414) 또는 OpenID discovery)를 공개하며,
  그 `issuer`는 위에 적힌 것과 정확히 같고, PKCE `S256`을 알리며, `authorization_endpoint`와 `token_endpoint`가
  있습니다. 인가 응답에 `iss`를 더한다면([RFC 9207](https://www.rfc-editor.org/rfc/rfc9207))
  `authorization_response_iss_parameter_supported`로 그렇다고 알립니다.
- `resource` 파라미터([RFC 8707](https://www.rfc-editor.org/rfc/rfc8707))를 받아들이고, audience가 서버인 token과
  refresh token을 발급합니다.
- 서버는 모든 token을 검증하고(서명, 만료, **audience가 자신인지**) 잘못된 것에는 `401`로 답합니다.

**목록에 대한 한 가지.** 각 목록은 요청한 사람의 token으로 요청되므로 사람마다 다를 수 있습니다. mcp-fuse에서
누구에게 무엇이 보일지를 정하는 방법이 이것입니다.

mcp-fuse가 보내는 것은 MCP client가 보내는 것뿐입니다. `Content-Type`, `Accept`, `User-Agent`,
`MCP-Protocol-Version`, 서버 자신의 `Mcp-Session-Id`, 서버 자신의 `Authorization`, 로그를 맞춰 보기 위한
`X-Request-Id`입니다. `X-Forwarded-*`는 없고, 최종 사용자의 client가 고른 것도 없습니다.

도구, prompt, resource의 **이름**은 다른 서버의 이름과 겹치지 않아야 합니다. 겹치면 mcp-fuse의 `backends`에서 먼저
적힌 서버가 그 이름을 갖습니다.

## 2. mcp-fuse를 인가 서버에 알리기

mcp-fuse는 서버의 인가 서버에 대한 OAuth client입니다. 다음 중 하나를 합니다.

- [동적 client 등록](https://www.rfc-editor.org/rfc/rfc7591)을 켜면 mcp-fuse가 스스로 한 번 등록합니다.
- client를 직접 등록합니다. PKCE와 refresh token을 쓰는 authorization code grant, redirect URI
  `<mcp-fuse resource origin><resource path>/connect/callback`(`https://mcp.example.com/mcp`라면
  `https://mcp.example.com/mcp/connect/callback`). client가 confidential이면 mcp-fuse에 그 secret을 파일로 줍니다.

mcp-fuse는 [client ID metadata document](https://modelcontextprotocol.io/specification/latest/basic/authorization/client-registration)를
쓰지 않습니다. 규격은 이쪽을 동적 등록보다 권합니다. 인가 서버가 그것만 지원한다면 client를 직접 등록하세요
([authorization](authorization.ko.md)).

## 3. mcp-fuse에 추가하기

설정([레퍼런스](configuration.ko.md))에:

```json
{
  "backends": [
    {"name": "orders", "url": "https://orders.example.com/mcp"}
  ]
}
```

미리 등록한 client가 있다면 `"client_id"`를, confidential이면 `"client_secret_file"`도 더합니다. mcp-fuse를
재기동하세요. 그러면 사람마다 받은 링크로 서버를 한 번 연결하고, 그때부터 그 서버의 도구가 목록에 나타납니다
([authorization](authorization.ko.md)). token을 요구하지 않는 서버라면(시험해 보는 동안) `"auth": "none"`으로
설정하세요.

## 추가하기 전에 서버 점검하기

```sh
# 1. token이 없으면 거부되고, 메타데이터가 어디 있는지 알려 줍니다.
curl -si -X POST https://orders.example.com/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"check","version":"0"}}}' \
  | head -20
#    기대: HTTP/1.1 401과 resource_metadata="…"를 담은 WWW-Authenticate 헤더

# 2. 메타데이터가 바로 이 서버를 resource로 말합니다.
curl -s "<the resource_metadata URL>" | jq '{resource, authorization_servers}'

# 3. 인가 서버가 mcp-fuse에 필요한 것을 지원합니다.
curl -s https://auth.example.com/.well-known/oauth-authorization-server \
  | jq '{issuer, code_challenge_methods_supported, authorization_endpoint, token_endpoint, registration_endpoint}'
#    기대: 2단계에 적힌 issuer, 지원 방식에 "S256"
```

세 가지가 모두 통과하면 mcp-fuse가 서버의 인가 서버를 찾아 그 서버용 token을 요청할 수 있습니다.
