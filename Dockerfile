# syntax=docker/dockerfile:1.22

ARG DOCKER_HUB="docker.io"

FROM ${DOCKER_HUB}/golang:1.26-alpine3.23 AS builder

RUN apk add --no-cache git build-base olm-dev

WORKDIR /build
ENV GOPATH=/go \
    GOMODCACHE=/go/pkg/mod \
    GOCACHE=/root/.cache/go-build

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY pkg ./pkg
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o matrix-line ./cmd/matrix-line

FROM ${DOCKER_HUB}/alpine:3.23

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache ffmpeg su-exec ca-certificates bash jq curl yq-go olm

COPY --from=builder /build/matrix-line /usr/bin/matrix-line
COPY ./docker-run.sh /docker-run.sh
RUN chmod +x /docker-run.sh
ENV BRIDGEV2=1
WORKDIR /data

CMD ["/docker-run.sh"]
