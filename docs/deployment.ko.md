# Deployment

[English](deployment.md) | **한국어** | [日本語](deployment.ja.md)

mcp-fuse는 정적 바이너리 하나(또는 컨테이너 하나), JSON 파일 하나, state 파일 하나입니다. 데이터베이스는 필요
없습니다. **인스턴스는 하나만** 실행합니다.

```
agent ──TLS──> load balancer / reverse proxy ──HTTP──> mcp-fuse (127.0.0.1:8080) ──HTTP(S)──> backends
person's browser ───────────────────────────────────────────────────────────────> backends' authorization servers
```

## 릴리스

GitHub의 각 릴리스 `vX.Y.Z`에는 정적 바이너리 `mcp-fuse-linux-amd64`와 `SHA256SUMS`가 들어 있습니다.

```sh
sha256sum -c SHA256SUMS
chmod +x mcp-fuse-linux-amd64 && ./mcp-fuse-linux-amd64 -version
```

릴리스는 아무것도 다시 빌드하지 않습니다. `main`의 모든 커밋은 빌드·테스트되어
`ghcr.io/daquv-core/mcp-fuse:<commit>`로 게시됩니다. 바이너리만 든 이미지(`FROM scratch`, 실행할 수 없음)입니다.
릴리스는 태그가 가리키는 커밋의 이미지에서 꺼낸 바이너리를 담습니다. 그래서 `-version`은 태그가 아니라
릴리스가 만들어진 커밋을 출력하며, 릴리스 노트에 그 커밋이 적혀 있습니다.

## 빌드

```sh
./build.sh          # gofmt check, go vet, then a static binary: ./mcp-fuse
./mcp-fuse -version
```

Go 1.22 이상이 필요합니다. 표준 라이브러리 외의 의존성은 없습니다. `go test -race ./...`로 테스트를 실행합니다.

컨테이너 이미지:

```sh
docker build -t mcp-fuse --build-arg VERSION=$(git describe --tags --always) .
docker run -v "$PWD/config:/etc/mcp-fuse:ro" -v mcp-fuse-state:/var/lib/mcp-fuse \
           -p 127.0.0.1:8080:8080 mcp-fuse
```

이미지는 root가 아닌 사용자(uid 65532)로 실행되므로 state 볼륨은 그 사용자가 쓸 수 있어야 합니다. https
backend에 필요한 CA 인증서를 포함한 최소 이미지를 기반으로 합니다.

## 파일

| 파일 | 내용 | 권한 |
|---|---|---|
| `config.json` | 설정 | 서비스 사용자가 읽을 수 있을 것 |
| `secret_file` | 무작위 32바이트 이상 | 서비스 사용자만 |
| `auth.client_secret_file`, `backends[].client_secret_file` | OAuth client secret | 서비스 사용자만 |
| `state_file` | 암호화된 grant | `0600`으로 생성됨. 디렉터리는 쓸 수 있어야 함 |

secret은 예를 들어 `head -c 48 /dev/urandom | base64 > /etc/mcp-fuse/secret`으로 만듭니다. 바꾸면 기존 state
파일을 읽을 수 없게 되어 모두가 다시 연결해야 합니다.

[`examples/mcp-fuse.service`](../examples/mcp-fuse.service)는 보안을 강화한 systemd unit입니다. 전용 사용자
(`useradd --system --no-create-home --shell /usr/sbin/nologin mcp-fuse`)로 실행하고 state 파일은
`/var/lib/mcp-fuse`에 둡니다.

## 리버스 프록시

[`examples/nginx.conf`](../examples/nginx.conf)가 동작하는 설정입니다. 무엇을 쓰든 중요한 것은 다음입니다.

- mcp-fuse로 **경로 두 개를 라우팅**합니다. `resource`의 경로(사람이 브라우저에서 여는 `<path>/connect/...` 페이지도
  포함)와 `/.well-known/oauth-protected-resource`입니다.
- **응답을 버퍼링하지 않습니다.** 도구의 응답이 server-sent event 스트림일 수 있습니다.
- **긴 호출을 허용합니다.** 도구 호출이 몇 분 걸릴 수 있으니 read timeout을 그에 맞추고, 앞단 load balancer가
  유휴 연결을 더 일찍 끊지 않게 합니다.
- **헤더를 그대로 통과시킵니다.** `Authorization`과 `MCP-Protocol-Version`이 도착해야 하고, 연결 페이지가 쓰는
  cookie도 마찬가지입니다. `X-Forwarded-*`를 설정하는 일은 프록시와 mcp-fuse 사이인 여기에서 합니다. mcp-fuse는
  그것을 다시 전달하지 않습니다.
- `POST`를 허용합니다. mcp-fuse는 MCP 경로의 `GET`과 `DELETE`에 `405`로 답합니다.
- 에이전트가 합쳐진 엔드포인트만 봐야 한다면 backend를 따로 노출하지 않습니다.

## 운영

- **상태 확인:** `GET /healthz`가 `{"status":"ok","version":"…"}`를 돌려줍니다. backend를 확인하지는 않습니다.
- **로그**는 표준 출력의 JSON 라인입니다. `msg`, `level`, 호출에는 `method`, `tool`, `backend`, `status`, `ms`,
  `request_id`가 들어갑니다. backend 실패와 backend 이름 충돌은 `WARN`으로 기록됩니다.
- **중지:** `SIGTERM`을 받으면 새 요청을 받지 않고 진행 중인 호출에 최대 30초를 줍니다.
- **업그레이드:** 바이너리를 바꾸고 재기동합니다. grant는 유지됩니다(state 파일에 있습니다). backend 세션은 필요할
  때 다시 열리고, 진행 중이던 연결은 처음부터 다시 해야 합니다.
- **백업:** state 파일과 secret 파일을 함께. 하나만으로는 쓸모가 없습니다.
- **backend 하나가 내려가면** 응답할 때까지 그 도구만 목록에서 빠집니다.

## mcp-fuse를 다른 곳에 등록하기

사람이 backend를 연결하려면 등록 두 가지가 있어야 합니다(자세한 내용은 [authorization](authorization.ko.md)).

1. 에이전트가 로그인하는 인가 서버에: `auth.client_id` client. redirect URI는 `<resource>/connect/identify`이고,
   mcp-fuse를 위해 발급된 token을 introspect할 수 있어야 합니다.
2. 각 backend의 인가 서버에: mcp-fuse를 client로. redirect URI는 `<resource>/connect/callback`입니다(서버가 동적
   등록을 지원하면 스스로 등록하게 해도 됩니다).
