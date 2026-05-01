# Hydra

Self-clustering proxy mesh. Every node joins a gossip cluster on startup, discovers its peers automatically, and routes requests to the owner of each entity using a consistent hash ring.

---

## Components

| Package | Responsibility |
|---|---|
| `cmd/hydra` | Entrypoint. Boots the fx dependency graph. |
| `internal/application` | Environment-aware factories. Selects `network.Provider` and `cluster.SeedProvider` per env. |
| `internal/cluster` | `Discovery` interface + HashiCorp `memberlist` implementation and seed providers (mDNS, Cloud Map). |
| `internal/network` | `Provider` interface + local and AWS IMDS implementations. `Registry` caches interfaces on a 60s refresh. |
| `internal/hash` | Consistent hash ring over virtual nodes (xxhash, 50 replicas/node). |
| `internal/bus` | In-memory pub/sub for cluster events. |
| `internal/topology` | Shared `Node` / `NetworkInterface` types. |
| `internal/httpapi` | Control-plane HTTP server. |
| `pkg/logger`, `pkg/env` | Zap logger factory and struct-tag env parser. |

---

## Quick start (local)

```sh
go build -o hydra ./cmd/hydra
ENVIRONMENT=local HYDRA_NODE_NAME=node-a ./hydra
# on another terminal (or host)
ENVIRONMENT=local HYDRA_NODE_NAME=node-b HYDRA_GOSSIP_PORT=7947 ./hydra
```

In `local` mode the nodes publish themselves over **mDNS** on the LAN and join each other without any static seeds.

---

## Configuration

### Common

| Variable | Default       | Description |
|---|---------------|---|
| `ENVIRONMENT` | `development` | `local`, `development`, `production`, `aws`. Drives provider selection. |
| `BASE_PORT` | `3128`        | First service port; subsequent NICs get `BASE_PORT + i`. |
| `CONTROL_PORT` | `9192`        | HTTP control-plane port. |
| `LOG_LEVEL` | `info`        | `debug`, `info`, `warn`, `error`. |

### Gossip plane

| Variable | Default | Description |
|---|---|---|
| `HYDRA_NODE_NAME` | `node` | Unique node ID in the cluster. Must differ per instance. |
| `HYDRA_GOSSIP_ADDR` | `0.0.0.0` | Bind address for memberlist. |
| `HYDRA_GOSSIP_PORT` | `7946` | Gossip port (UDP + TCP). |
| `HYDRA_GOSSIP_SEEDS` | — | Comma-separated `host:port` list of static seeds. |
| `HYDRA_GOSSIP_REJOIN_INTERVAL` | `15s` | Background auto-join tick; re-resolves seeds and heals split-brains. |
| `HYDRA_GOSSIP_SECRET` | — | Hex-encoded AES key. Must decode to 16/24/32 bytes (AES-128/192/256). Empty disables gossip encryption — peers with different values cannot join. Generate with `openssl rand -hex 16`. |
| `HYDRA_CLUSTER_TAG` | `hydra` | Logical mesh tag; used by discovery providers to filter peers. |

### Proxy

| Variable | Default | Description |
|---|---|---|
| `HYDRA_STRIP_HEADERS` | — | Comma-separated list of request headers to remove before forwarding to the upstream (e.g. `Authorization,Cookie`). Control-plane headers (`X-Hydra-Hop`, `X-Entity-ID`) are always stripped regardless. |

### AWS Cloud Map (only when `ENVIRONMENT=aws`)

| Variable | Default | Description |
|---|---|---|
| `HYDRA_CLOUDMAP_REGION` | `us-east-1` | AWS region of the Cloud Map namespace. |
| `HYDRA_CLOUDMAP_NAMESPACE` | — | **Required.** Cloud Map namespace name. |
| `HYDRA_CLOUDMAP_SERVICE` | `hydra` | Cloud Map service name inside the namespace. |

---

## Deployment on AWS

Set `ENVIRONMENT=aws`. The node will:

1. Query **IMDSv2** to learn its own ENIs (private IP, MAC, subnet).
2. Register itself in **Cloud Map** with `AWS_INSTANCE_IPV4` and
   `AWS_INSTANCE_PORT`.
3. List the Cloud Map service to discover peer IPs and `Join` them.
4. Gossip heartbeats + topology over memberlist from then on.
5. Deregister from Cloud Map on graceful shutdown.

**Set `HYDRA_GOSSIP_SECRET` in production** (same value on every peer)
so the gossip channel is AES-encrypted and only authorized nodes can
join. Without it, anything that can reach port `7946/udp` in the VPC
can join the cluster and hijack the hash ring. Store the key in AWS
Secrets Manager or SSM Parameter Store and inject it at startup.

### IAM policy

Attach to the EC2 instance profile:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": [
      "servicediscovery:ListNamespaces",
      "servicediscovery:ListServices",
      "servicediscovery:RegisterInstance",
      "servicediscovery:DeregisterInstance",
      "servicediscovery:DiscoverInstances"
    ],
    "Resource": "*"
  }]
}
```

### Cloud Map namespace

- Type: **HTTP** (not DNS). Hydra reads instance attributes, not DNS records.
- Service name must match `HYDRA_CLOUDMAP_SERVICE`.
- No custom health check is required; memberlist owns liveness detection.

### Security group (inbound)

| Port | Proto | Source | Purpose |
|------|---|---|---|
| 7946 | TCP | Hydra SG (self) | memberlist gossip state sync / full TCP push-pull |
| 7946 | UDP | Hydra SG (self) | memberlist gossip heartbeats |
| 3128 | TCP | Client SG | Service port (first NIC). Add +1 per extra NIC. |
| 9192 | TCP | Ops SG | Control-plane HTTP API |

Self-referencing SG rules (Hydra SG → Hydra SG) are the recommended
pattern so nodes gossip only among themselves.

### IMDSv2

No extra configuration is needed. Hydra uses IMDSv2 tokens
(`X-aws-ec2-metadata-token-ttl-seconds: 21600`) and falls back on 404
for optional fields (no public IP, etc.). Ensure IMDSv2 is **enabled**
on the instance; IMDSv1-only instances will fail to start.

---

## Runtime tuning

These knobs shape how the Go runtime and the kernel behave under load.
None of them are strictly required to run Hydra, but they are the
difference between a node that survives a traffic spike and a node
that gets OOM-killed or thrashes.

### `GOMAXPROCS` — leave it alone

Hydra targets **Go 1.26**, where the runtime reads the cgroup CPU quota
on Linux containers and sets `GOMAXPROCS` automatically. Vendoring
`go.uber.org/automaxprocs` is therefore intentionally **not** done — it
would only matter on Go ≤ 1.24.

If you are running outside a container and want to override (e.g. pin
to physical cores on a bare-metal host), set the environment variable
explicitly:

```sh
GOMAXPROCS=8 hydra
```

### `GOMEMLIMIT` — set it on every container

Set `GOMEMLIMIT` to roughly **90% of the container memory limit**. This
gives the GC a soft budget to pace against, which dramatically reduces
the chance of an OOM kill under bursty allocation. Example for a 4 GiB
container:

```sh
GOMEMLIMIT=3600MiB hydra
```

The 10% headroom covers off-heap allocations (cgo, mmap, kernel TCP
buffers — Hydra holds two TCP sockets per active CONNECT tunnel). Tune
down further if `pprof` shows large RSS coming from network buffers
under your peak load.

`GOGC` can stay at its default (`100`); only lower it (`50`–`75`) if a
GC profile shows runaway heap growth between collections.

### Sysctls (Linux hosts running Hydra)

services & logs

```shell
yum install -y https://github.com/norlis/hydra/releases/download/v1.0.0-beta.6/hydra_1.0.0-beta.6_linux_arm64.rpm

systemctl start hydra

systemctl enable hydra

# logs
systemctl status hydra

# or 
journalctl -u hydra -f
```

testing

```shell
NODE1=172.18.1.152
NODE2=172.18.1.215

# Verify affinity: same entity through different nodes must return the same exit IP
curl --proxy http://$NODE1:3128 --proxy-header "X-Entity-ID: org-123" https://checkip.amazonaws.com/
curl --proxy http://$NODE2:3128 --proxy-header "X-Entity-ID: org-123" https://checkip.amazonaws.com/

# Different entity → different exit IP
curl --proxy http://$NODE1:3128 --proxy-header "X-Entity-ID: org-456" https://checkip.amazonaws.com/
curl --proxy http://$NODE2:3128 --proxy-header "X-Entity-ID: org-456122" https://checkip.amazonaws.com/
```


The proxy is a long-lived TCP server with high connection turnover.
Crank these once on the host (or via a `DaemonSet`/userdata) to avoid
hitting kernel-side limits before Go-side ones:

```sh
# More queued SYNs than the 128 default.
sysctl -w net.core.somaxconn=4096
sysctl -w net.ipv4.tcp_max_syn_backlog=4096

# Don't cap us at the per-process default fd ceiling.
ulimit -n 1048576

# Conntrack table — only needed if the host runs iptables/nf_conntrack
# in front of Hydra (most do). Default 65k fills up fast under DDoS or
# scrape storms.
sysctl -w net.netfilter.nf_conntrack_max=1048576
```

### TCP keepalive

The proxy data-plane sets keepalive on every accepted conn via
`net.ListenConfig.KeepAliveConfig` (Go 1.23+): probe after 30s idle,
then every 10s, drop after 3 misses (~60s to detect a dead client).
This is in code and does not need any env var.

### `MaxHeaderBytes`

Capped at 16 KiB on both the proxy data-plane and the control-plane
HTTP servers. Hardcoded — there is no env knob, and there is no good
reason to raise it.

---

## Observability

Hydra emits **OpenTelemetry** metrics over **OTLP/HTTP** to a
collector you run alongside the fleet (e.g. the OpenTelemetry
Collector). Re-exposing as Prometheus, Datadog, Cloud Watch, etc. is a
collector pipeline concern and lives outside the binary — the node
itself does not serve `/metrics`.

Configure the exporter through the standard OTel environment
variables (no Hydra-specific knobs):

| Variable | Default | Description |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://localhost:4318` | Base URL of the OTLP/HTTP receiver. |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | — | Override for the metrics signal only. |
| `OTEL_EXPORTER_OTLP_HEADERS` | — | Comma-separated `k=v` pairs; use for auth tokens. |
| `OTEL_METRIC_EXPORT_INTERVAL` | `60000` (ms) | Push cadence. |
| `OTEL_RESOURCE_ATTRIBUTES` | — | Extra resource labels (`env=prod,region=us-east-1`). |
| `OTEL_SDK_DISABLED` | `false` | `true` turns the SDK into a no-op (useful for tests). |

`service.name=hydra`, `service.version=<git hash>`, and
`service.instance.id=<HYDRA_NODE_NAME>` are injected automatically;
the resource detector also adds host/process attributes.

Bundled metric names (all dotted, prefixed `hydra.proxy.*`):
`active_tunnels`, `connect_attempts{result}`, `connect_denied{reason}`,
`connect_errors{stage}`, `bytes{direction}`, `requests{method,status_class,decision}`,
`connect_setup_seconds`, `tunnel_seconds`. Go runtime metrics (heap,
GC, goroutines) are emitted by the OTel runtime instrumentation under
the standard `process.runtime.go.*` namespace.

---

## HTTP endpoints

The control-plane listens on `CONTROL_PORT` (default `9192`):

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/nodes` | Returns the local node + every peer known to memberlist. Each node carries its full `topology.Node` (interfaces, MAC, ports, last-seen, health) decoded from the peer's gossiped `NodeMeta`. |
| `GET` | `/healthz` | Liveness probe. Returns `200 ok` while the process is running. |
| `*` | `/debug/pprof/*` | Standard `net/http/pprof` handlers (`profile`, `heap`, `goroutine`, `trace`, …). Bind the control port to a private interface or fence with a security group — pprof exposes process internals. |

The forward proxy data-plane listens on `BASE_PORT` (+1 per extra NIC). It
accepts standard HTTP forward-proxy and CONNECT requests, honoring these
request headers:

| Header | Purpose |
|---|---|
| `X-Entity-ID` | Opaque identifier the router hashes to pick an owner in the consistent hash ring. If absent, the request is always processed locally. |
| `X-Hydra-Hop` | Set internally to `1` when a peer forwards a request; receivers treat it as "do not re-route" to avoid ping-pong loops. Never set this from a client. |

---

## How it works

### memberlist (gossip plane)

memberlist implements the **SWIM** gossip protocol. Each node:

- Sends periodic **UDP probes** to random peers. Missed probes trigger
  indirect pings through other peers before declaring a node dead.
- Piggybacks **metadata** (`NodeMeta`, our `topology.Node`) and app
  events in those probes, so propagation is O(log N) with negligible
  bandwidth overhead.
- Runs a **full state push-pull over TCP** periodically for
  convergence after network partitions.

On startup Hydra calls `memberlist.Create` + `Join(seeds)`. If the
first join fails (peers aren't up yet), a background loop retries
every `HYDRA_GOSSIP_REJOIN_INTERVAL`. `Join` is idempotent, so
re-running it against already-known peers is cheap.

`NodeDelegate` translates memberlist's `NotifyJoin/Leave/Update`
callbacks into `bus.ClusterEvent`s on our internal event bus. Other
subsystems (e.g. the hash ring) subscribe and mutate their state
accordingly.

### Consistent hashing

Given an `entityID` (e.g. a tenant ID, user ID, URL path), we route
the request to the node that "owns" that entity. The goals are:

- **Determinism**: the same `entityID` always routes to the same node
  while the cluster topology is stable.
- **Minimal rebalancing**: adding or removing one node re-routes only
  ~1/N of keys, not all of them.

The ring (`internal/hash/consistent.go`) uses xxhash and 50 virtual
nodes per physical node:

1. For each physical node, insert 50 hashes (`nodeID-0`..`nodeID-49`)
   into a sorted slice.
2. Each virtual-node hash maps back to the physical address (`IP:Port`).
3. To route `entityID`: compute `hash = xxhash(entityID)`, binary-search
   the slice for the first virtual node with `hash_v >= hash`. Wrap
   around to index 0 if none.

Virtual nodes matter: without them, a single physical node covers a
contiguous arc on the ring, leading to uneven load. 50 replicas
smooth the distribution so each node gets roughly the same fraction
of the keyspace.

When gossip reports a `NodeJoined` / `NodeLeft` event, the ring adds
or removes the node's 50 vnodes. Routing remains available throughout:
the ring uses a `sync.RWMutex`, so reads are non-blocking.

---

## Local development notes

- `ENVIRONMENT=local` uses `memberlist.DefaultLocalConfig` (tighter
  timeouts than LAN) and enables mDNS for seed discovery.
- Running multiple nodes on the same host: give each a different
  `HYDRA_GOSSIP_PORT`, `BASE_PORT`, `CONTROL_PORT`.
- Logs are JSON on stderr by default. Pipe through `jq` for readability.