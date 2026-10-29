# Deployment

## AWS

Set `ENVIRONMENT=aws`. The node will:

1. Query **IMDSv2** to learn its own ENIs (private IP, MAC, subnet).
2. Register itself in **Cloud Map** with `AWS_INSTANCE_IPV4` and `AWS_INSTANCE_PORT`.
3. List the Cloud Map service to discover peer IPs and `Join` them.
4. Gossip heartbeats + topology over memberlist from then on.
5. Deregister from Cloud Map on graceful shutdown.

**Set `HYDRA_GOSSIP_SECRET` in production** (same value on every peer) so the gossip channel is AES-encrypted and only authorized nodes can join. Without it, anything that can reach port `7946/udp` in the VPC can join the cluster and hijack the hash ring. Store the key in AWS Secrets Manager or SSM Parameter Store and inject it at startup.

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

Self-referencing SG rules (Hydra SG → Hydra SG) are the recommended pattern so nodes gossip only among themselves.

### IMDSv2

No extra configuration is needed. Hydra uses IMDSv2 tokens (`X-aws-ec2-metadata-token-ttl-seconds: 21600`) and falls back on 404 for optional fields (no public IP, etc.). Ensure IMDSv2 is **enabled** on the instance; IMDSv1-only instances will fail to start.

## Debian arm64 family (Raspberry Pi OS / DietPi)

The arm64 `.deb` targets the Debian family on 64-bit ARM (Raspberry Pi OS, DietPi, Debian, Ubuntu, ...). Hydra runs as a single standalone node (`ENVIRONMENT=standalone`): no IMDS, Cloud Map, or mDNS discovery. Install it with:

```bash
wget https://github.com/norlis/hydra/releases/download/vX/hydra_X_linux_arm64.deb
sudo dpkg -i hydra_X_linux_arm64.deb
```

Override configuration in `/etc/default/hydra` (survives package upgrades), then restart:

```bash
sudo nano /etc/default/hydra
sudo systemctl restart hydra
```

## RPM-based hosts (systemd)

The `.rpm` installs the same systemd unit and `/etc/default/hydra` as the `.deb`:

```shell
yum install -y https://github.com/norlis/hydra/releases/download/vX/hydra_X_linux_arm64.rpm

systemctl start hydra

systemctl enable hydra

# logs
systemctl status hydra

# or 
journalctl -u hydra -f
```

## Verify affinity across nodes

The same entity through different nodes must leave through the same exit IP:

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

## Runtime tuning

These knobs shape how the Go runtime and the kernel behave under load. None of them are strictly required to run Hydra, but they are the difference between a node that survives a traffic spike and a node that gets OOM-killed or thrashes.

### `GOMAXPROCS` — leave it alone

Hydra builds with **Go 1.27**; since Go 1.25 the runtime reads the cgroup CPU quota on Linux containers and sets `GOMAXPROCS` automatically. Vendoring `go.uber.org/automaxprocs` is therefore intentionally **not** done — it would only matter on Go ≤ 1.24.

If you are running outside a container and want to override (e.g. pin to physical cores on a bare-metal host), set the environment variable explicitly:

```sh
GOMAXPROCS=8 hydra
```

### `GOMEMLIMIT` — set it on every container

Set `GOMEMLIMIT` to roughly **90% of the container memory limit**. This gives the GC a soft budget to pace against, which dramatically reduces the chance of an OOM kill under bursty allocation. Example for a 4 GiB container:

```sh
GOMEMLIMIT=3600MiB hydra
```

The 10% headroom covers off-heap allocations (cgo, mmap, kernel TCP buffers — Hydra holds two TCP sockets per active CONNECT tunnel). Tune down further if `pprof` shows large RSS coming from network buffers under your peak load.

`GOGC` can stay at its default (`100`); only lower it (`50`–`75`) if a GC profile shows runaway heap growth between collections.

### Sysctls (Linux hosts running Hydra)

The proxy is a long-lived TCP server with high connection turnover. Crank these once on the host (or via a `DaemonSet`/userdata) to avoid hitting kernel-side limits before Go-side ones:

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

The proxy data-plane sets keepalive on every accepted conn via `net.ListenConfig.KeepAliveConfig` (Go 1.23+): probe after 30s idle, then every 10s, drop after 3 misses (~60s to detect a dead client). This is in code and does not need any env var.

### `MaxHeaderBytes`

Capped at 16 KiB on both the proxy data-plane and the control-plane HTTP servers. Hardcoded — there is no env knob, and there is no good reason to raise it.
