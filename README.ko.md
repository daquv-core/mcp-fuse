# mcp-fuse

[English](README.md) | **한국어** | [日本語](README.ja.md)

여러 MCP 서버를 하나의 MCP 서버처럼 보이게 하는 중계 서버입니다.

> **상태: 첫 구현이며 아직 릴리스가 없습니다.** MCP 규격 2025-11-25 이전을 지원합니다([프로토콜](#프로토콜) 참고).
>
> 이 문서는 [`README.md`](README.md)(영문)를 따릅니다. 내용이 다르면 영문이 기준입니다.

## 하는 일

AI 에이전트(ChatGPT, Gemini, Claude, 사내 AI 비서 등)는 **하나의** MCP 엔드포인트에만 접속합니다. `mcp-fuse`는
뒤에 있는 backend들이 내놓는 것을 합쳐서 보여 주고, 호출은 그것을 가진 backend로 전달합니다.

| 에이전트가 보내면 | mcp-fuse가 하는 일 |
|---|---|
| `initialize` | 모든 backend에 묻고, 그중 가장 오래된 프로토콜 버전, capability의 합집합, backend별 이름을 붙인 instructions를 이어서 답합니다 |
| `tools/list`, `prompts/list`, `resources/list` | **호출자의 신원으로** 모든 backend의 목록을 받아 합칩니다. 응답하지 않는 backend는 빠집니다 |
| `tools/call`, `prompts/get`, `resources/read` | 그 이름을 목록에 올린 backend로 요청을 **그대로** 보내고, 응답도 그대로 스트리밍합니다 |
| `notifications/cancelled` 등 알림 | 모든 backend에 전달합니다(`notifications/initialized`는 stateful backend에만) |
| 그 외 | stateful backend(없으면 첫 번째 backend)로 전달합니다 |

- 이름은 바꾸지 않습니다. 두 backend가 같은 이름을 내놓으면 설정에서 앞선 backend가 그 이름을 갖고, 겹침은 로그에
  남습니다.
- 인자와 결과는 바이트 단위로 그대로 통과합니다.
- 사람마다 무엇이 보이는지는 각 backend가 정합니다. `mcp-fuse`는 필터를 더하지 않습니다.
- backend 하나가 죽어도 나머지 도구는 남습니다.
- stateful backend가 호출자의 세션을 모르면 `404`로 답합니다. 클라이언트가 불완전한 목록을 조용히 받지 않고
  `initialize`를 다시 하게 하기 위해서입니다.

## 하지 않는 것

아래는 이 서버의 범위가 아니며 앞으로도 그렇습니다. 필요하면 backend 쪽에서 처리합니다.

- 인증, token 발급·검증·보관. 호출자의 `Authorization` 헤더는 그대로 전달합니다.
- 정책, 마스킹, 결과 가공, 캐시, 재시도, LLM.
- 도구가 무엇을 뜻하는지 이해하는 일.
- 요청·응답 본문 기록. 로그에는 도구 이름, backend, 지연 시간, 상태만 남습니다.

## 실행

```sh
./build.sh                                   # gofmt + vet + 정적 바이너리: ./mcp-fuse
cp config.example.json config.json           # 편집
./mcp-fuse -config config.json
```

`./mcp-fuse -version`은 버전을 출력합니다. Go 표준 라이브러리 외의 의존성은 없습니다.

### 설정

JSON 파일 하나이며, 바꾸면 재기동해야 반영됩니다. 알 수 없는 키는 오류입니다.

```json
{
  "listen": "127.0.0.1:8080",
  "resource": "https://mcp.example.com/mcp",
  "authorization_servers": ["https://auth.example.com"],
  "backends": [
    {"name": "quvi", "url": "http://127.0.0.1:8081/v3/mcp/nl2sql", "stateful": true},
    {"name": "exec", "url": "http://127.0.0.1:9000/mcp"}
  ],
  "list_timeout_seconds": 15
}
```

| 키 | |
|---|---|
| `listen` | 바인드할 주소. 기본값 `127.0.0.1:8080`. |
| `resource` | 에이전트가 접속하는 공개 URL. MCP는 이 URL의 경로에서 서비스합니다. 발견 문서와 401 challenge에 쓰입니다. |
| `authorization_servers` | 에이전트가 로그인할 곳. 무엇으로 할지는 배포 시 정하며, mcp-fuse는 알려 주기만 합니다. |
| `backends[].name` | 중복 불가. 로그와 instructions에 표시됩니다. |
| `backends[].url` | backend의 MCP 엔드포인트. |
| `backends[].stateful` | backend가 MCP 세션(`Mcp-Session-Id`)을 유지합니다. 최대 1개. 세션 id는 그 backend에만 보냅니다. |
| `list_timeout_seconds` | `initialize`와 목록 요청이 backend를 기다리는 시간. 기본값 15. |

### 엔드포인트

| | |
|---|---|
| `POST <resource 경로>` | MCP. `GET`과 `DELETE`는 405로 답합니다. |
| `GET /.well-known/oauth-protected-resource[/<resource 경로>]` | 보호 자원 메타데이터([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)): 설정의 `resource`와 `authorization_servers`. |
| `GET /healthz` | `{"status":"ok","version":"…"}`. backend를 확인하지는 않습니다. |

backend의 `401`은 이 서버의 메타데이터 문서를 가리키는 `WWW-Authenticate`를 단 `401`로 바뀝니다. 에이전트가 실제로
접속한 주소를 기준으로 인가받게 하기 위해서입니다.

## 프로토콜

동작은 [MCP 규격](https://modelcontextprotocol.io/specification/latest)을 따릅니다.

- **지원:** Streamable HTTP 위의 2025-11-25 이전 개정: `initialize` 핸드셰이크, 선택적 `Mcp-Session-Id`, JSON 또는
  이벤트 스트림 응답. 2025-06-18과 같이 batch는 거절합니다.
- **아직 없음:** 핸드셰이크와 세션을 없앤 2026-07-28 개정. `MCP-Protocol-Version: 2026-07-28` 이상을 단 요청에는
  `400`으로 답하며, 두 개정을 모두 지원하는 클라이언트는 이를 `initialize`로 되돌아가라는 신호로 받아들입니다.
- **제공하지 않음:** 상시 `GET` 이벤트 스트림, 서버→클라이언트 요청(sampling, elicitation). resource template은
  합치지 않습니다.

## 배포 형태

정적 바이너리 또는 컨테이너 하나, 설정 파일 하나, DB 없음. backend와 같은 망 안에서 실행하며, 앞단의 web 계층과
로드밸런서는 TLS 종료와 경로 하나의 전달만 맡습니다. 운영 계약 전문은
[`docs/cp_share/contract.md`](docs/cp_share/contract.md)(영문)입니다.

## 번역

[`README.md`](README.md)(영문)가 원본이고, 이 문서와 [`README.ja.md`](README.ja.md)가 따릅니다. 원본이 바뀌면 세
파일을 함께 고칩니다.
