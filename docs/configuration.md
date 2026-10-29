# Configuration

Every setting is an environment variable, loaded by `internal/config.go`. Packages installed with the `.deb`/`.rpm` read overrides from `/etc/default/hydra`. The OpenTelemetry exporter is configured with the standard `OTEL_*` variables, listed in [Observability](observability.md).


## Common

| Variable | Default       | Description |
|---|---------------|---|
| `ENVIRONMENT` | `local` | `local`, `aws`, `standalone` (alias `single`). Drives provider selection. |
| `BASE_PORT` | `3128`        | First service port; subsequent NICs get `BASE_PORT + i`. |
| `HYDRA_INTERFACES` | — | `local` only. Comma-separated interface names to serve, e.g. `en0,utun32`. Each gets `BASE_PORT` + its position in the list, even while another listed one is down. Empty serves every up, non-loopback IPv4 interface. |
| `CONTROL_PORT` | `9192`        | HTTP control-plane port. |
| `LOG_LEVEL` | `info`        | `debug`, `info`, `warn`, `error`. |
| `HYDRA_DRAIN_DELAY` | `5s` | How long `/ready` answers `503` before the control plane shuts down, so a load balancer stops routing to the node. |

## Gossip plane

| Variable | Default | Description |
|---|---|---|
| `HYDRA_NODE_NAME` | hostname | Unique node ID in the cluster. Set it when two instances would share a hostname. |
| `HYDRA_GOSSIP_ADDR` | `0.0.0.0` | Bind address for memberlist. |
| `HYDRA_GOSSIP_PORT` | `7946` | Gossip port (UDP + TCP). |
| `HYDRA_GOSSIP_SEEDS` | — | Comma-separated `host:port` list of static seeds. |
| `HYDRA_GOSSIP_REJOIN_INTERVAL` | `15s` | Background auto-join tick; re-resolves seeds and heals split-brains. |
| `HYDRA_GOSSIP_SECRET` | — | Hex-encoded AES key. Must decode to 16/24/32 bytes (AES-128/192/256). Empty disables gossip encryption — peers with different values cannot join. Generate with `openssl rand -hex 16`. |
| `HYDRA_CLUSTER_TAG` | `hydra` | Logical mesh tag; used by discovery providers to filter peers. |

## Proxy

| Variable | Default | Description |
|---|---|---|
| `HYDRA_STRIP_HEADERS` | — | Comma-separated list of request headers to remove before forwarding to the upstream (e.g. `Authorization,Cookie`). Control-plane headers (`X-Hydra-Hop`, `X-Entity-ID`) are always stripped regardless. |
| `HYDRA_PROXY_ALLOWED_PORTS` | `443,80` | Destination ports allowed for `CONNECT`; any other port gets `403` before dialing. Opening more (SMTP, SSH, RDP) turns the proxy into a relay. |
| `HYDRA_PROXY_DENY_CIDR` | — | Comma-separated CIDRs denied on top of the built-in list (RFC 1918, loopback, link-local, IETF reserved, IPv6 embeddings). |
| `HYDRA_PROXY_ALLOW_CIDR` | — | Comma-separated CIDRs that override the built-in deny list, e.g. `192.168.0.0/16` to reach your LAN. |
| `HYDRA_PROXY_MAX_TUNNELS` | `0` | Maximum concurrent `CONNECT` tunnels per node; over the cap answers `503` before dialing. `0` is unlimited. |
| `HYDRA_PROXY_IDLE_TIMEOUT` | `0s` | Closes a tunnel after this long without bytes in either direction. `0s` disables it; pick a value above the longest legitimate idle period (WebSocket keepalives). |
| `HYDRA_PROXY_AUTH_MODE` | `none` | `none` (safe only on a trusted LAN) or `basic` (RFC 7617 `Proxy-Authorization`). Peers relay with the same credentials, so every node must share them. |
| `HYDRA_PROXY_AUTH_USER`, `HYDRA_PROXY_AUTH_PASS` | — | Credentials for `HYDRA_PROXY_AUTH_MODE=basic`. |

## AWS Cloud Map (only when `ENVIRONMENT=aws`)

| Variable | Default | Description |
|---|---|---|
| `HYDRA_CLOUDMAP_REGION` | `us-east-1` | AWS region of the Cloud Map namespace. |
| `HYDRA_CLOUDMAP_NAMESPACE` | — | **Required.** Cloud Map namespace name. |
| `HYDRA_CLOUDMAP_SERVICE` | `hydra` | Cloud Map service name inside the namespace. |
