#!/bin/sh
# Builds the mautrix-groupme binary from ./cmd/mautrix-groupme.
#
# By default this links against libolm via cgo (requires libolm headers,
# e.g. `apt install libolm-dev` or `apk add olm-dev`). Set BUILD_TAGS=goolm
# to use mautrix-go's pure-Go olm implementation instead, which doesn't
# require cgo or libolm (useful for quick local builds/sanity checks).
GIT_TAG=$(git describe --exact-match --tags 2>/dev/null || echo unknown)
GIT_COMMIT=${COMMIT_HASH:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}
go build -tags "${BUILD_TAGS:-}" -o mautrix-groupme -ldflags "-X main.Tag=$GIT_TAG -X main.Commit=$GIT_COMMIT -X 'main.BuildTime=`date '+%b %_d %Y, %H:%M:%S'`'" ./cmd/mautrix-groupme "$@"
