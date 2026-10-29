# Running Hydra locally

The [README](../README.md#quick-start-local) starts one node. This page covers several nodes, how to read the routing logs and the usual laptop pitfalls.

## Another node

**Another node on a different machine** of the same LAN: run the same command; mDNS joins them.

**Another node on the same machine**: it also needs its own ports, otherwise it fails to bind.

```sh
OTEL_SDK_DISABLED=true HYDRA_NODE_NAME=n2 ENVIRONMENT=local \
  BASE_PORT=4128 CONTROL_PORT=9193 HYDRA_GOSSIP_PORT=7947 ./build/darwin/arm64/hydra
```

## Routing decisions

Every request logs a `request completed` line with its `decision`:

| `decision` | Meaning |
|---|---|
| `local` | The entity belongs to the interface that received it; it egresses here. |
| `handoff` | It belongs to another interface of this node; relayed to that interface's port. |
| `peer` | It belongs to another node; relayed to `peer`. On a CONNECT, an empty `peer` means the peer was unreachable and the tunnel fell back to a direct dial. |
| `hop-local` | It arrived relayed from another node or interface; it egresses here. |

Relays target the peer's gossip address with the owning interface's port, so an interface IP the peers cannot reach (a VPN tunnel) or that two hosts share (a VM bridge) does not break routing.

## Pick the interfaces

Laptops expose VM bridges and VPN links that cannot reach the internet. Serve only the ones that can, in the order you want their ports:

```sh
HYDRA_INTERFACES=en0,utun32 ...   # en0 → 3128, utun32 → 3129
```

## macOS firewall

Gossip probes peers over UDP. With the application firewall on, an unlisted binary gets its incoming UDP dropped, and the nodes keep marking each other failed and rejoining every few seconds (`node left` / `node joined` in the logs). Allow the binary on every Mac, and again after each rebuild:

```sh
sudo /usr/libexec/ApplicationFirewall/socketfilterfw --add "$PWD/build/darwin/arm64/hydra"
sudo /usr/libexec/ApplicationFirewall/socketfilterfw --unblockapp "$PWD/build/darwin/arm64/hydra"
```

## Tailscale exit node

An active exit node routes the LAN through the tunnel too, which breaks mDNS and gossip between the machines. Keep LAN access on, and give a static seed so discovery does not depend on mDNS:

```sh
tailscale set --exit-node-allow-lan-access=true
HYDRA_GOSSIP_SEEDS=<other-node-lan-ip>:7946 ...
```
