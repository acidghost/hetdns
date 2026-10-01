# syntax=docker/dockerfile:1.27.0@sha256:bde3983e9c939224420ddaf6b784cc30e09b035a4dea01f581230c50809f372e

FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS builder
RUN apk add --no-cache just
WORKDIR /src
ENV GOFLAGS=-mod=vendor
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY cmd ./cmd
COPY internal ./internal
COPY justfile ./
ARG BUILD_VERSION=0.0.0
ARG BUILD_COMMIT=unknown
ARG TARGETOS=linux
ARG TARGETARCH
RUN just version="${BUILD_VERSION}" commit_sha="${BUILD_COMMIT}" build "${TARGETOS}" "${TARGETARCH}" \
 && mv "build/hetdns-${TARGETOS}-${TARGETARCH}" /usr/local/bin/hetdns

FROM docker.io/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates curl
ARG BUILD_VERSION=0.0.0
ARG BUILD_COMMIT=unknown
LABEL org.opencontainers.image.title="hetdns" \
      org.opencontainers.image.description="Hetzner Cloud dynamic DNS reconciler" \
      org.opencontainers.image.source="https://github.com/acidghost/hetdns" \
      org.opencontainers.image.version="${BUILD_VERSION}" \
      org.opencontainers.image.revision="${BUILD_COMMIT}"
COPY --from=builder /usr/local/bin/hetdns /usr/local/bin/hetdns
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/hetdns"]
