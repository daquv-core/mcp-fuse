#!/usr/bin/env bash
# Build mcp-fuse.
#
#   ./build.sh   -> ./mcp-fuse
#
# The version is MCP_FUSE_VERSION when set (a build without .git, such as the Docker build), otherwise
# git describe --tags --always --dirty.
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${MCP_FUSE_VERSION:-$(git describe --tags --always --dirty)}"

# gofmt and vet gate the build itself, so an unformatted release cannot be published by forgetting a habit.
UNFMT="$(gofmt -l .)"
if [ -n "$UNFMT" ]; then
  echo "refusing: not gofmt-clean:" >&2; echo "$UNFMT" >&2; exit 1
fi
go vet ./... || exit 1

# Static, so the box needs no toolchain and no libc match.
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o mcp-fuse ./cmd/mcp-fuse
echo "built ${VERSION} -> ./mcp-fuse"
