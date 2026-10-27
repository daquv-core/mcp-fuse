# mcp-fuse

[English](README.md) | **한국어** | [日本語](README.ja.md)

여러 MCP 서버를 하나의 MCP 서버처럼 보이게 하는 리버스 프록시입니다.

AI 에이전트(ChatGPT, Gemini, Claude, 사내 AI 비서 등)는 MCP 엔드포인트가 하나일 때 가장 잘 일합니다. 그런데
쓰게 하고 싶은 것은 흔히 여러 MCP 서버에 나뉘어 있습니다. 애플리케이션 자체의 서버, 데이터 조회 서버, 도구
서버 같은 것들입니다. 하나씩 따로 연결하면 어느 도구를 언제 쓸지가 에이전트의 prompt에 맡겨집니다.
`mcp-fuse`는 이들을 엔드포인트 하나 뒤에 두고, 내놓는 것을 합쳐서 보여 주고, 호출은 그 도구를 가진 서버로
보냅니다. 서버들은 바꾸지 않습니다.

## 하는 일

- 모든 backend의 `initialize`와 도구·prompt·resource 목록을 합칩니다. 이름은 바꾸지 않으며, 두 backend가 같은
  이름을 내놓으면 먼저 적힌 backend가 그 이름을 갖습니다.
- 호출은 그 도구를 목록에 올린 backend로 **그대로** 보냅니다. 인자와 결과는 바이트 단위로 그대로 통과합니다.
- 각 backend의 목록을 **요청한 사람의 신원으로** 받으므로 사람마다 무엇이 보이는지는 각 backend가 정합니다.
  mcp-fuse는 필터를 더하지 않으며, backend 하나가 죽어도 나머지 도구는 남습니다.
- 에이전트에게는 표준 MCP **resource server**이고, 각 backend에게는 표준 MCP **client**입니다. 에이전트가 낸
  token은 확인에만 쓰고 다른 곳으로 보내지 않습니다. 각 backend는 자기 인가 서버가 그 사람을 위해 발급한 token을
  받습니다. backend는 표준 MCP 서버이기만 하면 됩니다.

### backend 연결하기

backend는 사람마다 한 번 연결합니다.

1. 에이전트가 `initialize`를 호출합니다. 그 사람이 아직 연결하지 않은 backend마다 instructions에 링크가 들어
   있습니다.
2. 사람이 링크를 브라우저에서 열고 에이전트가 로그인하는 인가 서버에서 로그인합니다. 이것이 누가 연결하는지를
   증명하며, 링크가 만들어진 바로 그 사람이어야 합니다.
3. 이어서 그 backend의 인가 서버로 가서 거기서 로그인합니다.
4. mcp-fuse는 그 사람과 그 backend의 token을 암호화해 보관합니다.

전 과정은 링크를 연 브라우저에서 끝내야 합니다. [authorization](docs/authorization.ko.md)을 보세요.

## 내 MCP 서버 추가하기

서버를 mcp-fuse 때문에 바꿀 필요는 없습니다. [MCP 규격](https://modelcontextprotocol.io/specification/latest)대로 만드세요.
Streamable HTTP와 [인가 규격](https://modelcontextprotocol.io/specification/latest/basic/authorization)(보호 자원
메타데이터, PKCE를 지원하는 인가 서버, 서버 자신을 audience로 한 token)입니다. 그다음:

1. 서버의 인가 서버에 mcp-fuse를 OAuth client로 알립니다(또는 동적 client 등록을 켭니다).
2. `backends`에 `{"name": "orders", "url": "https://orders.example.com/mcp"}`를 추가하고 재기동합니다.

사람마다 링크로 한 번 연결하면, 그 서버의 도구가 다른 서버의 도구와 합쳐져 그 사람의 목록에 나타납니다.
[Adding your MCP server](docs/backends.ko.md)에 무엇이 필요한지와 추가하기 전에 서버를 점검하는 방법이
있습니다.

## 하지 않는 것

아래는 이 서버의 범위가 아니며 앞으로도 그렇습니다. 필요하면 backend 쪽에서 처리합니다.

- token을 다른 곳으로 넘기는 일. 호출자의 token은 확인하는 데만 쓰고 다른 데 쓰지 않습니다.
- 정책, 마스킹, 결과 가공, 결과 캐시, 재시도, LLM.
- 도구가 무엇을 뜻하는지 이해하는 일.
- 요청·응답 본문이나 token의 로그 기록. 로그에는 도구 이름, backend, 지연 시간, 상태만 남습니다.

## 빠른 시작

```sh
./build.sh                                   # gofmt, vet, 정적 바이너리: ./mcp-fuse
cp examples/config.json config.json          # 편집
./mcp-fuse -config config.json
```

또는 [release](docs/deployment.ko.md#릴리스)의 바이너리(`mcp-fuse-linux-amd64`, `SHA256SUMS` 포함)를 받아 씁니다.

최소 설정:

```json
{
  "resource": "https://mcp.example.com/mcp",
  "authorization_servers": ["https://auth.example.com"],
  "auth": {"introspection_url": "https://auth.example.com/oauth/introspect", "client_id": "mcp-fuse"},
  "state_file": "/var/lib/mcp-fuse/state.bin",
  "secret_file": "/etc/mcp-fuse/secret",
  "backends": [
    {"name": "orders", "url": "https://orders.example.com/mcp"},
    {"name": "analytics", "url": "https://analytics.example.com/mcp"}
  ]
}
```

앞단에 TLS를 종료하는 리버스 프록시([`examples/nginx.conf`](examples/nginx.conf))를 두고, 인가 서버들에 mcp-fuse를
등록합니다([authorization](docs/authorization.ko.md)). **인스턴스는 하나만** 실행합니다. 컨테이너 이미지는
`docker build -t mcp-fuse .`로 만듭니다.

## 문서

`docs/` 아래 문서는 영문이 원본이고, 각각 `*.ko.md`와 `*.ja.md`가 따릅니다.

- [Adding your MCP server](docs/backends.ko.md) — 무엇을 만들고, 무엇을 등록하고, 무엇을 추가하는지.
- [Configuration](docs/configuration.ko.md) — 모든 설정 키.
- [Authorization](docs/authorization.ko.md) — 무엇을 어디에 등록하는지, backend를 어떻게 연결하는지.
- [A complete setup with Keycloak](docs/keycloak.ko.md) — Keycloak, 관리 화면, mcp-fuse를 처음부터 끝까지.
- [Deployment](docs/deployment.ko.md) — 빌드, 파일, 리버스 프록시, 운영.
- [Security model](docs/security.ko.md) — 보장하는 것과 사용자에게 남기는 것.
- [Protocol](docs/protocol.ko.md) — 처리하는 것, 지원하는 MCP 개정, 오류.
- [Contributing](CONTRIBUTING.md), [취약점 제보](SECURITY.md).

## 라이선스

Apache License 2.0입니다. [LICENSE](LICENSE)를 보세요.

## 번역

[`README.md`](README.md)(영문)가 원본이고, 이 문서와 [`README.ja.md`](README.ja.md)가 따릅니다. `docs/` 아래 문서도
영문이 원본이고 `docs/<name>.ko.md`와 `docs/<name>.ja.md`가 따릅니다. 영문 파일이 바뀌면 그 번역 둘을 함께 고칩니다.
