# Changelog

## v1.7 — Sophos firewall policy optimization

* **New `ztna-lab fwopt` subcommand: a policy optimizer for Sophos Firewall.**
  It reads the IPv4 rule base over the vendor's Firewall Configuration REST API
  (`/api/firewall-config/v1`, bearer API key) and reports what is dead,
  duplicated, over-broad or invisible to the log. Nine checks: `shadowed`,
  `permissive`, `redundant`, `duplicate`, `mergeable`, `broad-service`,
  `no-inspection`, `no-logging` and `disabled`.
* **Read-only by default.** Nothing is written without `-apply`, and `-apply`
  only performs the operations named in `-allow` — `log`, `disable`, `move`,
  `merge`, `delete`, defaulting to `log` alone, the one fix that cannot change
  what crosses the firewall. Every writing run takes a `0600` backup of the
  whole rule base first and stops at the first failure.
* **`-prefer-disable` turns every deletion into a disable**, so a cleanup can
  sit on the firewall and be reviewed — or reversed — before anything is really
  removed.
* **A shadowed rule is never deleted automatically**, not even under
  `-allow all`. Its only offered fix is promotion above the rule hiding it:
  a shadowed rule does something the rule above it does not, and removing it
  would erase a policy somebody wrote on purpose along with the evidence that
  it is not working. Deletions are offered on redundant, duplicate and disabled
  rules, which are dead already.
* **Coverage analysis errs towards silence.** Rules reference networks and
  services by name, so the analyzer compares name sets, which proves coverage
  but cannot see that two differently named objects hold the same subnet. It
  refuses to claim coverage at all when the covering rule carries an exclusion,
  a schedule, a user restriction or a Security Heartbeat requirement that the
  covered rule does not. Missing a finding costs nothing; inventing one costs a
  live rule.
* **`-fail-on` exits 3** when findings reach a severity, so a pipeline can tell
  a broken tool (exit 1) from a failing rule base.
* **`-save` / `-in` work offline.** Dump a rule base, review it later, diff two
  dumps; the API's `{"items": […]}` envelope is accepted as input too.
* `-format json` for the whole report, plan included. `SOPHOS_FW_HOST`,
  `SOPHOS_FW_API_KEY` and `SOPHOS_FW_INSECURE` keep keys off the command line.
* New `sophos/` package carrying the client, the analysis engine and the
  planner, with no dependency on the rest of the appliance. Full reference in
  [docs/SOPHOS-FWOPT.md](docs/SOPHOS-FWOPT.md).

## v1.6 — Re-deploy always builds the latest main

* **`setup.sh` is now a re-deploy tool.** Each run synchronizes
  `/opt/ztna-lab-appliance` with the head of `origin/main` and rebuilds from
  that exact commit. It hard-resets and `git clean -xfd`s the tree, so local
  edits and a stale `dist/` from an earlier build cannot leak into the build.
* **It aborts instead of installing stale code.** After syncing it checks
  `HEAD == origin/<branch>`; if the remote cannot be reached, the run fails
  rather than quietly reinstalling what was already on disk.
* **A failed sync no longer takes the appliance down.** The running instance is
  stopped only after the new code is in hand, and a fresh clone goes to a
  staging directory that is swapped in only once the clone succeeds.
* **Docker re-deploys actually pick up new code.** `docker compose up -d` only
  builds when the image tag is missing, so a re-deploy was reusing the cached
  `ztna-lab-appliance` image and kept running the old binary. New
  `make docker-redeploy` runs `build --pull` + `up -d --force-recreate`, and
  `setup.sh` uses it. `make docker-up` now passes `--build` too.
* **Fixed: `make docker-build` built nothing.** Both compose services sit behind
  a profile, and the target was missing `--profile host`, so compose selected no
  service at all and exited successfully.
* Stopped containers are now removed as well — a stopped `ztna-appliance` holds
  the name and would be recreated from the old image.
* The image tag comes from `ZTNA_IMAGE_TAG` (default `latest`) instead of a
  hardcoded `:1.0` that never tracked the real version.
* `ZTNA_MODE=docker|baremetal` skips the menu so re-deploys can run from cron,
  Ansible or CI, where `read < /dev/tty` would fail. `ZTNA_BRANCH`, `ZTNA_REPO`
  and `ZTNA_DIR` are also configurable.
* Every deploy records branch, commit, subject, mode and timestamp in
  `/etc/ztna-lab/deployed.env` — the only way to tell what is running in bare
  metal mode, where the source tree is deleted after install.

## v1.5 — Per-module log switches

* **A debug on/off switch per module in the admin panel** (SYS, DNS, HTTP, SSH,
  ADM). Switching a module off drops its lines at the source — they are never
  written to the log file and never printed to stdout — rather than merely
  hiding them from the view. Available from the panel, from the CLI
  (`log modules`, `log module <NAME> on|off`), via `GET`/`POST
  /api/log/modules`, and as a startup default through
  `ZTNA_LOG_DISABLED_MODULES=HTTP,DNS`.
* A module switch is **always recorded**, even for a module that is off, so a
  log that suddenly goes quiet still says why.
* Modules that are not shipped with the appliance register themselves, enabled,
  the first time they log — a new subsystem shows up in the panel on its own.
* **Why SSH logs looked missing.** They were not: the HTTP test plane logs every
  request, so a gateway health-checking port 80 pushes SSH out of the tail
  window within seconds. Measured: 100 requests to `:80` leave 80 HTTP lines and
  zero SSH lines in an 80-line tail. Switching HTTP off makes SSH visible
  immediately, which is what the new per-module switches are for.
* Log line colours are now shared between the tail view and the module
  switches, so a module reads the same colour everywhere.

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
