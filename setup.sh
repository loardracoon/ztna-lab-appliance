#!/bin/sh
# shellcheck shell=bash
# ZTNA Lab Appliance — interactive installer (v1.3)
#
# Compatível com Debian/Ubuntu/Mint, RHEL/Fedora/Rocky/Alma, openSUSE, Arch,
# e Alpine Linux. Detecta distro, init system (systemd ou OpenRC), e instala
# dependências e o appliance no modo escolhido.

# ────────── bash bootstrap ──────────
# Re-executa sob bash, instalando-o se necessário (Alpine não vem com bash).
if [ -z "${ZTNA_BOOT:-}" ]; then
    if ! command -v bash >/dev/null 2>&1; then
        echo "Bash não encontrado. Tentando instalar..."
        if command -v apk >/dev/null 2>&1; then
            apk add --no-cache bash >/dev/null 2>&1 || {
                echo "Falha ao instalar bash. Tente: apk add bash" >&2
                exit 1
            }
        elif command -v apt-get >/dev/null 2>&1; then
            apt-get install -y bash >/dev/null 2>&1 || exit 1
        else
            echo "Este installer precisa de bash, que não está instalado e não pude instalar automaticamente." >&2
            exit 1
        fi
    fi
    ZTNA_BOOT=1 exec bash "$0" "$@"
fi

# ────────── a partir daqui rodamos sob bash ──────────
set -euo pipefail

# ────────── cores ──────────
if [ -t 1 ]; then
    C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YLW=$'\033[33m'
    C_DIM=$'\033[2m';  C_BOLD=$'\033[1m'; C_RST=$'\033[0m'
else
    C_RED=""; C_GRN=""; C_YLW=""; C_DIM=""; C_BOLD=""; C_RST=""
fi
info() { printf "  %s\n" "$*"; }
ok()   { printf "  ${C_GRN}✓${C_RST} %s\n" "$*"; }
warn() { printf "  ${C_YLW}⚠${C_RST} %s\n" "$*"; }
err()  { printf "  ${C_RED}✗${C_RST} %s\n" "$*" >&2; }
hdr()  { printf "\n${C_BOLD}▶ %s${C_RST}\n" "$*"; }
die()  { err "$*"; exit 1; }

# ────────── pre-checks ──────────
[ "$(uname -s)" = "Linux" ] || die "This installer only supports Linux."
[ -f /etc/os-release ] || die "Cannot detect distribution (/etc/os-release missing)."
[ -f Makefile ] && [ -f go.mod ] || die "Run this script from the project root."

# shellcheck disable=SC1091
. /etc/os-release
DISTRO_ID="${ID:-unknown}"

# Package manager
if   command -v apt-get >/dev/null 2>&1; then PKG="apt"
elif command -v dnf     >/dev/null 2>&1; then PKG="dnf"
elif command -v yum     >/dev/null 2>&1; then PKG="yum"
elif command -v zypper  >/dev/null 2>&1; then PKG="zypper"
elif command -v pacman  >/dev/null 2>&1; then PKG="pacman"
elif command -v apk     >/dev/null 2>&1; then PKG="apk"
else PKG=""
fi

# Init system
if command -v rc-update >/dev/null 2>&1 && [ "$DISTRO_ID" = "alpine" ]; then
    INIT="openrc"
elif command -v systemctl >/dev/null 2>&1; then
    INIT="systemd"
elif command -v rc-update >/dev/null 2>&1; then
    INIT="openrc"
else
    INIT="unknown"
fi

# Sudo
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
    command -v sudo >/dev/null 2>&1 || die "Not root and sudo is unavailable. Re-run as root or install sudo."
    SUDO="sudo"
fi

# ────────── helpers ──────────
pkg_install() {
    case "$PKG" in
        apt)     $SUDO apt-get update -qq && $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y "$@" ;;
        dnf|yum) $SUDO "$PKG" install -y "$@" ;;
        zypper)  $SUDO zypper -n install "$@" ;;
        pacman)  $SUDO pacman -Sy --noconfirm "$@" ;;
        apk)     $SUDO apk add --no-cache "$@" ;;
        *)       die "No supported package manager found." ;;
    esac
}

ensure_curl() {
    command -v curl >/dev/null 2>&1 || pkg_install curl
}

# No Alpine, vários pacotes (docker, docker-cli-compose, libcap utils) vivem no
# repositório "community" que pode vir desabilitado em instalações mínimas/Virt.
# Esta função garante que ele está habilitado antes de qualquer apk add que
# dependa dele.
ensure_alpine_community() {
    [ "$DISTRO_ID" = "alpine" ] || return 0

    # Já habilitado (linha não-comentada contendo /community)?
    if grep -qE '^[^#]+/community([[:space:]]|$)' /etc/apk/repositories 2>/dev/null; then
        return 0
    fi

    info "Enabling Alpine community repository..."

    # Caso 1: linha está lá mas comentada — descomentar.
    if grep -qE '^#.*\/community([[:space:]]|$)' /etc/apk/repositories 2>/dev/null; then
        $SUDO sed -i 's|^#\([[:space:]]*[A-Za-z].*\/community\)|\1|' /etc/apk/repositories
    fi

    # Caso 2: ainda não tem (instalação muito enxuta) — adicionar.
    if ! grep -qE '^[^#]+/community([[:space:]]|$)' /etc/apk/repositories 2>/dev/null; then
        # Tenta derivar a versão do branch já em uso (vX.Y) ou cai pra latest-stable
        local branch
        branch=$(grep -oE 'v[0-9]+\.[0-9]+' /etc/apk/repositories 2>/dev/null | head -1)
        branch="${branch:-latest-stable}"
        echo "https://dl-cdn.alpinelinux.org/alpine/${branch}/community" \
            | $SUDO tee -a /etc/apk/repositories >/dev/null
    fi

    # Atualiza o índice pra refletir a mudança.
    $SUDO apk update >/dev/null 2>&1 || warn "apk update failed — check network."
    ok "Alpine community repo enabled."
}

ensure_docker() {
    if command -v docker >/dev/null 2>&1; then
        ok "Docker: $(docker --version | head -1)"
    else
        warn "Docker not installed."
        read -r -p "  Install Docker now? [Y/n] " ans
        case "${ans:-Y}" in
            [Yy]*) ;;
            *) die "Cannot continue without Docker." ;;
        esac

        case "$DISTRO_ID" in
            alpine)
                # No Alpine: docker e compose vivem no repo community.
                ensure_alpine_community
                pkg_install docker docker-cli-compose
                $SUDO rc-update add docker default >/dev/null 2>&1 || true
                $SUDO service docker start
                sleep 3
                ;;
            *)
                # Demais distros: convenience script oficial (lida com tudo internamente)
                ensure_curl
                curl -fsSL https://get.docker.com -o /tmp/get-docker.sh
                $SUDO sh /tmp/get-docker.sh
                rm -f /tmp/get-docker.sh
                $SUDO systemctl enable --now docker 2>/dev/null || true
                ;;
        esac
        ok "Docker installed."
    fi

    # Compose plugin
    if docker compose version >/dev/null 2>&1; then
        ok "docker compose (plugin) present"
    elif command -v docker-compose >/dev/null 2>&1; then
        ok "docker-compose (standalone) present"
    else
        warn "Compose plugin missing — installing..."
        case "$PKG" in
            apt) pkg_install docker-compose-plugin ;;
            apk) pkg_install docker-cli-compose ;;
            dnf|yum) pkg_install docker-compose-plugin 2>/dev/null || true ;;
            *) warn "Install docker compose manually." ;;
        esac
    fi

    # Daemon up?
    if ! $SUDO docker info >/dev/null 2>&1; then
        warn "Docker daemon not responding — starting..."
        if [ "$INIT" = "openrc" ]; then
            $SUDO service docker start || die "Failed to start docker."
        else
            $SUDO systemctl start docker || die "Failed to start docker."
        fi
        sleep 2
    fi
}

ensure_docker_group() {
    [ -z "$SUDO" ] && return                            # root já tem
    id -nG "$USER" | grep -qw docker && return          # já no grupo
    warn "User '$USER' is not in the 'docker' group."
    read -r -p "  Add '$USER' to 'docker' group? (needs re-login afterwards) [Y/n] " ans
    case "${ans:-Y}" in
        [Yy]*)
            if [ "$DISTRO_ID" = "alpine" ]; then
                $SUDO addgroup "$USER" docker
            else
                $SUDO usermod -aG docker "$USER"
            fi
            info "Added. ${C_BOLD}Re-login required for this to take effect.${C_RST}"
            info "For now, this script will use sudo for docker commands."
            ;;
    esac
}

check_ports() {
    # iproute2 (ss) ou busybox netstat
    local checker=""
    if command -v ss >/dev/null 2>&1; then
        checker="ss -tulnH"
    elif command -v netstat >/dev/null 2>&1; then
        checker="netstat -tuln"
    else
        case "$PKG" in
            apk) pkg_install iproute2 ;;
            *)   pkg_install iproute2 2>/dev/null || true ;;
        esac
        if command -v ss >/dev/null 2>&1; then
            checker="ss -tulnH"
        else
            warn "Couldn't install ss/netstat — skipping port conflict check."
            return
        fi
    fi

    local conflicts=""
    for p in "$@"; do
        if $checker 2>/dev/null | grep -qE "[:.]${p}\b"; then
            conflicts="$conflicts $p"
        fi
    done
    if [ -z "$conflicts" ]; then
        ok "Ports free: $*"
        return
    fi
    warn "Ports already in use:$conflicts"
    for p in $conflicts; do
        $checker 2>/dev/null | grep -E "[:.]${p}\b" | head -2 | sed 's/^/    /' || true
    done
    echo
    info "Common culprits:"
    info "  port 53  → systemd-resolved (disable: systemctl disable --now systemd-resolved)"
    info "  port 80  → nginx, apache"
    echo
    read -r -p "  Continue anyway (the appliance will fail to bind these)? [y/N] " ans
    case "${ans:-N}" in
        [Yy]*) ;;
        *) die "Aborted." ;;
    esac
}

# ────────── banner ──────────
clear 2>/dev/null || true
cat <<EOF

${C_BOLD}ZTNA Lab Appliance${C_RST} — installer · v1.3.1

Detected environment:
  Distribution :  ${PRETTY_NAME:-$DISTRO_ID}
  Package mgr  :  ${PKG:-none}
  Init system  :  ${INIT}
  Privilege    :  $([ -z "$SUDO" ] && echo "root" || echo "$USER (sudo)")

EOF

if [ "$INIT" = "unknown" ]; then
    die "Neither systemd nor OpenRC detected. Cannot proceed."
fi

# ────────── menu ──────────
hdr "Choose deployment mode"
cat <<EOF
  ${C_BOLD}1)${C_RST} Docker  (recommended)
     Container deploy. Fastest, isolated, easy to remove.
     Requires: Docker engine + compose plugin (will offer to install).

  ${C_BOLD}2)${C_RST} Bare metal  (${INIT} service)
     Native install at /usr/local/bin/ + /etc/ztna-lab/.
     Builds the binary using Docker, then runs it directly on the host.
     Requires: Docker (build only — can be removed after).

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

# ────────── docker path ──────────
if [ "$MODE" = "docker" ]; then

    hdr "Checking dependencies"
    ensure_docker
    ensure_docker_group
    check_ports 53 80 2222 9000

    hdr "Building and starting the appliance"
    info "First run downloads ~500 MB (golang:1.22-alpine + distroless base). Subsequent runs are cached."
    echo
    $SUDO make docker-up
    echo

    sleep 2
    if $SUDO docker ps --format '{{.Names}}' | grep -q '^ztna-appliance$'; then
        ok "Container ztna-appliance is up."
    else
        err "Container failed to start. See logs:"
        info "    $SUDO docker compose -f deployments/docker/docker-compose.yml --profile host logs"
        exit 1
    fi

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

# ────────── bare metal path ──────────
if [ "$MODE" = "baremetal" ]; then

    hdr "Checking dependencies"
    case "$INIT" in
        systemd) ok "systemd present" ;;
        openrc)  ok "OpenRC present" ;;
        *)       die "Unsupported init system: $INIT" ;;
    esac

    command -v make >/dev/null 2>&1 || pkg_install make
    ok "make present"

    ensure_docker
    check_ports 53 80 2222 9000

    hdr "Building the binary"
    info "Compiling Go via Docker (no Go install needed on the host)..."
    $SUDO make build
    [ -f dist/ztna-lab ] || die "Build failed — dist/ztna-lab not produced."
    ok "Built: $(ls -lh dist/ztna-lab | awk '{print $5}') at dist/ztna-lab"

    hdr "Installing as $INIT service"
    if [ "$INIT" = "openrc" ]; then
        $SUDO make install-alpine
    else
        $SUDO make install
    fi
    echo

    sleep 2
    if [ "$INIT" = "systemd" ]; then
        if $SUDO systemctl is-active --quiet ztna-lab; then
            ok "Service ztna-lab is active."
        else
            err "Service failed to start. See:  sudo journalctl -u ztna-lab --no-pager -n 40"
            exit 1
        fi
    else
        if $SUDO rc-service ztna-lab status 2>/dev/null | grep -q started; then
            ok "Service ztna-lab is active."
        else
            err "Service failed to start. See:  cat /var/log/ztna-lab/stderr.log"
            exit 1
        fi
    fi

    if curl -s -m 3 http://localhost:9000/api/health 2>/dev/null | grep -q '"ok"'; then
        ok "Admin API responding at :9000"
    else
        warn "Admin API not yet responsive (it may still be initializing)."
    fi

    hdr "Done"
    if [ "$INIT" = "systemd" ]; then
        cat <<EOF
  ${C_BOLD}Access:${C_RST}
    Admin UI       :  http://localhost:9000
    HTTP inspector :  http://localhost:80
    SSH mock       :  ssh -p 2222 admin@localhost
    CLI (REPL)     :  /usr/local/bin/ztna-lab cli

  ${C_BOLD}Operate:${C_RST}
    Status         :  sudo systemctl status ztna-lab
    Logs           :  sudo journalctl -u ztna-lab -f
    Restart        :  sudo systemctl restart ztna-lab
    Config         :  sudo \$EDITOR /etc/ztna-lab/config.env  (restart after edits)
    Uninstall      :  sudo make uninstall

  ${C_DIM}Docker was installed only to build the binary. You can remove it now.${C_RST}

EOF
    else
        cat <<EOF
  ${C_BOLD}Access:${C_RST}
    Admin UI       :  http://localhost:9000
    HTTP inspector :  http://localhost:80
    SSH mock       :  ssh -p 2222 admin@localhost
    CLI (REPL)     :  /usr/local/bin/ztna-lab cli

  ${C_BOLD}Operate:${C_RST}
    Status         :  rc-service ztna-lab status
    Logs           :  tail -f /var/log/ztna-lab/stdout.log
    Restart        :  rc-service ztna-lab restart
    Config         :  \$EDITOR /etc/ztna-lab/config.env  (rc-service ztna-lab restart)
    Uninstall      :  sudo make uninstall-alpine

  ${C_DIM}Docker was installed only to build the binary. You can remove it now:${C_RST}
    ${C_DIM}service docker stop  &&  rc-update del docker default  &&  apk del docker${C_RST}

EOF
    fi
fi

ok "Setup complete."
