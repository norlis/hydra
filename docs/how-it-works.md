# How Hydra works

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
| `pkg/logger`, `pkg/env` | `log/slog` logger factory and struct-tag env parser. |

## memberlist (gossip plane)

memberlist implements the **SWIM** gossip protocol. Each node:

- Sends periodic **UDP probes** to random peers. Missed probes trigger indirect pings through other peers before declaring a node dead.
- Piggybacks **metadata** (`NodeMeta`, our `topology.Node`) and app events in those probes, so propagation is O(log N) with negligible bandwidth overhead.
- Runs a **full state push-pull over TCP** periodically for convergence after network partitions.

On startup Hydra calls `memberlist.Create` + `Join(seeds)`. If the first join fails (peers aren't up yet), a background loop retries every `HYDRA_GOSSIP_REJOIN_INTERVAL`. `Join` is idempotent, so re-running it against already-known peers is cheap.

`NodeDelegate` translates memberlist's `NotifyJoin/Leave/Update` callbacks into `bus.ClusterEvent`s on our internal event bus. Other subsystems (e.g. the hash ring) subscribe and mutate their state accordingly.

## Consistent hashing

Given an `entityID` (e.g. a tenant ID, user ID, URL path), we route the request to the node that "owns" that entity. The goals are:

- **Determinism**: the same `entityID` always routes to the same node while the cluster topology is stable.
- **Minimal rebalancing**: adding or removing one node re-routes only ~1/N of keys, not all of them.

The ring (`internal/hash/consistent.go`) uses xxhash and 50 virtual nodes per physical node:

1. For each physical node, insert 50 hashes (`nodeID-0`..`nodeID-49`) into a sorted slice.
2. Each virtual-node hash maps back to the physical address (`IP:Port`).
3. To route `entityID`: compute `hash = xxhash(entityID)`, binary-search the slice for the first virtual node with `hash_v >= hash`. Wrap around to index 0 if none.

Virtual nodes matter: without them, a single physical node covers a contiguous arc on the ring, leading to uneven load. 50 replicas smooth the distribution so each node gets roughly the same fraction of the keyspace.

When gossip reports a `NodeJoined` / `NodeLeft` event, the ring adds or removes the node's 50 vnodes. Routing remains available throughout: the ring uses a `sync.RWMutex`, so reads are non-blocking.
