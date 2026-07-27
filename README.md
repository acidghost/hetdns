# hetdns

`hetdns` is a small, stateless Hetzner Cloud dynamic DNS reconciler. It discovers public
IPv4/IPv6 addresses from HTTPS or direct external commands, reconciles authoritative `A` and
`AAAA` RRsets, and serves a read-only status UI and JSON API.

It uses the current Hetzner Cloud API (`api.hetzner.cloud/v1`), not the legacy DNS Console
API.

## Build

Go 1.25 or newer and [`just`](https://just.systems/) are required.

```sh
just test
just build
./build/hetdns-$(go env GOOS)-$(go env GOARCH) -version
```

Build the container with `just build-image`. The runtime image is pinned Alpine and includes
CA certificates and `curl` for command sources. It runs as UID/GID 65532.

## Configuration

Copy [`config.example.json`](config.example.json). Configuration is strict JSON: unknown and
duplicate fields are rejected.

```json
{
  "sources": {
    "wan-v4": {
      "type": "http",
      "family": "ipv4",
      "url": "https://ip.me",
      "timeout": "10s",
      "user_agent": ""
    }
  },
  "records": {
    "home-v4": {
      "zone": "example.com",
      "name": "home",
      "type": "A",
      "source": "wan-v4",
      "ttl": 300
    }
  }
}
```

Source types:

- `http`: requires HTTPS unless `allow_insecure_http` is explicitly true. Private/loopback
  destinations are blocked unless `allow_private_networks` is true. Requests use
  `hetdns/<version>` as their User-Agent by default. Set `user_agent` to a custom value, or to
  an empty string to send no User-Agent (required by `ip.me` for its plain-text response).
- `command`: `argv` is executed directly without a shell; `argv[0]` must be absolute. Commands
  receive no service environment or token.

Both source types require exactly one address as output. Private, loopback, link-local,
multicast, and unspecified results are rejected unless `allow_non_public` is true.

Record options:

- `name` is relative to `zone`; use `@` for the apex.
- `type` is `A` or `AAAA` and must match the source family.
- `ttl` is optional and used only when creating an RRset.
- `create` defaults to `true`.
- `replace_all` defaults to `false`; multi-value RRsets are protected from destructive
  replacement.

Validate configuration and the token without network requests:

```sh
HETDNS_HETZNER_TOKEN='…' just run -config config.example.json -check
```

Prefer a token file in production:

```sh
just run -config ./config.json -token-file ./hetzner-token
```

The token needs DNS read/write permission for the configured zones.

## Runtime options

Flags take precedence over environment variables.

| Flag | Environment | Default |
|---|---|---|
| `-config` | `HETDNS_CONFIG` | `/etc/hetdns/config.json` |
| `-token-file` | `HETDNS_HETZNER_TOKEN_FILE` | unset |
| — | `HETDNS_HETZNER_TOKEN` | unset fallback |
| `-interval` | `HETDNS_INTERVAL` | `5m` |
| `-listen` | `HETDNS_LISTEN` | `:8080` |
| `-log-level` | `HETDNS_LOG_LEVEL` | `info` |
| `-log-format` | `HETDNS_LOG_FORMAT` | `json` |
| `-history-limit` | `HETDNS_HISTORY_LIMIT` | `100` |

The minimum interval is one minute. Reconciliation starts immediately, never overlaps, and
uses bounded jitter and per-record backoff.

## HTTP endpoints

- `/` — server-rendered status UI
- `/api/v1/status` — JSON status snapshot
- `/healthz` — process liveness
- `/readyz` — initialization and scheduler readiness

Every endpoint is GET-only. There is no update action and no Prometheus endpoint. The UI
contains operational hostnames and addresses: keep it on a trusted network or put
authentication and TLS in an ingress/service mesh.

## OpenTelemetry

Metrics are exported with OTLP/HTTP Protobuf when `OTEL_EXPORTER_OTLP_ENDPOINT` or
`OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` is set. Standard OpenTelemetry environment variables
configure endpoint, headers, TLS, compression, timeout, resource attributes, and export
interval. Set `OTEL_SDK_DISABLED=true` to disable the SDK. Only `http/protobuf` is supported
in v1.

Exporter failures do not affect reconciliation or readiness.

## Kubernetes

### Install the OCI Helm chart

Install an exact chart version and supply configuration from a local JSON file:

```sh
VERSION=0.1.0

kubectl create secret generic hetdns-token \
  --from-literal=token='YOUR_HETZNER_CLOUD_TOKEN'

helm upgrade --install hetdns \
  oci://ghcr.io/acidghost/charts/hetdns \
  --version "$VERSION" \
  --set-file config.raw=./config.json
```

The chart does not create or store the Hetzner token. It mounts only the configured key from
`hetdns-token`. Its bundled `example.com` configuration is safe as an example but must be
replaced for real use.

Important values:

| Value | Default | Purpose |
|---|---|---|
| `image.repository` | `ghcr.io/acidghost/hetdns` | Released image repository |
| `image.tag` | empty | Defaults to the chart's immutable `appVersion` |
| `image.digest` | empty | Overrides the tag with `repository@digest` |
| `config.raw` | safe `example.com` JSON | Contents of the managed `config.json` |
| `config.existingConfigMap` | empty | Use externally managed configuration instead |
| `config.existingConfigMapKey` | `config.json` | Key in the external ConfigMap |
| `tokenSecret.name` / `tokenSecret.key` | `hetdns-token` / `token` | Existing token Secret reference |
| `runtime.*` | application defaults | Interval, listener, logging, and history settings |
| `extraEnv` | `[]` | Additional environment, including OpenTelemetry variables |
| `ingress.enabled` | `false` | Create an Ingress for the status UI |

To upgrade, select another exact chart version. Its `appVersion` selects the matching image,
which causes a normal Deployment rollout without a manual restart:

```sh
VERSION=0.1.1
helm upgrade hetdns oci://ghcr.io/acidghost/charts/hetdns \
  --version "$VERSION" \
  --reuse-values
```

`hetdns` is a singleton; the chart rejects `replicaCount` greater than one. The status UI
contains operational hostnames and addresses. Protect any exposed Ingress with authentication
and TLS.

A secure singleton Kustomize base remains available in
[`deploy/kubernetes`](deploy/kubernetes):

```sh
# Edit deploy/kubernetes/configmap.yaml first.
kubectl apply -k deploy/kubernetes
kubectl port-forward service/hetdns 8080:8080
```

### Develop and validate the chart

Helm, kubeconform, and actionlint are pinned by [`mise.toml`](mise.toml):

```sh
mise install
just helm-lint
just helm-conform
just helm-package version=0.1.0
helm show chart build/hetdns-0.1.0.tgz
```

The conform recipe checks both the default render and ingress/external-ConfigMap options.

## Releases and supply-chain verification

A signed annotated Git tag is the release source of truth. Tags must be strict SemVer with a
leading `v`; prereleases such as `v0.2.0-rc.1` are supported. Merge the intended application
and chart changes to `main` and wait for required CI before tagging.

```sh
git tag -s v0.1.0 -m 'hetdns v0.1.0'
git push origin v0.1.0
```

The tag publishes only these matching, exact versions:

- `ghcr.io/acidghost/hetdns:0.1.0`
- `oci://ghcr.io/acidghost/charts/hetdns --version 0.1.0`
- GitHub Release `v0.1.0`, with `hetdns-0.1.0.tgz` attached

Release tags and OCI artifacts are immutable. There is no `latest`, major, or minor alias.
Repository administrators must protect `v*` tags from updates and deletion with a GitHub tag
ruleset. If a release stops after publishing one artifact, verify its commit and digest, then
complete/sign the missing artifact manually; never delete, move, or overwrite the published
version.

Released images are multi-platform (`linux/amd64` and `linux/arm64`) and are signed by their
immutable index digest with GitHub Actions keyless signing. Obtain the digest from GHCR, then
verify both the issuer and this workflow's certificate identity:

```sh
IMAGE=ghcr.io/acidghost/hetdns@sha256:RELEASE_DIGEST

cosign verify \
  --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
  --certificate-identity-regexp='^https://github.com/acidghost/hetdns/.github/workflows/publish-release.yaml@refs/tags/v' \
  "$IMAGE"
```

BuildKit provenance (`mode=max`) and SPDX SBOM attestations are attached to the image in GHCR,
not merely retained as workflow files. Discover and download their decoded content with:

```sh
docker buildx imagetools inspect "$IMAGE"
docker buildx imagetools inspect "$IMAGE" \
  --format '{{ json .Provenance }}' > provenance.json
docker buildx imagetools inspect "$IMAGE" \
  --format '{{ json .SBOM }}' > sbom.json
jq . provenance.json
jq . sbom.json
```

Check provenance for `https://github.com/acidghost/hetdns`, the release tag/version, expected
source commit SHA, GitHub Actions/BuildKit builder identity, both target platforms, and build
parameters. The SBOM output must be non-empty SPDX data for each platform.

## License

[Unlicense](UNLICENSE)
