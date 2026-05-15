#!/usr/bin/env bash
# ZTNA Lab Appliance — interactive installer
#
# Pergunta modo de deploy (Docker ou bare metal systemd), instala dependências,
# builda o binário e deploya. Idempotente — pode ser re-rodado.

set -euo pipefail

# ──────────────────────── cores ────────────────────────
if [ -t 1 ]; then
  C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YLW=$'\033[33m'
  C_DIM=$'\033[2m'; C_BOLD=$'\033[1m'; C_RST=$'\033[0m'
else
  C_RED=""; C_GRN=""; C_YLW=""; C_DIM=""; C_BOLD=""; C_RST=""
fi
info()  { printf "  %s\n" "$*"; }
ok()    { printf "  ${C_GRN}✓${C_RST} %s\n" "$*"; }
warn()  { printf "  ${C_YLW}⚠${C_RST} %s\n" "$*"; }
err()   { printf "  ${C_RED}✗${C_RST} %s\n" "$*" >&2; }
hdr()   { printf "\n${C_BOLD}▶ %s${C_RST}\n" "$*"; }
die()   { err "$*"; exit 1; }

# ──────────────────────── pre-checks ────────────────────────
[ "$(uname -s)" = "Linux" ] || die "This installer only supports Linux."
[ -f /etc/os-release ] || die "Cannot detect distribution (/etc/os-release missing)."
[ -f Makefile ] && [ -f go.mod ] || die "Run this script from the project root."

# shellcheck disable=SC1091
. /etc/os-release
DISTRO_ID="${ID:-unknown}"

# package manager
if   command -v apt-get >/dev/null 2>&1; then PKG="apt"
elif command -v dnf     >/dev/null 2>&1; then PKG="dnf"
elif command -v yum     >/dev/null 2>&1; then PKG="yum"
elif command -v zypper  >/dev/null 2>&1; then PKG="zypper"
elif command -v pacman  >/dev/null 2>&1; then PKG="pacman"
elif command -v apk     >/dev/null 2>&1; then PKG="apk"
else PKG=""
fi

# sudo
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  command -v sudo >/dev/null 2>&1 || die "Not root and sudo is unavailable. Re-run as root or install sudo."
  SUDO="sudo"
fi

# ──────────────────────── helpers ────────────────────────
pkg_install() {
  case "$PKG" in
    apt)     $SUDO apt-get update -qq && $SUDO DEBIAN_FRONTEND=noninteractive apt-get install -y "$@" ;;
    dnf|yum) $SUDO "$PKG" install -y "$@" ;;
    zypper)  $SUDO zypper -n install "$@" ;;
    pacman)  $SUDO pacman -Sy --noconfirm "$@" ;;
    apk)     $SUDO apk add --no-cache "$@" ;;
    *)       die "No supported package manager found ($PKG)." ;;
  esac
}

ensure_curl() {
  command -v curl >/dev/null 2>&1 || pkg_install curl
}

ensure_docker() {
  if command -v docker >/dev/null 2>&1; then
    ok "Docker: $(docker --version | head -1)"
  else
    warn "Docker not installed."
    read -r -p "  Install Docker via get.docker.com? [Y/n] " ans
    case "${ans:-Y}" in
      [Yy]*) ;;
      *) die "Cannot continue without Docker." ;;
    esac
    ensure_curl
    curl -fsSL https://get.docker.com -o /tmp/get-docker.sh
    $SUDO sh /tmp/get-docker.sh
    rm -f /tmp/get-docker.sh
    $SUDO systemctl enable --now docker
    ok "Docker installed."
  fi

  # compose
  if docker compose version >/dev/null 2>&1; then
    ok "docker compose (plugin) present"
  elif command -v docker-compose >/dev/null 2>&1; then
    ok "docker-compose (standalone) present"
  else
    warn "compose plugin missing — attempting install"
    case "$PKG" in
      apt) pkg_install docker-compose-plugin ;;
      dnf|yum) pkg_install docker-compose-plugin || true ;;
      *) warn "Install docker compose manually: https://docs.docker.com/compose/install/" ;;
    esac
  fi

  # docker daemon up?
  if ! $SUDO docker info >/dev/null 2>&1; then
    warn "Docker daemon not responding — starting..."
    $SUDO systemctl start docker || die "Failed to start docker."
  fi
}

# usuário no grupo docker pra rodar make sem sudo
ensure_docker_group() {
  if [ -z "$SUDO" ]; then return; fi  # root já tem
  if id -nG "$USER" | grep -qw docker; then return; fi
  warn "User '$USER' is not in the 'docker' group."
  read -r -p "  Add '$USER' to 'docker' group? (needs re-login afterwards) [Y/n] " ans
  case "${ans:-Y}" in
    [Yy]*)
      $SUDO usermod -aG docker "$USER"
      info "Added. ${C_BOLD}You'll need to log out and back in for this to take effect.${C_RST}"
      info "For now, this script will use sudo for docker commands."
      DOCKER_PREFIX="$SUDO "
      ;;
    *)
      DOCKER_PREFIX="$SUDO "
      ;;
  esac
}

check_ports() {
  local need_ss=0
  command -v ss >/dev/null 2>&1 || need_ss=1
  [ $need_ss -eq 1 ] && pkg_install iproute2 2>/dev/null || true

  local conflicts=""
  for p in "$@"; do
    if ss -tulnH 2>/dev/null | grep -qE "[:.]${p}\b"; then
      conflicts="$conflicts $p"
    fi
  done
  if [ -z "$conflicts" ]; then
    ok "Ports free: $*"
    return
  fi
  warn "Ports already in use:$conflicts"
  ss -tulnp 2>/dev/null | head -1
  for p in $conflicts; do
    ss -tulnp 2>/dev/null | grep -E "[:.]${p}\b" | sed 's/^/    /' | head -3
  done
  echo
  info "Common culprits:"
  info "  port 53  → systemd-resolved (sudo systemctl disable --now systemd-resolved)"
  info "  port 80  → nginx, apache"
  echo
  read -r -p "  Continue anyway (the appliance will fail to bind these)? [y/N] " ans
  case "${ans:-N}" in
    [Yy]*) ;;
    *) die "Aborted." ;;
  esac
}

# ──────────────────────── banner ────────────────────────
clear 2>/dev/null || true
cat <<EOF

${C_BOLD}ZTNA Lab Appliance${C_RST} — installer · v1.2

Detected environment:
  Distribution :  ${PRETTY_NAME:-$DISTRO_ID}
  Package mgr  :  ${PKG:-none}
  Privilege    :  $([ -z "$SUDO" ] && echo "root" || echo "$USER (sudo)")

EOF

# ──────────────────────── menu ────────────────────────
hdr "Choose deployment mode"
cat <<EOF
  ${C_BOLD}1)${C_RST} Docker  (recommended)
     Container deploy. Fastest, isolated, easy to remove.
     Requires: Docker engine + compose plugin (will offer to install).

  ${C_BOLD}2)${C_RST} Bare metal  (systemd service)
     Native install at /usr/local/bin/ + /etc/ztna-lab/ + systemd unit.
     Builds the binary using Docker, then runs it directly on the host.
     Requires: systemd, Docker (build only — can be removed after).

  ${C_BOLD}3)${C_RST} Exit

EOF

while true; do
  read -r -p "  Choice [1/2/3]: " choice
  case "$choice" in
    1) MODE="docker"; break ;;
    2) MODE="baremetal"; break ;;
    3) info "Cancelled."; exit 0 ;;
    *) warn "Invalid choice — type 1, 2, or 3." ;;
  esac
done

DOCKER_PREFIX="$SUDO "

# ──────────────────────── docker path ────────────────────────
if [ "$MODE" = "docker" ]; then

  hdr "Checking dependencies"
  ensure_docker
  ensure_docker_group
  check_ports 53 80 2222 9000

  hdr "Building and starting the appliance"
  info "First run downloads ~500 MB (golang:1.22-alpine + distroless base). Subsequent runs are cached."
  echo
  if [ -n "$DOCKER_PREFIX" ]; then
    $SUDO make docker-up
  else
    make docker-up
  fi
  echo

  # sanity
  sleep 2
  if $SUDO docker ps --format '{{.Names}}' | grep -q '^ztna-appliance$'; then
    ok "Container ztna-appliance is up."
  else
    err "Container failed to start. See logs:"
    info "    $SUDO docker compose -f deployments/docker/docker-compose.yml --profile host logs"
    exit 1
  fi

  # health
  sleep 1
  if curl -s -m 3 http://localhost:9000/api/health 2>/dev/null | grep -q '"ok"'; then
    ok "Admin API responding at :9000"
  else
    warn "Admin API not yet responsive (it may still be initializing)."
  fi

  hdr "Done"
  cat <<EOF
  ${C_BOLD}Access:${C_RST}
    Admin UI       :  http://localhost:9000
    HTTP inspector :  http://localhost:80
    SSH mock       :  ssh -p 2222 admin@localhost   (any password)
    DNS            :  dig @localhost example.com
    CLI (REPL)     :  $SUDO docker exec -it ztna-appliance /ztna-lab cli

  ${C_BOLD}Operate:${C_RST}
    Logs           :  $SUDO docker compose -f deployments/docker/docker-compose.yml --profile host logs -f
    Stop           :  make docker-down
    Restart        :  $SUDO docker restart ztna-appliance
    Rebuild        :  make docker-up

EOF
fi

# ──────────────────────── bare metal path ────────────────────────
if [ "$MODE" = "baremetal" ]; then

  hdr "Checking dependencies"
  command -v systemctl >/dev/null 2>&1 || die "systemd not found. Bare metal mode requires systemd."
  ok "systemd present"

  command -v make >/dev/null 2>&1 || pkg_install make
  ok "make present"

  ensure_docker            # needed as builder
  check_ports 53 80 2222 9000

  hdr "Building the binary"
  info "Compiling Go via Docker (no Go install needed on the host)..."
  if [ -n "$DOCKER_PREFIX" ]; then
    $SUDO make build
  else
    make build
  fi
  if [ ! -f dist/ztna-lab ]; then
    die "Build failed — dist/ztna-lab not produced."
  fi
  ok "Built: $(ls -lh dist/ztna-lab | awk '{print $5}') at dist/ztna-lab"

  hdr "Installing systemd service"
  $SUDO make install
  echo

  # sanity
  sleep 2
  if $SUDO systemctl is-active --quiet ztna-lab; then
    ok "Service ztna-lab is active."
  else
    err "Service failed to start. See:"
    info "    sudo journalctl -u ztna-lab --no-pager -n 40"
    exit 1
  fi

  if curl -s -m 3 http://localhost:9000/api/health 2>/dev/null | grep -q '"ok"'; then
    ok "Admin API responding at :9000"
  else
    warn "Admin API not yet responsive (it may still be initializing)."
  fi

  hdr "Done"
  cat <<EOF
  ${C_BOLD}Access:${C_RST}
    Admin UI       :  http://localhost:9000
    HTTP inspector :  http://localhost:80
    SSH mock       :  ssh -p 2222 admin@localhost   (any password)
    DNS            :  dig @localhost example.com
    CLI (REPL)     :  /usr/local/bin/ztna-lab cli

  ${C_BOLD}Operate:${C_RST}
    Status         :  sudo systemctl status ztna-lab
    Logs           :  sudo journalctl -u ztna-lab -f
    Restart        :  sudo systemctl restart ztna-lab
    Config         :  sudo \$EDITOR /etc/ztna-lab/config.env  (restart after edits)
    Uninstall      :  sudo make uninstall

  ${C_DIM}Docker was installed only to build the binary. You can remove it now:${C_RST}
    ${C_DIM}sudo systemctl disable --now docker  &&  sudo apt-get remove docker-ce  (or equivalent)${C_RST}

EOF
fi

ok "Setup complete."
