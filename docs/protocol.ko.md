# Protocol

[English](protocol.md) | **한국어** | [日本語](protocol.ja.md)

mcp-fuse는 Streamable HTTP 위에서 MCP를 말하며 [MCP 규격](https://modelcontextprotocol.io/specification/latest)을
따릅니다.

## 처리하는 것

| 에이전트가 보내면 | mcp-fuse가 하는 일 |
|---|---|
| `initialize` | 그 사람이 연결한 모든 backend와 세션을 엽니다. 그중 가장 오래된 프로토콜 버전, capability의 합집합, 각 backend의 instructions를 backend 이름 아래에 차례로 붙여 답합니다. 그 사람이 연결하지 않은 backend도 거기에 설명하고, 그것을 연결하는 링크를 함께 줍니다 |
| `tools/list`, `prompts/list`, `resources/list` | 연결된 모든 backend의 목록을 **그 사람으로서** 받아(페이지 나눔을 따라가며) 답을 합칩니다. 응답하지 않거나 그 메서드가 없는 backend는 아무것도 더하지 않습니다 |
| `tools/call`, `prompts/get`, `resources/read` | 그 이름을 목록에 올린 backend로 요청을 그대로 보내고, 응답도 그대로 스트리밍해 돌려줍니다 |
| `notifications/cancelled` 등 알림 | 그 사람의 세션이 있는 연결된 모든 backend에 전달합니다 |
| `notifications/initialized` | 흡수합니다. mcp-fuse가 backend와 handshake를 따로 합니다 |
| `ping` | 로컬에서 답합니다 |
| 그 밖의 것 | 그 사람이 연결한 첫 번째 backend로 전달합니다 |

이름은 바꾸지 않습니다. 같은 이름을 내놓은 backend가 있으면 `backends`에서 앞에 있는 쪽이 갖습니다.

## 세션

세션은 mcp-fuse와 각 backend 사이에 있습니다. 에이전트에게는 세션을 주지 않으며, 에이전트가 보내는
`Mcp-Session-Id`는 무시합니다. mcp-fuse는 처음 필요할 때(또는 에이전트가 `initialize`를 호출할 때) 세션을 열고,
backend가 합의한 프로토콜 개정을 기억해 `MCP-Protocol-Version`으로 다시 보내며, backend가 더는 모르는
세션에 `404`로 답하면 새 세션을 열어 요청을 한 번 반복합니다.

## 개정

- **지원:** 2025-11-25까지의 MCP 개정. `initialize` handshake, 선택적인 `Mcp-Session-Id`, JSON 또는 이벤트
  스트림 응답. batch는 2025-06-18과 같이 거부합니다.
- **지원하지 않음:** handshake와 프로토콜 수준 세션을 없앤 2026-07-28 개정. `MCP-Protocol-Version: 2026-07-28`
  이상을 담은 요청에는 본문이 평문인 `400`으로 답하며, 두 개정을 모두 지원하는 client는 이를 `initialize`로
  물러나라는 신호로 받아들입니다.
- **제공하지 않음:** 상시 `GET` 이벤트 스트림(MCP 경로의 `GET`은 `405`), 세션 종료(`DELETE`는 `405`),
  서버에서 client로 가는 요청(sampling, elicitation), resource template. elicitation을 제공하지 않으므로 backend를
  연결하는 링크는 `initialize`의 instructions에 담아 보냅니다.

## 오류

mcp-fuse는 처리할 수 없는 요청에 JSON-RPC 오류로 답하며, HTTP 상태가 어느 쪽이 실패했는지 알려 줍니다.

| 상황 | 상태 | 비고 |
|---|---|---|
| token이 없거나 mcp-fuse에 유효하지 않음 | `401` | `WWW-Authenticate: Bearer resource_metadata="…"`가 이 서버 자신의 메타데이터를 가리킵니다 |
| 인가 서버에 물을 수 없음 | `503` | `401`이 아닙니다. `401`이면 client가 다시 로그인하러 가게 됩니다 |
| 연결된 어떤 backend도 내놓지 않은 이름 | `200` | JSON-RPC 오류 `-32602` |
| 그 사람이 연결하지 않은 backend를 직접 호출 | `200` | JSON-RPC 오류 `-32004` |
| 답해야 하는 모든 backend가 실패 | `502` | JSON-RPC 오류 `-32002` |
| 지원하는 것보다 새로운 프로토콜 개정 | `400` | 평문 |
