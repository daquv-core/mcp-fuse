# A runnable image: the static binary on a minimal base that has CA certificates (backends are usually https)
# and runs as a non-root user.
#
#   docker build -t mcp-fuse --build-arg VERSION=$(git describe --tags --always) .
#   docker run -v $PWD/config:/etc/mcp-fuse:ro -v mcp-fuse-state:/var/lib/mcp-fuse -p 127.0.0.1:8080:8080 mcp-fuse
#
# `--target release` stops at the binary alone (FROM scratch, one layer, not runnable): the image CI publishes per
# commit, and the file a release carries.
FROM golang:1.26 AS build
WORKDIR /src
COPY . .
ARG VERSION=dev
RUN go test ./... && MCP_FUSE_VERSION="$VERSION" ./build.sh

FROM scratch AS release
COPY --from=build /src/mcp-fuse /mcp-fuse

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /src/mcp-fuse /mcp-fuse
ENTRYPOINT ["/mcp-fuse"]
CMD ["-config", "/etc/mcp-fuse/config.json"]
