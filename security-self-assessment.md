# Hydra Proxy — Security Self-Assessment

This document follows the [CNCF TAG-Security self-assessment](https://github.com/cncf/tag-security/blob/main/community/assessments/guide/self-assessment.md) format. It records the security posture of Hydra as currently implemented, based on source review.

## Metadata

|   |   |
| -- | -- |
| Assessment stage | Complete (point-in-time, self-conducted) |
| Software | <https://github.com/norlis/hydra> |
| Security provider | No — Hydra is a self-clustering forward-proxy mesh; its defensive controls protect its own operation, it is not a security product. |
| Languages | Go |
| SBOM | Not published. Dependencies are declared in `go.mod` / `go.sum`. |

### Security links

| Doc | URL / path |
| -- | -- |
| Security-relevant configuration | Configuration section; `internal/config.go` |
| SSRF classifier | `internal/proxy/ipcheck/` |
| Systemd packaging | `deployment/systemd/hydra.service` |

## Overview

Hydra is a self-clustering HTTP forward-proxy mesh. Nodes discover each other via SWIM gossip (memberlist), share topology, and route requests with consistent hashing: a request tagged `X-Entity-ID` is served by the node/interface that owns that key, giving each entity a stable egress IP.

### Background

Each node exposes one proxy listener per network interface (data plane), an HTTP API with pprof (control plane), and participates in a gossip cluster (cluster plane). On AWS, interfaces and seeds are discovered via IMDS and Cloud Map.

### Actors

* **Data plane** — per-NIC proxy servers (`BASE_PORT`+n). Client-facing; performs routing, CONNECT tunneling, and upstream forwarding. Isolated from the control plane by port and handler.
* **Control plane** — HTTP API + pprof on `CONTROL_PORT` (default 9192). Plain HTTP, no authentication; isolation relies on network placement.
* **Cluster plane** — memberlist gossip on `HYDRA_GOSSIP_PORT` (7946 TCP+UDP). Carries membership and node metadata; supports symmetric AES encryption.
* **Infrastructure providers** — IMDS/Cloud Map (AWS) or local NIC/mDNS discovery, selected by `ENVIRONMENT`.

### Actions

* Client request arrives with `X-Entity-ID`; the node hashes it on the ring and either serves locally or forwards to the owning peer with `X-Hydra-Hop: 1` (loop prevention; the header is never trusted from clients).
* For HTTPS, the node establishes an opaque CONNECT tunnel after validating the destination port allowlist and the SSRF classifier verdict.
* Every upstream dial passes the SSRF classifier in `net.Dialer.Control` — after DNS resolution, before the SYN.

### Goals

* Safe egress: proxied traffic cannot reach internal/private networks (SSRF protection).
* Header hygiene: internal routing headers and credentials never leak upstream.
* End-to-end TLS between client and origin, untouched by the proxy.
* Optional client authentication and encrypted cluster membership.

### Non-goals

* TLS interception, inspection, or policy enforcement on tunneled traffic.
* Hardening the control plane for public exposure (it is designed for private networks).
* Anonymization or privacy guarantees toward origins beyond source-IP selection.

## Self-assessment use

This self-assessment was created by the Hydra maintainers to perform an internal analysis of the project's security. It is not an independent security audit and does not imply certification against any framework. It is informative only and makes no forward-looking commitments.

## Security functions and features

### Critical

* **SSRF classifier** (`internal/proxy/ipcheck`, enforced at `internal/proxy/forwarder.go:91`): destination IPs are classified after DNS resolution and before the SYN, so DNS rebinding cannot bypass it. Denied by default: RFC 1918, loopback, link-local (incl. AWS IMDS `169.254.169.254`), multicast, reserved/test ranges, CGNAT (`100.64.0.0/10`), IPv4-embedded IPv6 (NAT64 `64:ff9b::/96`, 6to4 `2002::/16`, Teredo `2001::/32`; IPv4-mapped addresses are unmapped before evaluation), and the proxy's own addresses (self-connect denial). `HYDRA_PROXY_DENY_CIDR` adds ranges; `HYDRA_PROXY_ALLOW_CIDR` creates exceptions.
* **Header sanitization** (`internal/proxy/forwarder.go:432-445`): before any request leaves the proxy, the following are removed: `X-Hydra-Hop` (loop marker — never trusted from clients), `X-Entity-ID` (routing key — not leaked upstream), `Proxy-Authorization`, `Proxy-Connection`, all RFC 7230 hop-by-hop headers (including any header named in `Connection`), plus operator-configured extras (`HYDRA_STRIP_HEADERS`).
* **Proxy authentication** (`internal/proxy/auth.go`): optional RFC 7617 Basic auth (`HYDRA_PROXY_AUTH_MODE=basic`); credential comparison uses `crypto/subtle.ConstantTimeCompare` (no timing side channel); `Proxy-Authorization` is deleted immediately after validation on every path (CONNECT, plain HTTP, peer relay).
* **Memory safety**: pure Go with zero uses of the `unsafe` package anywhere in the repo.

### Security relevant

* **No TLS interception**: HTTPS crosses the proxy as opaque CONNECT tunnels end-to-end between client and origin; the proxy cannot read or downgrade that traffic. TLS version/cipher policy remains a client↔origin contract — Hydra never negotiates TLS on the data path.
* **Request-smuggling mitigation**: Go's strict `net/http` parser rejects malformed framing and ambiguous `Transfer-Encoding`/`Content-Length`; HTTP/2 is explicitly disabled on the data plane (`internal/proxy/server.go` empties `TLSNextProto`), removing h2-specific request-splitting surface.
* **Timeouts and limits** — data plane: `ReadHeaderTimeout` 30s (slow-header mitigation), `IdleTimeout` 300s, 16 KB header cap; no global read/write timeouts by design (long-lived tunnels), per-tunnel inactivity bounded by `HYDRA_PROXY_IDLE_TIMEOUT`. Control plane: 10/15/30/60s + 16 KB cap. Concurrent tunnels capped by `HYDRA_PROXY_MAX_TUNNELS` (default `0` = unlimited). CONNECT destinations restricted to `HYDRA_PROXY_ALLOWED_PORTS` (default `443,80`).
* **Gossip encryption**: memberlist supports AES-128/192/256 via `HYDRA_GOSSIP_SECRET` (`internal/cluster/memberlist.go:75`); disabled by default with an explicit startup warning.
* **Logging discipline**: no request headers or bodies are logged; proxy logs carry method, host, routing decision, peer, and status (`internal/proxy/router.go:76-82`). Combined with the early strip of `Proxy-Authorization`, credentials cannot reach telemetry. Note: `X-Entity-ID` values are logged as the routing key — callers should use pseudonymous IDs, not personal data.

## Project compliance

Hydra does not claim compliance with any formal security standard or framework (e.g. SOC 2, FIPS 140, PCI-DSS). No compliance audits have been performed.

## Secure development practices

### Development pipeline

* Quality/security tooling is standardized as Makefile targets, pinned via `tools/go.mod` and installed into `./bin/`: `make lint` (golangci-lint, runs with `--fix`), `make vulncheck` (govulncheck over the module graph), `make format` / `make check-format` (gofumpt; the check variant is CI-safe).
* Unit tests run via `make test` (`go test ./... --cover`).
* Gap: these are developer-invoked steps; the only CI workflow (`release.yml`) builds releases without gating on lint, vulncheck, format, or tests.

### Communication channels

* Development and issue tracking on the GitHub repository. No dedicated security channel exists yet.

### Ecosystem

* Deployed on EC2 (multi-ENI instances) or on-prem Linux via systemd packaging; integrates with AWS IMDS and Cloud Map for discovery, and OpenTelemetry (OTLP push) for metrics.

## Security issue resolution

* There is no formal responsible-disclosure or incident-response process yet. Suspected vulnerabilities can be reported to the maintainer via the repository. No response SLA is defined.

## Appendix

### Known issues (current gaps, by operational impact)

1. **Control plane exposure:** plain HTTP, no auth, includes pprof. Must remain network-isolated; any future public exposure requires adding auth/TLS first.
2. **Gossip runs unencrypted by default** — `HYDRA_GOSSIP_SECRET` must be set explicitly per deployment.
3. **`HYDRA_PROXY_MAX_TUNNELS=0` (unlimited) by default** — no built-in backpressure against tunnel exhaustion until configured.
4. **systemd unit runs as `root`** (`deployment/systemd/hydra.service`) — no privilege separation in the shipped packaging.
5. **Quality/security tooling not in CI** — `make lint`, `make vulncheck`, `make check-format`, and the test suite only run when a developer invokes them.
6. **Allowlist precedence** (`HYDRA_PROXY_ALLOW_CIDR` overrides all denies, including self-connect, `internal/proxy/ipcheck/classifier.go:97`) — intentional for development, but a misconfiguration risk in production.

### OpenSSF Best Practices

The project has not pursued the OpenSSF Best Practices badge.
