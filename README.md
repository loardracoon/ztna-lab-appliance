# ZTNA Lab Appliance

Data-plane validation toolkit for ZTNA gateways. Single Go binary that exposes
four test surfaces (HTTP inspector with geo lookup, recursive DNS, mock SSH,
latency probe) plus a separate management plane (REST API + web UI + remote
CLI). Deployable as a Docker container or as a systemd service on bare metal.

## Quick install

```bash
unzip ztna-lab-appliance-v1.2.zip
cd ztna-lab-appliance-v1.2
./setup.sh
```

The installer detects your distro, asks for the deployment mode (Docker or
bare metal), installs any missing dependencies, builds the binary, and starts
the appliance.

## What it does

| Surface     | Port      | Purpose                                                  |
|-------------|-----------|----------------------------------------------------------|
| HTTP        | 80/tcp    | Inspector page with widgets: source IP, GeoIP, headers, TLS, upload echo |
| DNS         | 53/udp    | Local A/CNAME records + recursive forward                |
| SSH (mock)  | 2222/tcp  | Auth + interactive shell with session tracking           |
| Admin       | 9000/tcp  | Management REST API + web UI                             |

The HTTP inspector is the centerpiece: when a request hits port 80, it renders
a single page showing every transformation the ZTNA gateway applied to the
traffic — real client IP, forwarding chain, injected auth headers, gateway
identity, TLS termination point, plus a live upload/echo widget to test body
handling.

GeoIP lookup uses public APIs (ip-api.com primary, ipwho.is fallback) with a
24h in-memory cache. No local database, no API key.

## Manual install paths

If you prefer to skip the interactive installer:

```bash
# Docker
make docker-up

# Bare metal (needs Docker temporarily as builder, plus systemd)
make build
sudo make install
```

See `deployments/docker/README.md` and `deployments/linux/README.md` for the
full options, including the `bridge` profile, custom config, host port
conflict resolution, and systemd hardening details.

## Repository layout

```
.
├── setup.sh                    Interactive installer
├── Makefile                    All targets: build, install, docker-up, etc.
├── *.go                        Go sources (main, appliance layer, packages)
├── admin/  dns/  httpd/        Per-feature packages
├── logger/  sshd/
├── deployments/
│   ├── docker/                 Dockerfile + compose with host/bridge profiles
│   └── linux/                  systemd unit + install.sh + uninstall.sh
└── docs/APPLIANCE.md           Detailed operational doc (PT-BR)
```

## Build requirements

* **Runtime**: Docker (container mode) OR systemd (bare metal). Nothing else.
* **Build**: Docker (the Makefile uses `golang:1.22-alpine` as a build
  container; no Go install needed on the host).

## Configuration

Same environment variables in both deployments — only the source differs:
container reads from `docker-compose.yml`, bare metal from
`/etc/ztna-lab/config.env`.

| Variable | Default | Purpose |
|---|---|---|
| `ZTNA_ADMIN_ADDR` | `0.0.0.0:9000` | Admin API bind |
| `ZTNA_ADMIN_TOKEN` | *(empty)* | Bearer token for admin API auth |
| `ZTNA_AUTOSTART_DNS` | `true` | Start DNS server on boot |
| `ZTNA_AUTOSTART_HTTP` | `true` | Start HTTP server on boot |
| `ZTNA_AUTOSTART_SSH` | `true` | Start SSH mock on boot |

## License

MIT. See `LICENSE`.
