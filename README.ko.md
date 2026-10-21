# mcp-fuse

[English](README.md) | **한국어** | [日本語](README.ja.md)

여러 MCP 서버를 하나의 MCP 서버처럼 보이게 하는 중계 서버입니다.

> 이 문서는 [`README.md`](README.md)(영문)를 따릅니다. 내용이 다르면 영문이 기준입니다.

## 하는 일

AI 에이전트(ChatGPT, Gemini, Claude, 사내 AI 비서 등)는 **하나의** MCP 엔드포인트에만 접속합니다. `mcp-fuse`는
뒤에 있는 backend들이 내놓는 것을 합쳐서 보여 주고, 호출은 그것을 가진 backend로 전달합니다.

에이전트에게 `mcp-fuse`는 평범한 MCP resource server입니다. 에이전트는 `mcp-fuse`가 알려 주는 인가 서버에서
로그인하고, 받은 token은 **`mcp-fuse`용**입니다. 각 backend에게 `mcp-fuse`는 평범한 MCP client입니다. **그
backend의 인가 서버가 그 사람을 위해 발급한 token**을 따로 보관해 씁니다. 에이전트가 낸 token은 어떤 backend에도
보내지 않으므로, backend는 표준 MCP 서버이기만 하면 됩니다.

| 에이전트가 보내면 | mcp-fuse가 하는 일 |
|---|---|
| `initialize` | 그 사람이 연결해 둔 모든 backend와 세션을 열고, 그중 가장 오래된 프로토콜 버전, capability의 합집합, backend별 이름을 붙인 instructions를 이어서 답합니다. 아직 연결하지 않은 backend도 연결 링크와 함께 여기에 적습니다 |
| `tools/list`, `prompts/list`, `resources/list` | 연결된 모든 backend의 목록을 **그 사람의 신원으로** 받아 합칩니다. 응답하지 않는 backend는 빠집니다 |
| `tools/call`, `prompts/get`, `resources/read` | 그 이름을 목록에 올린 backend로 요청을 **그대로** 보내고, 응답도 그대로 스트리밍합니다 |
| `notifications/cancelled` 등 알림 | 세션이 있는 연결된 모든 backend에 전달합니다 |
| 그 외 | 그 사람이 연결한 첫 번째 backend로 전달합니다 |

- 이름은 바꾸지 않습니다. 두 backend가 같은 이름을 내놓으면 설정에서 앞선 backend가 그 이름을 갖고, 겹침은 로그에
  남습니다.
- 인자와 결과는 바이트 단위로 그대로 통과합니다.
- 사람마다 무엇이 보이는지는 각 backend가 정합니다. `mcp-fuse`는 필터를 더하지 않습니다.
- backend 하나가 죽어도 나머지 도구는 남습니다.
- 세션은 `mcp-fuse`와 각 backend 사이의 것입니다. 에이전트에게는 세션을 주지 않으며, backend가 세션을 잊으면
  `mcp-fuse`가 새로 열고 요청을 다시 보냅니다.

### backend 연결하기

backend는 사람마다 한 번 연결합니다.

1. 에이전트가 `initialize`를 호출합니다. 그 사람이 아직 연결하지 않은 backend마다 instructions에 링크가 들어
   있습니다.
2. 사람이 링크를 브라우저에서 엽니다. 링크는 그 backend의 인가 서버로 이어집니다(backend의 보호 자원
   메타데이터, RFC 9728로 찾으며, PKCE를 쓴 authorization code 방식이고 resource indicator, RFC 8707로
   backend에 묶습니다).
3. 거기서 로그인하면 `mcp-fuse`로 돌아오고, `mcp-fuse`는 그 사람과 그 backend의 token을 암호화해 보관합니다.
   에이전트가 대화를 다시 시작하면 그 backend의 도구가 보입니다.

인가 서버가 refresh token을 더는 받아 주지 않으면 그 backend는 그 사람의 목록에서 빠지고, 링크가 instructions에
다시 나타납니다.

## 하지 않는 것

아래는 이 서버의 범위가 아니며 앞으로도 그렇습니다. 필요하면 backend 쪽에서 처리합니다.

- token을 다른 곳으로 넘기는 일. 에이전트가 `mcp-fuse`에 낸 token은 확인하는 데만 쓰고 다른 데 쓰지 않습니다.
- 정책, 마스킹, 결과 가공, 캐시, 재시도, LLM.
- 도구가 무엇을 뜻하는지 이해하는 일.
- 요청·응답 본문이나 token의 로그 기록. 로그에는 도구 이름, backend, 지연 시간, 상태만 남습니다.

## 실행

```sh
./build.sh                                   # gofmt + vet + 정적 바이너리: ./mcp-fuse
cp config.example.json config.json           # 편집
./mcp-fuse -config config.json
```

`./mcp-fuse -version`은 버전을 출력합니다. Go 표준 라이브러리 외의 의존성은 없습니다. grant를 로컬 파일에 두므로
**인스턴스는 하나만** 실행합니다.

### 설정

JSON 파일 하나이며, 바꾸면 재기동해야 반영됩니다. 알 수 없는 키는 오류입니다.

```json
{
  "listen": "127.0.0.1:8080",
  "resource": "https://mcp.example.com/mcp",
  "authorization_servers": ["https://auth.example.com"],
  "auth": {
    "introspection_url": "https://auth.example.com/oauth/introspect",
    "client_id": "mcp-fuse",
    "client_secret_file": "/etc/mcp-fuse/introspection-secret"
  },
  "state_file": "/var/lib/mcp-fuse/state.bin",
  "secret_file": "/etc/mcp-fuse/secret",
  "backends": [
    {"name": "quvi", "url": "https://quvi.internal/v3/mcp/nl2sql"},
    {"name": "exec", "url": "https://exec-mcp.internal/mcp",
     "client_id": "mcp-fuse", "client_secret_file": "/etc/mcp-fuse/exec-client-secret"}
  ],
  "list_timeout_seconds": 15
}
```

| 키 | |
|---|---|
| `listen` | 바인드할 주소. 기본값 `127.0.0.1:8080`. |
| `resource` | 에이전트가 접속하는 공개 URL. MCP는 이 URL의 경로에서 서비스합니다. 호출자의 token이 가져야 할 audience입니다. |
| `authorization_servers` | 에이전트가 로그인할 곳. 무엇으로 할지는 배포 시 정하며, mcp-fuse는 알려 주기만 합니다. |
| `auth.introspection_url`, `auth.client_id`, `auth.client_secret_file` | 호출자 token을 확인하는 방법(RFC 7662). token은 활성 상태이고, subject가 있고, `resource`를 audience로 가져야 합니다. 모든 backend가 `"auth": "none"`일 때만 `auth`를 생략할 수 있습니다. |
| `state_file` | grant를 암호화해 보관하는 파일. 잃으면 모두가 다시 연결해야 합니다. |
| `secret_file` | 32바이트 이상. `state_file`과 연결 링크의 키가 여기서 파생됩니다. |
| `backends[].name` | 중복 불가. 로그, instructions, 연결 링크에 표시됩니다. `/`, `?`, `#`, 공백은 쓸 수 없습니다. |
| `backends[].url` | backend의 MCP 엔드포인트. |
| `backends[].auth` | `"oauth"`(기본값): 사람마다 연결합니다. `"none"`: backend가 token을 요구하지 않습니다. |
| `backends[].client_id`, `client_secret_file` | backend의 인가 서버에 mcp-fuse용으로 미리 등록해 둔 client. `client_id`가 없으면 mcp-fuse가 스스로 등록하고(RFC 7591) 결과를 기억합니다. |
| `list_timeout_seconds` | `initialize`와 목록 요청이 backend를 기다리는 시간. 기본값 15. |

backend의 인가 서버에 등록할 redirect URI는 `<resource의 origin><resource 경로>/connect/callback`이며, 위 예시에서는
`https://mcp.example.com/mcp/connect/callback`입니다.

### 엔드포인트

| | |
|---|---|
| `POST <resource 경로>` | MCP. `GET`과 `DELETE`는 405로 답합니다. |
| `GET <resource 경로>/connect/<backend>?t=…` | backend 연결을 시작합니다. 링크는 instructions로 전달되며 10분간 유효합니다. |
| `GET <resource 경로>/connect/callback` | backend의 인가 서버가 사람을 돌려보내는 곳. |
| `GET /.well-known/oauth-protected-resource[/<resource 경로>]` | 보호 자원 메타데이터([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)): 설정의 `resource`와 `authorization_servers`. |
| `GET /healthz` | `{"status":"ok","version":"…"}`. backend를 확인하지는 않습니다. |

### backend가 받는 것

표준 MCP client가 보내는 것만 받습니다: `Content-Type`, `Accept`, `User-Agent`, `MCP-Protocol-Version`, 자기
`Mcp-Session-Id`, 자기 `Authorization`, 그리고 로그를 맞춰 볼 `X-Request-Id`. 호출자가 보낸 것은 하나도 복사하지
않고 `X-Forwarded-*`도 붙이지 않습니다.

## 프로토콜

동작은 [MCP 규격](https://modelcontextprotocol.io/specification/latest)을 따릅니다.

- **지원:** Streamable HTTP 위의 2025-11-25 이전 개정: `initialize` 핸드셰이크, 선택적 `Mcp-Session-Id`, JSON 또는
  이벤트 스트림 응답. 2025-06-18과 같이 batch는 거절합니다.
- **미지원:** 핸드셰이크와 세션을 없앤 2026-07-28 개정. `MCP-Protocol-Version: 2026-07-28` 이상을 단 요청에는
  `400`으로 답하며, 두 개정을 모두 지원하는 클라이언트는 이를 `initialize`로 되돌아가라는 신호로 받아들입니다.
- **제공하지 않음:** 상시 `GET` 이벤트 스트림, 서버→클라이언트 요청(sampling, elicitation). 그래서 연결 링크는 URL
  방식 elicitation이 아니라 instructions로 전달합니다. resource template은 합치지 않습니다.

## 배포 형태

정적 바이너리 또는 컨테이너 하나, 설정 파일 하나, 상태 파일 하나, 인스턴스 하나. backend와 같은 망 안에서
실행하며, 앞단의 web 계층과 로드밸런서는 TLS 종료와 경로 하나의 전달만 맡습니다. 운영 계약 전문은
[`docs/cp_share/contract.md`](docs/cp_share/contract.md)(영문)입니다.

## 번역

[`README.md`](README.md)(영문)가 원본이고, 이 문서와 [`README.ja.md`](README.ja.md)가 따릅니다. 원본이 바뀌면 세
파일을 함께 고칩니다.
