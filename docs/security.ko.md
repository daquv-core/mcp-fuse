# Security model

[English](security.md) | **한국어** | [日本語](security.ja.md)

mcp-fuse는 에이전트와 데이터를 가진 시스템들 사이에 있으므로, 도구를 어떻게 다루는지보다 자격 증명과 본문을
어떻게 다루는지가 더 중요합니다. 이 문서는 무엇을 보장하는지, 왜 그런지, 무엇을 사용자에게 남기는지를 정리합니다.
취약점을 제보하려면 [SECURITY.md](../SECURITY.md)를 보세요.

## 보장하는 것

**token을 다른 곳으로 넘기지 않습니다.** 에이전트가 내민 token은 mcp-fuse를 위한 것입니다. introspect하고,
mcp-fuse의 `resource`가 audience에 있어야 하며, 호출자가 누구인지 아는 데에만 씁니다. backend는 자기 인가 서버가
그 사람을 위해, 그 backend를 audience로 하여 발급한 token만 받습니다. 호출자의 token을 전달하면 backend가
자신을 위한 것이 아닌 token을 받거나, 한 서비스용 token을 가진 공격자가 다른 서비스에서 재사용할 수 있습니다.
MCP 인가 규격이 금지하는 confused deputy 문제입니다("token passthrough").

**backend에는 MCP client가 보내는 것만 보내고, 호출자가 고른 것은 보내지 않습니다.** `Content-Type`, `Accept`,
`User-Agent`, `MCP-Protocol-Version`, backend 자신의 `Mcp-Session-Id`, 자신의 `Authorization`, 그리고 backend의
로그를 우리 로그와 맞춰 볼 수 있게 하는 `X-Request-Id`입니다. 호출자의 헤더는 복사하지 않고 `X-Forwarded-*`도
추가하지 않습니다. 호출자가 보낸 request id는 분명히 id인 경우(`[A-Za-z0-9._-]`, 최대 64자)에만 전달합니다.

**도구 호출은 그대로 통과합니다.** 인자와 응답은 JSON-RPC 프레임 이상으로 해석하지 않고, 다시 인코딩하지 않고,
저장하지 않습니다. 에이전트가 보낸 것이 바이트 단위로 그대로 backend에 가고, 반대도 마찬가지입니다.

**민감한 것은 기록하지 않습니다.** 로그에는 도구 이름, backend, 지연 시간, 상태, request id가 남습니다. 요청이나
응답 본문, token, secret, backend나 인가 서버가 낸 오류의 문구는 남지 않습니다(분류나 코드만 남습니다).

**한 사람의 연결이 다른 사람의 것이 되는 일은 없습니다.** grant의 키는 introspection이 확정한 사람입니다. 라우팅
색인도 사람별입니다. backend를 연결하려면 증명 두 가지가 필요합니다.

- *누가 연결하는가.* 사람이 에이전트가 로그인하는 인가 서버에서 로그인해야 하고, 링크가 만들어진 바로 그 사람이어야
  합니다. 연결 링크는 서명된 10분짜리 ticket이며, 신원이 아니라 주장입니다. 이 단계가 없으면 링크를 받은 누구든
  자기 자신으로 backend에 로그인하고, 그 token이 링크 주인의 것으로 저장되어 링크 주인이 그 사람으로 행동할 수
  있게 됩니다.
- *어느 브라우저인가.* 링크를 열 때 심는 cookie(`HttpOnly`, `SameSite=Lax`, 연결 페이지로 범위 한정)가 모든
  단계에서 돌아와야 하고, 모든 `state` 값은 한 번만 쓸 수 있습니다. 이것이 없으면 backend의 로그인 주소를 다른
  사람에게 건넬 수 있고, 그 사람의 token이 첫 번째 사람의 것으로 저장됩니다.

**grant는 저장 시 암호화됩니다.** `state_file`은 `secret_file`에서 파생한 키를 쓰는 AES-256-GCM이며, `0600`으로
생성되고 임시 파일을 거쳐 쓰므로 중간에 죽어도 이전 상태가 남습니다. 연결 ticket은 같은 secret에서 파생한 다른
키로 서명합니다.

**인가 서버는 신뢰를 얻어야 합니다.** mcp-fuse는 PKCE S256을 알리지 않는 서버, 메타데이터가 다른 issuer를 말하는
서버, 보호 자원 메타데이터가 다른 resource를 말하는 서버를 거부합니다. 인가 응답의 `iss` 파라미터
([RFC 9207](https://www.rfc-editor.org/rfc/rfc9207))도 확인합니다.

## 사용자에게 남기는 것

- **호스트.** `state_file`과 `secret_file`을 읽을 수 있는 사람은 저장된 token을 쓸 수 있습니다. 두 파일과 서비스
  사용자를 보호하세요. 백업도 같은 주의로 다루세요. state 파일에는 살아 있는 자격 증명이 들어 있습니다.
- **backend로의 TLS**는 시스템의 신뢰 루트를 씁니다. 사설 CA를 쓰는 backend는 그 CA가 시스템 신뢰 저장소(또는
  컨테이너 이미지)에 있어야 합니다.
- **리버스 프록시.** mcp-fuse는 기본적으로 loopback에 바인드하며 앞단에서 TLS가 종료된다고 가정합니다.
  `listen`을 직접 노출하지 마세요.
- **backend가 돌려주는 것.** mcp-fuse는 결과를 검사하지 않습니다. 결과로 데이터를 누출하는 backend는 mcp-fuse를
  통해서도 누출합니다. 마스킹과 인가는 backend의 몫입니다.
- **인가 서버의 가용성.** introspection에 닿지 못하면 호출자는 `503`을 받습니다(`401`이 아닙니다. `401`이면
  client가 다시 로그인하러 갑니다). 성공한 introspection은 최대 60초 또는 token의 `exp`까지 캐시되므로, 인가
  서버에서 폐기한 token이 그 시간 동안은 여전히 동작할 수 있습니다.
- **인스턴스는 하나.** grant는 로컬 파일에, 진행 중인 연결은 메모리에 있습니다. 인스턴스가 둘이면 갈라집니다.

## 일부러 하지 않는 것

정책, 마스킹, 결과 가공, 결과 캐시, 끊어진 세션을 다시 여는 것과 token 갱신을 넘어서는 재시도, 그리고 모든
LLM. 이들은 backend의 몫입니다. [CONTRIBUTING](../CONTRIBUTING.md)을 보세요.
