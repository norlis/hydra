# Hydra

Self-clustering HTTP forward proxy mesh for workloads that need a stable egress per entity: every request carrying the same `X-Entity-ID` (a tenant, an account) leaves through the same node and network interface, whichever node receives it. Nodes discover each other over gossip and route with a consistent hash ring, so adding or removing one moves only ~1/N of the entities.

> **Status:** v2 is in release candidate (`v2.0.0-rc.*`). Runs on Linux and macOS, amd64 and arm64; on AWS (IMDS + Cloud Map discovery), on a LAN (mDNS) or as a single standalone node.

## Requirements

- To run: a release binary or package from [GitHub Releases](https://github.com/norlis/hydra/releases) (see [Deployment](docs/deployment.md)).
- To build: Go 1.27.3 through [mise](https://mise.jdx.dev/); Node.js and npm for `make build` (web UI). Details in [CONTRIBUTING.md](CONTRIBUTING.md).

## Quick start (local)

In `local` mode each node discovers its own network interfaces and announces itself over **mDNS**, so nodes on the same LAN find each other without static seeds. Two variables are enough:

```sh
make build   # or: go build -o build/darwin/arm64/hydra ./cmd/hydra

OTEL_SDK_DISABLED=true ENVIRONMENT=local ./build/darwin/arm64/hydra
```

| Variable | Why |
|---|---|
| `ENVIRONMENT=local` | Host interfaces + mDNS discovery (it is also the default). |
| `OTEL_SDK_DISABLED=true` | No OTLP collector runs locally; without it the exporter keeps failing to push metrics. |

The node takes the machine's hostname as its name. Set `HYDRA_NODE_NAME` only when two nodes would share it: two machines with the same hostname, or two nodes on one machine. Two nodes with the same name cannot both join.

What it opens: one proxy per IPv4 interface starting at `BASE_PORT` (3128, 3129, …; the port picks the egress NIC), the control plane on `9192`, and gossip on `7946` TCP+UDP. macOS may ask to allow incoming connections the first time.

Check it:

```sh
curl -x http://127.0.0.1:3128 https://checkip.amazonaws.com/   # egress IP
curl http://127.0.0.1:9192/api/proxies                          # one entry per node × interface
open http://127.0.0.1:9192/events                               # live topology UI
```

Loopback and private destinations answer `403` by design (SSRF policy). To reach something on your LAN, add `HYDRA_PROXY_ALLOW_CIDR=192.168.0.0/16` (or the range you need).

Requests carrying the same `X-Entity-ID` are routed to the same owner node whichever proxy receives them. For HTTPS the header must travel on the `CONNECT` itself, so use `--proxy-header`; curl's `-H` only reaches the origin, inside the tunnel, and the proxy never sees it:

```sh
curl -x http://127.0.0.1:3128 --proxy-header 'X-Entity-ID: tenant-42' https://checkip.amazonaws.com/
curl -x http://127.0.0.1:3128 -H 'X-Entity-ID: tenant-42' http://checkip.amazonaws.com/    # plain HTTP: -H is enough
```

To see who owns an entity, ask any node; every node answers the same:

```sh
curl -X POST -d '["tenant-42"]' http://127.0.0.1:9192/api/resolve
```

More nodes, routing logs, interface selection and macOS/Tailscale pitfalls: [docs/running-locally.md](docs/running-locally.md).

## Documentation

- [Running locally](docs/running-locally.md): several nodes, routing decisions, laptop pitfalls.
- [Configuration](docs/configuration.md): every environment variable.
- [Deployment](docs/deployment.md): AWS (IAM, Cloud Map, security groups), Debian/RPM packages, runtime and kernel tuning.
- [Observability](docs/observability.md): OTLP exporter settings and metric names.
- [HTTP endpoints](docs/http-api.md): control plane and proxy headers.
- [How it works](docs/how-it-works.md): components, gossip and consistent hashing.

## Contributing

Requirements, commands, local development notes and the release process are in [CONTRIBUTING.md](CONTRIBUTING.md).
