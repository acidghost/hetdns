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

A secure singleton Kustomize base is in [`deploy/kubernetes`](deploy/kubernetes). It uses a
mounted Secret token, read-only filesystems, no service-account token, dropped capabilities,
non-root UID 65532, health probes, and `Recreate` deployment strategy.

```sh
kubectl create secret generic hetdns-token --from-literal=token='YOUR_TOKEN'
# Edit deploy/kubernetes/configmap.yaml first.
kubectl apply -k deploy/kubernetes
kubectl port-forward service/hetdns 8080:8080
```

Do not scale beyond one replica without external leader election.

## License

[Unlicense](UNLICENSE)
