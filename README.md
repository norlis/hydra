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
| `HYDRA_CLUSTER_TAG` | `hydra` | Logical mesh tag; used by discovery providers to filter peers. |

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

All endpoints go through the control-plane server on `CONTROL_PORT`.

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/nodes` | Returns the local node + every peer known to memberlist. Each node carries its full `topology.Node` (interfaces, MAC, ports, last-seen, health) decoded from the peer's gossiped `NodeMeta`. |

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