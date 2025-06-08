FROM --platform=$BUILDPLATFORM golang:1-alpine AS builder
WORKDIR /work
ENV CGO_ENABLED=0
ARG BINARY

RUN apk add --update --no-cache git

COPY ./go.* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -o ./app -ldflags "-s -w" .

FROM alpine:3 AS base
WORKDIR /work

COPY --from=builder /work/app ./
ENTRYPOINT ["/work/app"]
