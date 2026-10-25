#!/bin/sh
# Development check: vet, tests (with the race detector) and a build of the bridge.
# The add-on itself compiles the bridge from vector/bridge in its Dockerfile.
set -e
cd "$(dirname "$0")/vector/bridge"
go vet ./...
go test -race ./...
go build -o /dev/null ./cmd/vector-bridge
echo "vector-bridge OK"
