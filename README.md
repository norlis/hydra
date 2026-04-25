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

| Variable | Default | Description |
|---|---|---|
| `ENVIRONMENT` | `development` | `local`, `development`, `production`, `aws`. Drives provider selection. |
| `BASE_PORT` | `3128` | First service port; subsequent NICs get `BASE_PORT + i`. |
| `CONTROL_PORT` | `9090` | HTTP control-plane port. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |

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
|---|---|---|---|
| 7946 | TCP | Hydra SG (self) | memberlist gossip state sync / full TCP push-pull |
| 7946 | UDP | Hydra SG (self) | memberlist gossip heartbeats |
| 3128 | TCP | Client SG | Service port (first NIC). Add +1 per extra NIC. |
| 9090 | TCP | Ops SG | Control-plane HTTP API |

Self-referencing SG rules (Hydra SG → Hydra SG) are the recommended
pattern so nodes gossip only among themselves.

### IMDSv2

No extra configuration is needed. Hydra uses IMDSv2 tokens
(`X-aws-ec2-metadata-token-ttl-seconds: 21600`) and falls back on 404
for optional fields (no public IP, etc.). Ensure IMDSv2 is **enabled**
on the instance; IMDSv1-only instances will fail to start.

---

## HTTP endpoints

The control-plane listens on `CONTROL_PORT` (default `9090`):

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/nodes` | Returns the local node + every peer known to memberlist. Each node carries its full `topology.Node` (interfaces, MAC, ports, last-seen, health) decoded from the peer's gossiped `NodeMeta`. |

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