# The release artifact: the static mcp-fuse binary as the only layer of an image nothing runs.
# CI builds it on every push to main and publishes ghcr.io/daquv-core/mcp-fuse:<sha>;
# control-plane pulls it and copies the binary out of the layer.
FROM golang:1.26 AS build
WORKDIR /src
COPY . .
ARG MCP_FUSE_VERSION
RUN test -n "$MCP_FUSE_VERSION" || { echo "MCP_FUSE_VERSION is required" >&2; exit 1; }
RUN go test ./... && MCP_FUSE_VERSION="$MCP_FUSE_VERSION" ./build.sh

FROM scratch
COPY --from=build /src/mcp-fuse /mcp-fuse
