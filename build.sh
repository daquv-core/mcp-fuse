#!/usr/bin/env bash
# Build mcp-fuse.
#
# Releases are CI's: every push to main runs this script inside deploy/release/Dockerfile with
# MCP_FUSE_VERSION=<commit sha> and publishes ghcr.io/daquv-core/mcp-fuse:<sha>. Publishing is not
# deploying: control-plane deploys a sha when it pins it (docs/cp_share/contract.md).
#
#   ./build.sh   -> ./mcp-fuse (a local build)
#
# The version is MCP_FUSE_VERSION when set (CI, whose build context has no .git), otherwise
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
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o mcp-fuse .
echo "built ${VERSION} -> ./mcp-fuse"
