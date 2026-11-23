# syntax=docker/dockerfile:1.29.0@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183

FROM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS builder
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

FROM docker.io/library/alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
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
