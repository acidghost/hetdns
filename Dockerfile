# syntax=docker/dockerfile:1@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89

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

FROM docker.io/library/alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
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
