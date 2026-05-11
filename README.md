# ZTNA Lab Appliance

A self-contained appliance for validating what a ZTNA gateway does to
traffic, DNS, headers, and latency end-to-end. Single Go binary, deployable
either as a **systemd service on any Linux host** or as a **container** via
Docker/Podman. Same binary, two delivery paths.

## What it does

Exposes four test surfaces that a ZTNA gateway can be pointed at:

| Surface     | Port      | Validates                                        |
|-------------|-----------|--------------------------------------------------|
| HTTP        | 80/tcp    | Source IP, headers, file upload/download         |
| DNS         | 53/udp    | Local A/CNAME records + recursive forward        |
| SSH (mock)  | 2222/tcp  | Auth + interactive shell with session tracking   |
| Latency     | n/a       | p50/p95/p99 percentiles against any HTTP target  |

Plus a separate management plane (REST API + web UI + remote CLI) on port
**9000/tcp** for controlling the appliance without touching the test
surfaces.

## Repository layout

```
.
├── *.go                       Go sources (main, dns, httpd, sshd, admin)
├── Makefile                   single entrypoint for all targets
├── INTEGRATION.md             how to merge with existing v2.0 source (PT-BR)
├── deployments/
│   ├── linux/                 bare metal installer + systemd unit
│   └── docker/                Dockerfile + docker-compose.yml
└── docs/                      detailed docs (architecture, API, PT-BR)
```

> **First time setting up?** Read [`INTEGRATION.md`](INTEGRATION.md) — this
> package contains the appliance layer; you need to merge it with the
> original ZTNA Lab v2.0 Go source before the first build.

## Quick start — Docker / Podman

```bash
make docker-up
# admin panel at http://localhost:9000
```

Uses host networking so UDP/53 and TCP/80 work transparently. Data
persisted in a named volume.

## Quick start — bare metal Linux (systemd)

```bash
make build         # compiles inside golang:1.22-alpine, no Go needed locally
sudo make install
# admin panel at http://<host>:9000
```

Creates a `ztna-lab` system user, installs to `/usr/local/bin`, drops a
hardened systemd unit, runs as non-root with only `CAP_NET_BIND_SERVICE`.

## Management interfaces

* **Web UI**: `http://<host>:9000`
* **REST API**: see [`docs/API.md`](docs/API.md)
* **CLI** (local or remote, talks to the admin API):
  ```bash
  ztna-lab cli                                          # bare metal
  docker exec -it ztna-appliance /ztna-lab cli          # container
  ```

## Configuration

Both deployments read the same environment variables. On bare metal they
live in `/etc/ztna-lab/config.env`; in Docker, in `docker-compose.yml`.

| Variable | Default | Purpose |
|---|---|---|
| `ZTNA_ADMIN_ADDR` | `0.0.0.0:9000` | Admin API bind |
| `ZTNA_ADMIN_TOKEN` | *(empty)* | Bearer token for the admin API |
| `ZTNA_AUTOSTART_DNS` | `true` | Start DNS server on boot |
| `ZTNA_AUTOSTART_HTTP` | `true` | Start HTTP server on boot |
| `ZTNA_AUTOSTART_SSH` | `true` | Start SSH mock on boot |

## Build requirements

* **For deployment**: Docker or systemd. No Go runtime needed on the target.
* **For development**: Docker (the Makefile uses `golang:1.22-alpine` as a
  build container, so you don't have to install Go locally either).
* **Architecture support**: Linux/amd64 today. Cross-compile by setting
  `GOARCH=arm64` in the Makefile.

## License

MIT (or whatever applies to your fork).
