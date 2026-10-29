# Contributing

## Requirements

- [mise](https://mise.jdx.dev/): pins Go 1.27.3 (`mise.toml`). Run Go and `make` through `mise exec --`.
- Node.js and npm: `make build` rebuilds the web UI (`frontend/` → `web/assets/`, embedded in the binary). `go build ./cmd/hydra` alone reuses the committed assets.
- Docker, only for the local observability stack.

## Commands

```bash
mise exec -- make tools          # dev tools into ./bin/ from tools/go.mod
mise exec -- make all            # check-format lint test build
mise exec -- make test           # go test ./... --cover
mise exec -- make lint           # golangci-lint run --fix
mise exec -- make vulncheck      # govulncheck ./...
mise exec -- make docs           # regenerate the Swagger spec (swag)
mise exec -- go test ./internal/hash/... -run TestConsistentHash -v   # a single test
```

## Local development notes

- `ENVIRONMENT=local` uses `memberlist.DefaultLocalConfig` (tighter timeouts than LAN) and enables mDNS for seed discovery.
- `ENVIRONMENT=standalone` runs a single node with no discovery (`NoopSeedProvider`): no mDNS, no Cloud Map. Peers join only via static `HYDRA_GOSSIP_SEEDS`.
- Running multiple nodes on the same host: give each a different `HYDRA_GOSSIP_PORT`, `BASE_PORT`, `CONTROL_PORT`.
- Logs are JSON on stderr by default. Pipe through `jq` for readability.
- Observability and load tests: `make -C deployment/develop stack-up ORIGIN_HOST=<lan-ip>` starts the OTel Collector, Prometheus, Grafana and a test origin from `docker-compose.yml`; k6 scenarios and how to run them are in `loadtest/README.md` (local only, not in the repository).

More on running several nodes, routing logs and laptop pitfalls in [docs/running-locally.md](docs/running-locally.md).

## Releasing

Releases are built by GoReleaser (`.goreleaser.yaml`): binaries for linux/darwin × amd64/arm64 plus `.deb` and `.rpm` packages with the systemd unit.

```bash
mise exec -- make release-test   # local snapshot, publishes nothing
git tag vX.Y.Z && git push origin vX.Y.Z
mise exec -- make release        # needs the tag and a GitHub token
```

## Open ideas

- Evaluate [quic-go](https://quic-go.net/docs/) (HTTP/3).
