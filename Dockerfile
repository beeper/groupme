FROM golang:1.27-alpine3.23 AS builder

RUN apk add --no-cache git ca-certificates build-base olm-dev

COPY . /build
WORKDIR /build
ARG COMMIT_HASH=unknown
ENV COMMIT_HASH=${COMMIT_HASH}
RUN ./build.sh

FROM alpine:3.23

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache su-exec ca-certificates olm bash jq yq-go curl

COPY --from=builder /build/mautrix-groupme /usr/bin/mautrix-groupme
COPY --from=builder /build/docker-run.sh /docker-run.sh
VOLUME /data
WORKDIR /data

CMD ["/docker-run.sh"]
