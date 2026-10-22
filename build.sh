#!/bin/sh
MAUTRIX_VERSION=$(cat go.mod | grep 'maunium.net/go/mautrix ' | awk '{ print $2 }' | head -n1)
if [ -n "${SOURCE_DATE_EPOCH:-}" ]; then
	BUILD_TIME=$(date -u -Iseconds -d "@$SOURCE_DATE_EPOCH")
else
	BUILD_TIME=$(date -Iseconds)
fi
GO_LDFLAGS="-s -w -X main.Tag=$(git describe --exact-match --tags 2>/dev/null) -X main.Commit=$(git rev-parse HEAD) -X 'main.BuildTime=$BUILD_TIME' -X 'maunium.net/go/mautrix.GoModVersion=$MAUTRIX_VERSION'"
go build -ldflags="$GO_LDFLAGS" ./cmd/matrix-line "$@"
