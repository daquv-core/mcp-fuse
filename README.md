# mcp-fuse

A reverse proxy that presents several MCP servers as one.

여러 MCP 서버를 하나의 MCP 서버처럼 보이게 하는 중계 서버입니다.

> **Status: design in progress.** There is no release yet. / 설계 중이며 아직 릴리스가 없습니다.

## What it does

An AI agent (ChatGPT, Gemini, Claude, or an in-house assistant) connects to **one** MCP endpoint. `mcp-fuse` merges the tool lists of the backends behind it and forwards each call to the backend that owns the tool.

- Tool names are kept as they are. Name collisions between backends are rejected at startup.
- Tool arguments and results pass through unchanged.
- Each backend decides what a given person may see. `mcp-fuse` adds no filter.
- One backend being down does not remove the tools of the others.

## What it does not do

These are out of scope and will stay that way. Requests for them belong in the backends.

- Authentication, token issuing, token validation or token storage. The caller's `Authorization` header is forwarded as is.
- Policy, masking, result transformation, caching, retries, or any LLM.
- Understanding what a tool means.
- Logging request or response bodies. Logs carry the tool name, backend, latency and status only.

## 하지 않는 것

인증·token 발급·검증·보관, 정책·마스킹·결과 가공·캐시·재시도·LLM, 도구 의미 이해, 요청·응답 본문 기록은 이 서버의 범위가 아닙니다. 필요하면 각 backend 쪽에서 처리합니다.

## Deployment shape

A single static binary or container, one configuration file, no database. It runs inside the network next to its backends; the web tier and load balancer in front of it only terminate TLS and route one path.

## Protocol

Protocol behavior follows the [MCP specification](https://modelcontextprotocol.io/specification/latest). Usage and configuration will be documented here together with the first release.
