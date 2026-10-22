# Configuration

[English](configuration.md) | **한국어** | [日本語](configuration.ja.md)

mcp-fuse는 `-config`로 지정한 JSON 파일 하나를 읽습니다(기본 `config.json`). 변경은 재기동해야 반영됩니다. 모르는
키는 오류이므로 철자가 틀린 옵션이 조용히 기본값으로 돌아가는 일이 없습니다. 프로세스는 0이 아닌 코드로 종료하며
무엇이 잘못됐는지 알려 줍니다.

전체 예시는 [`examples/config.json`](../examples/config.json)입니다.

## 키

| 키 | 필수 | 의미 |
|---|---|---|
| `listen` | 아니오 | 바인드할 주소. 기본 `127.0.0.1:8080`. loopback에 바인드하고 앞에 리버스 프록시를 둡니다([deployment](deployment.ko.md)). |
| `resource` | 예 | 에이전트가 접속하는 공개 URL. 예: `https://mcp.example.com/mcp`. MCP는 이 경로에서 서비스하고, 사람이 backend를 연결하는 페이지도 그 아래에 있습니다. 호출자의 token이 가져야 하는 audience이며, 보호 자원 메타데이터의 `resource`입니다. 요청의 `Host`는 대신 쓰지 않습니다. |
| `authorization_servers` | 예 | 에이전트가 로그인할 수 있는 인가 서버들. 보호 자원 메타데이터([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728))로 알립니다. 첫 번째 서버는 사람이 backend를 연결할 때 로그인하는 곳이기도 합니다([authorization](authorization.ko.md)). |
| `auth` | 아래 참고 | 호출자의 token을 확인하는 방법. |
| `auth.introspection_url` | `auth`가 있으면 예 | [RFC 7662](https://www.rfc-editor.org/rfc/rfc7662) 엔드포인트. token이 active이고, subject를 말하며, audience 중에 `resource`가 있으면 받아들입니다. |
| `auth.client_id` | `auth`가 있으면 예 | 그 인가 서버에서 mcp-fuse가 쓰는 OAuth client. introspection 엔드포인트에 인증하고, backend를 연결할 때 사람을 로그인시킵니다. |
| `auth.client_secret_file` | 아니오 | client의 secret이 든 파일. 없으면 client는 public이고 id만 보냅니다. |
| `state_file` | `oauth` backend가 있으면 | grant를 암호화해 보관하는 곳. 잃으면 모두가 다시 연결해야 하며, 그 밖에 이것에 의존하는 것은 없습니다. |
| `secret_file` | `oauth` backend가 있으면 | 32바이트 이상. `state_file`을 암호화하는 키와 연결 링크에 서명하는 키가 각각의 용도로 이것에서 파생됩니다. |
| `backends` | 예 | 합칠 MCP 서버들. 우선순위 순서입니다(아래 참고). |
| `backends[].name` | 예 | 고유해야 합니다. 로그, instructions, 연결 링크에 나타나므로 `/`, `?`, `#`, 공백이 없어야 합니다. |
| `backends[].url` | 예 | backend의 MCP 엔드포인트(http 또는 https). |
| `backends[].auth` | 아니오 | `"oauth"`(기본): 사람마다 backend를 연결하고 mcp-fuse는 그 사람 본인의 token을 씁니다. `"none"`: backend가 token을 요구하지 않습니다. |
| `backends[].client_id` | 아니오 | backend의 인가 서버에 mcp-fuse용으로 미리 등록한 client. 없으면 mcp-fuse가 스스로 등록([RFC 7591](https://www.rfc-editor.org/rfc/rfc7591))하고 결과를 기억합니다. |
| `backends[].client_secret_file` | 아니오 | 그 client가 confidential이면 그 secret이 든 파일. `client_id`가 필요합니다. |
| `list_timeout_seconds` | 아니오 | `initialize`와 목록이 backend를 기다리는 시간. 기본 15. |

`auth`는 모든 backend가 `"auth": "none"`일 때만 생략할 수 있습니다. 보관할 사람별 token이 없으면 식별할 사람도
없기 때문입니다. 그러면 mcp-fuse는 시작할 때 호출자가 인증되지 않는다는 경고를 로그에 남깁니다.

## 순서가 중요합니다

`backends`는 우선순위 목록입니다. 두 backend가 같은 이름의 도구, prompt, resource를 내놓으면 먼저 적힌 쪽이
이름을 갖고, 다른 쪽은 합친 목록에서 빠지며, 충돌은 로그에 남습니다. 특정 backend에 속하지 않는 요청(mcp-fuse가
모르는 메서드)은 그 사람이 연결한 첫 번째 backend로 갑니다.

## secret은 파일입니다

설정 파일에는 secret이 없고, 파일이 secret의 위치를 가리킵니다. 각 secret 파일은 서비스 사용자에게만 주세요
(`0600`). state 파일은 `0600`으로 만들어집니다.

## 등록할 redirect URI

mcp-fuse는 `resource`에서 redirect URI 두 개를 만듭니다. `https://mcp.example.com/mcp`라면:

| 등록할 곳 | Redirect URI |
|---|---|
| 호출자가 로그인하는 인가 서버의 `auth.client_id` client | `https://mcp.example.com/mcp/connect/identify` |
| 각 backend의 인가 서버에 있는 mcp-fuse client(스스로 등록하는 경우는 제외) | `https://mcp.example.com/mcp/connect/callback` |

각 등록에서 무엇을 허용해야 하는지는 [authorization](authorization.ko.md)을 보세요.

## 한도

코드에 고정된 값입니다. 요청 본문은 최대 16 MiB, 통째로 읽는 backend 응답(목록, `initialize`)은 최대 32 MiB,
backend가 목록을 페이지로 나눌 때 최대 20페이지, 연결 링크의 유효 시간은 10분입니다.
