# syntax=docker/dockerfile:1.28.0@sha256:bb22d9815c728170f72750f4e5b0d672e06176142e1d602c7e66c050100b7e5b

FROM golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS builder
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

FROM docker.io/library/alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
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
