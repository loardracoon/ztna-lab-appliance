# ZTNA Lab Appliance

A self-contained data-plane validation toolkit for ZTNA (Zero Trust Network Access) gateways. It provides four test surfaces — HTTP inspector, recursive DNS, mock SSH, and latency probe — plus a separate management plane with a REST API, web UI, and remote CLI. It also ships a Sophos Firewall policy optimizer, which reviews a rule base over the vendor's REST API and cleans it up on request. All in a single Go binary, deployable as a Docker container or a bare-metal service.

## Why this exists

When evaluating or debugging a ZTNA gateway, you need controlled endpoints that let you observe exactly what the gateway does to traffic. Typical questions:

- Is the gateway injecting auth headers? Which ones? Does it rewrite the source IP?
- Is DNS resolution happening inside or outside the tunnel?
- Can SSH reach the target? Which user reaches the other side?
- What is the round-trip latency introduced by the gateway?

Standing up separate servers for each of these is tedious. ZTNA Lab bundles them all into one appliance you can spin up in seconds and tear down cleanly.

## How it works

```
                      ┌─────────────────────────────────────┐
                      │         ZTNA Lab Appliance           │
 ZTNA gateway ───────▶│  :80   HTTP Inspector                │
 (or direct)  ───────▶│  :53   DNS (UDP)                     │
              ───────▶│  :2222 SSH mock                      │
                      │                                      │
 Admin / ops  ───────▶│  :9000 Admin API + Web UI            │
              ───────▶│         CLI (ztna-lab cli)            │
                      └─────────────────────────────────────┘
```

**Test plane** — the four surfaces that your ZTNA gateway talks to:

| Surface | Port | What it does |
|---|---|---|
| HTTP Inspector | 80/tcp | Renders a page showing the real client IP, all headers, TLS info, and a live upload/download widget. Reveals every transformation the gateway applied. |
| DNS | 53/udp | Authoritative for any A/CNAME records you add; forwards everything else recursively to 1.1.1.1. Lets you verify that DNS queries go through (or bypass) the tunnel. |
| SSH mock | 2222/tcp | Accepts any credential (password or public key). Offers a minimal interactive shell with `whoami`, `all-users`, and echo. Proves SSH connectivity and shows which user identity reaches the target. |
| Latency probe | — | Fires N HTTP GET requests against any URL, measuring per-connection round-trip times with statistics (min/avg/p50/p95/p99/max/jitter). Triggered from the CLI or Admin API. |

**Management plane** — talks to the daemon, not the gateway:

| Component | Details |
|---|---|
| Admin REST API | `:9000` — start/stop services, manage DNS records, tail logs, run latency, toggle SSH debug mode. Optional bearer-token auth. |
| Web UI | Same port, browser-accessible dashboard at `http://<host>:9000`. |
| CLI | `ztna-lab cli` — REPL that speaks to the Admin API. Same commands whether you run it on the host or `exec` into the container. |
| Firewall policy optimizer | `ztna-lab fwopt` — reviews a Sophos Firewall rule base over the Firewall Configuration REST API and, on request, applies the fixes. Points at a firewall, not at this appliance. See [docs/SOPHOS-FWOPT.md](docs/SOPHOS-FWOPT.md). |

### HTTP Inspector endpoints

| Path | Method | Description |
|---|---|---|
| `/` or `/inspector` | GET | Full inspector page (client IP, GeoIP, headers, TLS) |
| `/headers` | GET | Headers dump in plain text — curl-friendly |
| `/download/<N>` | GET | Stream N random bytes (throughput test) |
| `/upload` | POST/PUT | Discards the body, returns received byte count |
| `/health` | GET | Liveness probe: `{"status":"ok"}` |

GeoIP lookup uses ip-api.com (fallback: ipwho.is) with a 24-hour in-memory cache. No local database, no API key required.

### SSH mock shell commands

```
whoami        → prints current user and source IP
all-users     → table of all connected sessions (ID, user, IP, timestamp)
exit / quit   → closes the session
<anything>    → echoed back
```

---

## Quick install

The interactive installer detects your distro, init system (systemd or OpenRC), and installs everything automatically.

```bash
bash setup.sh
```

Or pipe directly from the web (works even with `curl | bash` — the installer handles TTY correctly):

```bash
curl -fsSL https://raw.githubusercontent.com/loardracoon/ztna-lab-appliance/main/setup.sh | bash
```

The installer:
1. Detects the package manager (`apk`, `apt`, `dnf`, `yum`) and init system (`openrc`, `systemd`)
2. Installs `git`, `curl`, `make`, and Docker if missing
3. Asks whether to deploy via **Docker** or **bare metal**
4. Synchronizes `/opt/ztna-lab-appliance` with the head of `origin/main`
5. Stops any previous instance, checks for port conflicts on 53, 80, 2222, and 9000
6. Builds, installs, and starts the appliance from that exact commit
7. Records what was deployed in `/etc/ztna-lab/deployed.env`
8. In bare metal mode, removes build tools after install to minimize footprint

Tested on: **Alpine Linux 3.18+** (OpenRC), **Debian/Ubuntu** (systemd), **RHEL/Rocky/Alma/CentOS** (systemd).

### Re-deploying

Running the installer again is the supported way to upgrade: it is idempotent
and every run rebuilds from the current head of `origin/main`.

```bash
bash setup.sh                       # sync + rebuild, interactive
ZTNA_MODE=docker bash setup.sh      # no menu — for cron, Ansible, CI
```

What the re-deploy guarantees:

* The working tree is hard-reset to `origin/<branch>` and `git clean -xfd`-ed,
  so local edits and a stale `dist/` from a previous build cannot leak in.
* The installer verifies `HEAD == origin/<branch>` and **aborts** if it cannot
  reach the remote, rather than silently reinstalling the old code.
* The previous instance is stopped only *after* the new code is in hand, so a
  failed sync leaves the running appliance untouched.
* Docker mode rebuilds the image and recreates the container
  (`build --pull` + `up -d --force-recreate`). A plain `docker compose up -d`
  reuses the existing image tag and would keep running the old binary.
* Bare metal mode runs `make clean` first, so the timestamp-driven build can
  never install a leftover binary.

Check what is currently deployed — useful in bare metal mode, where the source
tree is deleted after install:

```bash
cat /etc/ztna-lab/deployed.env
# branch=main
# commit=4d90ca1c5d64a75c23755aac3ed7f40573ee5327
# subject=Merge branch 'claude/relaxed-goodall-bj83li'
# mode=docker
# deployed_at=2026-09-16T04:28:37Z
```

Installer environment variables:

| Variable | Default | Description |
|---|---|---|
| `ZTNA_MODE` | *(menu)* | `docker` or `baremetal` — skips the interactive prompt |
| `ZTNA_BRANCH` | `main` | Branch to deploy |
| `ZTNA_REPO` | official repo | Repository URL to clone from |
| `ZTNA_DIR` | `/opt/ztna-lab-appliance` | Where the source is kept |
| `ZTNA_IMAGE_TAG` | `latest` | Docker image tag built by compose |

---

## Manual install

If you prefer not to use the interactive installer:

### Docker (recommended)

```bash
# Clone the repo
git clone https://github.com/loardracoon/ztna-lab-appliance.git
cd ztna-lab-appliance

# Build image and start (network_mode: host — no NAT overhead)
make docker-up

# After pulling new code: rebuild the image and recreate the container.
# `up -d` alone reuses the existing image tag and keeps the old binary running.
git pull && make docker-redeploy

# Tail logs
make docker-logs

# Open a CLI session inside the running container
make docker-cli

# Stop
make docker-down
```

The `host` network profile is the default — UDP/53 is exposed directly on the host interface, which avoids kernel NAT overhead that would skew latency measurements. A `bridge` profile with explicit port mappings is also available:

```bash
docker compose -f deployments/docker/docker-compose.yml --profile bridge up -d
```

### Bare metal — systemd (Debian / Ubuntu / RHEL / Rocky / Fedora)

```bash
make build          # compiles a static linux/amd64 binary via Docker
sudo make install   # copies binary, installs and enables the systemd unit
```

### Bare metal — OpenRC (Alpine)

```bash
make build
sudo make install-alpine
```

### Uninstall

```bash
sudo make uninstall          # systemd
sudo make uninstall-alpine   # OpenRC

make docker-clean            # removes image and data volume
```

---

## Using the appliance

### Web UI

Open `http://<host>:9000` in a browser. The dashboard shows the live state of all services, lets you start/stop them individually, manage DNS records, toggle SSH debug mode, tail the log, and run a latency probe — all without a terminal.

### CLI

The CLI connects to the Admin API and exposes the same commands in an interactive REPL:

```bash
# Inside the container
docker exec -it ztna-appliance /ztna-lab cli

# On the host (bare metal install), pointing at a remote appliance
ZTNA_ADMIN_URL=http://10.0.0.5:9000 ztna-lab cli
```

```
ztna> status                            # service states
ztna> dns list                          # A and CNAME records
ztna> dns add app.lab 10.0.0.42         # add A record
ztna> dns cname add www.lab app.lab     # add CNAME
ztna> dns remove app.lab                # delete record
ztna> ssh who                           # active SSH sessions
ztna> log tail 100                      # last 100 log lines
ztna> log modules                       # per-module logging state
ztna> log module HTTP off               # stop logging HTTP entirely
ztna> latency run http://10.0.0.1 50 200  # 50 probes, 200 ms apart
ztna> exit
```

### Sophos firewall policy optimizer

`ztna-lab fwopt` is a standalone tool inside the same binary. It talks to a
Sophos Firewall's [Firewall Configuration REST API](https://docs.sophos.com/nsg/sophos-firewall/rest-api/),
not to this appliance, and it is read-only unless you pass `-apply`.

```bash
export SOPHOS_FW_HOST=10.0.0.1:4444      # address and WebAdmin port
export SOPHOS_FW_API_KEY='…'             # bearer token generated on the firewall

# Review. Nothing is written.
ztna-lab fwopt -insecure

# Keep a copy and work against it offline.
ztna-lab fwopt -insecure -save rules.json
ztna-lab fwopt -in rules.json -format json -out review.json

# Apply only the fixes that cannot change what the firewall forwards.
ztna-lab fwopt -insecure -apply -allow log,merge
```

It reports:

| Check | What it means |
|---|---|
| `shadowed` | An earlier rule already matches this traffic and does something else with it — the rule never evaluates, so its policy is not in force. Never deleted automatically; the offered fix is to promote it above the rule hiding it |
| `permissive` | `accept` from any source to any destination on any service |
| `redundant` / `duplicate` | Already covered by an earlier rule that treats the traffic the same way |
| `mergeable` | Two adjacent rules that could be one |
| `broad-service` | `accept` with no service restriction |
| `no-inspection` | `accept` with no IPS, web, application policy or scanning |
| `no-logging` | `logTraffic` off, so matches never reach the log |
| `disabled` | Switched off, still in the rule base |

Applying is opt-in twice over: `-apply` sends the plan, and `-allow` decides
which operations it may use (`log`, `disable`, `move`, `merge`, `delete`; the
default is `log` alone). Every run that writes takes a `0600` backup of the
whole rule base first and asks for confirmation unless given `-yes`.
`-prefer-disable` turns every deletion into a disable, so a cleanup can be
reviewed on the firewall before anything is really removed.

Full reference, including how coverage is decided and what the tool
deliberately will not conclude: **[docs/SOPHOS-FWOPT.md](docs/SOPHOS-FWOPT.md)**.

### Admin REST API

All endpoints require `Authorization: Bearer <token>` when `ZTNA_ADMIN_TOKEN` is set.

| Method | Path | Description |
|---|---|---|
| GET | `/api/status` | Service states and SSH debug flag |
| GET | `/api/health` | Liveness probe (no auth required) |
| POST | `/api/dns/start` | Start DNS server |
| POST | `/api/dns/stop` | Stop DNS server |
| GET | `/api/dns/records` | List A and CNAME records |
| POST | `/api/dns/records` | Add record `{"type":"A","name":"...","value":"..."}` |
| DELETE | `/api/dns/records/<name>` | Delete record |
| POST | `/api/http/start` | Start HTTP server |
| POST | `/api/http/stop` | Stop HTTP server |
| POST | `/api/ssh/start` | Start SSH server |
| POST | `/api/ssh/stop` | Stop SSH server |
| GET | `/api/ssh/sessions` | List active SSH sessions |
| POST | `/api/ssh/debug` | Toggle SSH debug mode `{"enabled":true}` — no restart needed |
| GET | `/api/log/tail?n=50` | Last N log lines, plus log file stats (path, size, line count, recycle limits) and per-module state |
| GET | `/api/log/modules` | Per-module logging state |
| POST | `/api/log/modules` | Switch a module's logging on/off `{"module":"HTTP","enabled":false}` |
| POST | `/api/latency` | Run latency probe `{"url":"...","count":50,"interval_ms":200}` |

Example:

```bash
TOKEN=$(openssl rand -hex 32)

curl http://localhost:9000/api/status
curl -X POST http://localhost:9000/api/dns/records \
     -H "Authorization: Bearer $TOKEN" \
     -d '{"type":"A","name":"target.lab","value":"192.168.1.10"}'
curl -X POST http://localhost:9000/api/ssh/debug \
     -H "Authorization: Bearer $TOKEN" \
     -d '{"enabled":true}'
```

---

## Configuration

All settings are environment variables. Docker reads them from `docker-compose.yml`; bare metal reads from `/etc/ztna-lab/config.env` (see `deployments/linux/config.env.example`).

| Variable | Default | Description |
|---|---|---|
| `ZTNA_ADMIN_ADDR` | `0.0.0.0:9000` | Admin API bind address |
| `ZTNA_ADMIN_TOKEN` | *(empty)* | Bearer token. Empty = no authentication (lab-only) |
| `ZTNA_AUTOSTART_DNS` | `true` | Start DNS on boot |
| `ZTNA_AUTOSTART_HTTP` | `true` | Start HTTP on boot |
| `ZTNA_AUTOSTART_SSH` | `true` | Start SSH on boot |
| `ZTNA_SSH_KEY` | `/data/ssh_host_key` | RSA host key path. Generated automatically if absent. |
| `ZTNA_SSH_ED25519_KEY` | *(empty)* | Ed25519 host key path (optional; use one, both, or neither) |
| `ZTNA_SSH_DEBUG` | `false` | Enable SSH debug logging at startup. Can also be toggled at runtime via `POST /api/ssh/debug`. |
| `ZTNA_DNS_RECORDS` | `/data/dns_records.json` | DNS records persistence file |
| `ZTNA_LOG_PATH` | `/data/ztna_lab.log` | Log file path. The parent directory is created if missing; if the path cannot be opened the logger falls back to `<tmpdir>/ztna_lab.log` so the log view keeps working. |
| `ZTNA_LOG_MAX_LINES` | `5000` | Recycle the log file once it reaches this many lines. |
| `ZTNA_LOG_MAX_BYTES` | `10485760` | Recycle the log file once it reaches this many bytes. Whichever limit is hit first wins. |
| `ZTNA_LOG_DISABLED_MODULES` | *(empty)* | Comma-separated modules to start with logging switched off, e.g. `HTTP,DNS`. Toggle at runtime from the admin panel, the CLI, or `POST /api/log/modules`. |
| `ZTNA_ADMIN_URL` | `http://127.0.0.1:9000` | Used by `ztna-lab cli` to locate the daemon |
| `SOPHOS_FW_HOST` | *(empty)* | `ztna-lab fwopt`: firewall address and WebAdmin port, e.g. `10.0.0.1:4444` |
| `SOPHOS_FW_API_KEY` | *(empty)* | `ztna-lab fwopt`: API key, sent as a bearer token |
| `SOPHOS_FW_INSECURE` | `false` | `ztna-lab fwopt`: skip TLS verification (needed for the default self-signed WebAdmin certificate) |

To protect the Admin API with authentication, generate a token and set it before starting:

```bash
openssl rand -hex 32   # copy the output into ZTNA_ADMIN_TOKEN
```

---

## Repository layout

```
.
├── setup.sh                       Interactive installer (v2.0)
├── Makefile                       Build, install, docker, and utility targets
│
├── main.go                        Interactive REPL (local mode) + latency runner
├── main_appliance.go              Daemon entrypoint, subcommand routing
├── cli_client.go                  Remote CLI (ztna-lab cli subcommand)
├── fwopt.go                       Sophos policy optimizer CLI (ztna-lab fwopt subcommand)
│
├── admin/
│   ├── server.go                  Admin REST API (all /api/* routes)
│   ├── ui.html                    Single-page web dashboard (embedded)
│   └── latency.go                 Latency result types for the API
│
├── dns/
│   ├── server.go                  UDP DNS server with recursive forwarding
│   └── records.go                 A/CNAME record store (JSON persistence)
│
├── httpd/
│   ├── server.go                  HTTP test server and endpoints
│   ├── inspector.go               Inspector page handler (GeoIP, headers)
│   └── inspector.html             Inspector page template (embedded)
│
├── sshd/
│   ├── server.go                  SSH mock server (accept-all, session tracking)
│   └── config.go                  Config struct + ConfigFromEnv()
│
├── logger/
│   └── logger.go                  Shared logger: stdout + file, per-module switches, line- and size-based recycling
│
├── sophos/                        Sophos Firewall policy optimization engine
│   ├── types.go                   IPv4 rule model (Firewall Configuration API schema)
│   ├── client.go                  REST client: list, get, create, update, delete, move
│   ├── coverage.go                Set semantics — does rule A cover rule B?
│   ├── analyzer.go                The checks and the findings they produce
│   ├── plan.go                    Findings to API calls, conflict resolution, apply
│   └── report.go                  Text and JSON rendering
│
├── docs/
│   ├── APPLIANCE.md               Appliance internals
│   └── SOPHOS-FWOPT.md            Firewall policy optimizer reference
│
└── deployments/
    ├── docker/
    │   ├── Dockerfile             Multi-stage build (Go builder + Alpine runtime)
    │   ├── docker-compose.yml     host and bridge profiles
    │   └── README.md
    ├── linux/
    │   ├── ztna-lab.service       systemd unit (hardened)
    │   ├── config.env.example     Environment variable reference
    │   ├── install.sh             Install script called by make install
    │   └── uninstall.sh
    └── alpine/
        ├── ztna-lab.openrc        OpenRC initscript
        ├── install.sh             Install script called by make install-alpine
        └── uninstall.sh
```

---

## Build requirements

- **Build**: Docker with the Compose plugin. The Makefile uses `golang:1.22-alpine` as a build container — no Go toolchain needed on the host.
- **Runtime (Docker mode)**: Docker only.
- **Runtime (bare metal)**: systemd (Debian/Ubuntu/RHEL) or OpenRC (Alpine). The binary is a static `linux/amd64` executable with no external dependencies.

---

## License

MIT. See [LICENSE](LICENSE).
