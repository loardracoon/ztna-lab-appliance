# Changelog

## v1.4 — English-only control surfaces + log view fixes

* **Control panel and admin panel are now English-only.** Every user-facing
  string in the local REPL, the `ztna-lab cli` remote client and the web admin
  UI was translated: prompts, help text, usage lines, table headers, and the
  error messages the DNS/HTTP/SSH servers return through both surfaces.
  The admin page is now `lang="en"`.
* **Log view actually refreshes.** Four separate causes were fixed:
  * `refresh()` used `Promise.all`, so a single failing endpoint aborted the
    whole poll and left the log pane frozen. It now uses `Promise.allSettled`
    and each panel fails independently.
  * The polled API responses carried no cache headers. The admin API now sends
    `Cache-Control: no-store` and the UI fetches with `cache: 'no-store'` plus
    a cache-busting query parameter.
  * The tail renders oldest-first, but the pane never scrolled, so the newest
    lines stayed below the fold and the view looked static. The pane now
    follows the tail (with a `follow` toggle that respects manual scrolling).
  * `logger.Init` gave up when `ZTNA_LOG_PATH` could not be opened, which made
    `Tail` fail permanently with "logger not initialized" and no visible error.
    It now creates the parent directory, falls back to `<tmpdir>/ztna_lab.log`,
    and the UI surfaces tail errors instead of swallowing them.
* **Log file is recycled by line count.** `ZTNA_LOG_MAX_LINES` (default 5000)
  recycles the file alongside the existing `ZTNA_LOG_MAX_BYTES` (default 10 MB)
  limit — whichever is reached first. The line counter is seeded from the
  existing file on startup, so a restart on an oversized file recycles it
  immediately. Disk usage stays bounded at roughly 2x the limit (current file
  plus one `.1` backup).
* `logger.Tail` reads backwards from the end of the file in chunks instead of
  scanning it from byte 0 on every poll, and tops up from the `.1` backup so
  the view does not go blank right after a recycle.
* New `logger.Stat()` exposes path, size, line count and recycle limits;
  `/api/log/tail` returns them and the panel shows the fill level.
* Admin panel: pause/resume for the live tail, selectable tail depth
  (80/200/500/1000), log lines are HTML-escaped before rendering, and long DNS
  record names no longer push the delete button outside the card.
* Unit tests for the logger (`logger/logger_test.go`) covering recycling by
  line and byte limits, tail ordering, tail across a recycle, and the
  unwritable-path fallback.

## v1.3 — Alpine Linux support + build robustness

* **Alpine Linux support** (OpenRC, apk). New `deployments/alpine/` directory
  with OpenRC initscript, install.sh, uninstall.sh, and README.
* `setup.sh` now auto-installs `bash` on Alpine (re-exec from `/bin/sh`),
  detects the init system (systemd vs OpenRC), and uses the correct package
  manager and service manager for the chosen path. Docker install on Alpine
  uses `apk add docker docker-cli-compose` instead of `get.docker.com`.
* `make install-alpine` / `make uninstall-alpine` for the OpenRC path.
* `make status` and `make logs` auto-detect init system and dispatch.
* **Self-healing builds.** `go mod tidy` runs before every build, both in
  the Makefile and the Dockerfile. This regenerates `go.sum` automatically
  if it's missing, corrupted, or stale — so the broken-checksum errors that
  affected v1.0–v1.2 cannot recur.
* `go.sum` removed from the repository; it's generated on first build and
  should be committed afterwards for deterministic re-builds.
* Bug fix in `setup.sh`: `DEBIAN_FRONTEND=noninteractive` no longer breaks
  when running as root on Debian/Ubuntu (was missing `env` prefix, which
  caused the inline-assignment-after-variable-expansion parsing quirk).

## v1.2 — Inspector with widgets, GeoIP, and inline upload

* HTTP inspector rewritten as a single-page UI with separated widgets:
  status bar (proxy/direct trace), origin & forwarding, ZTNA identity,
  GeoIP, request basics, transport security, grouped headers, test tools.
* GeoIP lookup via public APIs (ip-api.com primary, ipwho.is fallback),
  in-memory cache (24h positive, 5min negative), no local database.
* Body echo widget — POST a text body or upload a file directly from the
  page, see size/CT round-trip inline. No external tool needed.
* Headers grouped into Forwarding / Authentication / Standard categories
  with color coding.
* TLS card adapts to context: green when TLS is direct, amber when plain
  HTTP with `X-Forwarded-Proto: https` (typical post-gateway).
* Dark mode automatic via `prefers-color-scheme`.

## v1.1 — Appliance scaffolding

* Container deployment via Docker / Podman with host or bridge networking.
* Bare metal deployment via systemd with hardened unit, non-root user,
  `CAP_NET_BIND_SERVICE` only.
* Single binary supports `daemon` and `cli` subcommands. REPL CLI talks
  to admin API over HTTP, so `docker exec` works for remote control.
* Admin REST API on port 9000 with embedded web UI for managing DNS
  records, controlling servers, viewing SSH sessions, tailing logs, and
  running latency probes.
* Persistent state via `/data` (container) or `/var/lib/ztna-lab/` (bare
  metal): DNS records JSON, SSH host key, logs.

## v1.0 — Base toolkit (REPL only)

* Interactive REPL with readline.
* DNS server (miekg/dns) with local A/CNAME records and upstream forward.
* HTTP test server with /inspector, /headers, /download/N, /upload, /health.
* SSH mock with persistent host key.
* Latency probe with percentile statistics.
* File-based logger with module prefixes and tail support.
