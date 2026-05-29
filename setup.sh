#!/bin/sh
# shellcheck shell=bash
# ZTNA Lab Appliance — Automated Git Installer & Optimizer
#
# Clona o repositório, para serviços existentes, instala o appliance
# e remove ferramentas de build para minimizar o footprint da VM.
#
# Compatível: Alpine Linux, Debian, Ubuntu, CentOS / RHEL / Rocky / Alma e derivados.
#
# Uso direto:    bash setup.sh
# Uso via pipe:  curl -fsSL <url>/setup.sh | bash

# ────────── bash bootstrap ──────────
# Re-executa sob bash. Alpine mínimo não inclui bash; instala se necessário.
if [ -z "${ZTNA_BOOT:-}" ]; then
    if ! command -v bash >/dev/null 2>&1; then
        echo "Bash não encontrado. Instalando..." >&2
        if command -v apk >/dev/null 2>&1; then
            apk add --no-cache bash >/dev/null 2>&1 \
                || { echo "Falha ao instalar bash. Execute: apk add bash" >&2; exit 1; }
        elif command -v apt-get >/dev/null 2>&1; then
            apt-get install -y bash >/dev/null 2>&1 || exit 1
        else
            echo "Instale o bash manualmente e tente novamente." >&2
            exit 1
        fi
    fi
    ZTNA_BOOT=1 exec bash "$0" "$@"
fi

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
[ "$(uname -s)" = "Linux" ] || die "Este instalador suporta apenas Linux."
[ -f /etc/os-release ]      || die "Não foi possível detectar a distribuição (/etc/os-release ausente)."

# shellcheck disable=SC1091
. /etc/os-release
DISTRO_ID="${ID:-unknown}"

# Gerenciador de pacotes
if   command -v apt-get >/dev/null 2>&1; then PKG="apt"
elif command -v dnf     >/dev/null 2>&1; then PKG="dnf"
elif command -v yum     >/dev/null 2>&1; then PKG="yum"
elif command -v zypper  >/dev/null 2>&1; then PKG="zypper"
elif command -v pacman  >/dev/null 2>&1; then PKG="pacman"
elif command -v apk     >/dev/null 2>&1; then PKG="apk"
else PKG=""
fi

# Init system — Alpine tem prioridade para evitar falsa detecção de systemd em WSL
if [ "$DISTRO_ID" = "alpine" ] && command -v rc-update >/dev/null 2>&1; then
    INIT="openrc"
elif command -v systemctl >/dev/null 2>&1; then
    INIT="systemd"
elif command -v rc-update >/dev/null 2>&1; then
    INIT="openrc"
else
    INIT="unknown"
fi

# Privilégios
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
    command -v sudo >/dev/null 2>&1 || die "Execute como root ou instale o sudo."
    SUDO="sudo"
fi

# ────────── helpers de pacote ──────────
pkg_install() {
    case "$PKG" in
        apt)     $SUDO apt-get update -qq \
                     && $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y "$@" ;;
        dnf|yum) $SUDO "$PKG" install -y "$@" ;;
        zypper)  $SUDO zypper -n install "$@" ;;
        pacman)  $SUDO pacman -Sy --noconfirm "$@" ;;
        apk)     $SUDO apk add --no-cache "$@" ;;
        *)       die "Gerenciador de pacotes não suportado." ;;
    esac
}

pkg_uninstall() {
    [ $# -eq 0 ] && return
    case "$PKG" in
        apt)     $SUDO env DEBIAN_FRONTEND=noninteractive apt-get purge -y "$@" 2>/dev/null || true
                 $SUDO apt-get autoremove -y 2>/dev/null || true ;;
        dnf|yum) $SUDO "$PKG" remove -y "$@" 2>/dev/null || true ;;
        zypper)  $SUDO zypper -n rm "$@" 2>/dev/null || true ;;
        pacman)  $SUDO pacman -Rns --noconfirm "$@" 2>/dev/null || true ;;
        apk)     $SUDO apk del "$@" 2>/dev/null || true ;;
    esac
}

# ────────── funções auxiliares ──────────

ensure_git_and_curl() {
    local missing=""
    command -v git  >/dev/null 2>&1 || missing="git"
    command -v curl >/dev/null 2>&1 || missing="${missing:+$missing }curl"
    if [ -n "$missing" ]; then
        info "Instalando dependências base ($missing)..."
        # shellcheck disable=SC2086
        pkg_install $missing
    fi
}

# No Alpine, docker fica no repositório community que pode vir comentado.
ensure_alpine_community() {
    [ "$DISTRO_ID" = "alpine" ] || return 0
    grep -qE '^[^#]+/community([[:space:]]|$)' /etc/apk/repositories 2>/dev/null && return 0

    info "Habilitando repositório community do Alpine..."

    if grep -qE '^#.*\/community([[:space:]]|$)' /etc/apk/repositories 2>/dev/null; then
        $SUDO sed -i 's|^#\([[:space:]]*[A-Za-z].*\/community\)|\1|' /etc/apk/repositories
    else
        local branch
        branch=$(grep -oE 'v[0-9]+\.[0-9]+' /etc/apk/repositories 2>/dev/null | head -1)
        echo "https://dl-cdn.alpinelinux.org/alpine/${branch:-latest-stable}/community" \
            | $SUDO tee -a /etc/apk/repositories >/dev/null
    fi

    $SUDO apk update >/dev/null 2>&1 || warn "apk update falhou — verifique a rede."
    ok "Repositório community habilitado."
}

ensure_docker() {
    if ! command -v docker >/dev/null 2>&1; then
        info "Instalando Docker..."
        case "$DISTRO_ID" in
            alpine)
                ensure_alpine_community
                pkg_install docker docker-cli-compose
                $SUDO rc-update add docker default >/dev/null 2>&1 || true
                # Alpine usa OpenRC — 'rc-service', não 'service'
                $SUDO rc-service docker start
                sleep 3
                ;;
            *)
                ensure_git_and_curl
                curl -fsSL https://get.docker.com -o /tmp/get-docker.sh
                $SUDO sh /tmp/get-docker.sh >/dev/null
                rm -f /tmp/get-docker.sh
                $SUDO systemctl enable --now docker 2>/dev/null || true
                ;;
        esac
        ok "Docker instalado."
    fi

    # Verifica plugin compose (necessário para docker compose e para o Makefile).
    if ! $SUDO docker compose version >/dev/null 2>&1 \
       && ! command -v docker-compose >/dev/null 2>&1; then
        info "Instalando plugin docker compose..."
        case "$PKG" in
            apt)     pkg_install docker-compose-plugin ;;
            apk)     pkg_install docker-cli-compose ;;
            dnf|yum) $SUDO "$PKG" install -y docker-compose-plugin 2>/dev/null \
                         || warn "Instale o docker compose plugin manualmente." ;;
            *)       warn "Instale o docker compose plugin manualmente." ;;
        esac
    fi

    # Garante que o daemon está respondendo.
    if ! $SUDO docker info >/dev/null 2>&1; then
        info "Iniciando daemon do Docker..."
        if [ "$INIT" = "openrc" ]; then
            $SUDO rc-service docker start || die "Falha ao iniciar o Docker."
        else
            $SUDO systemctl start docker  || die "Falha ao iniciar o Docker."
        fi
        sleep 2
        $SUDO docker info >/dev/null 2>&1 \
            || die "Daemon do Docker não respondeu. Verifique: $SUDO docker info"
    fi
}

# Para usuários não-root: tenta adicionar ao grupo docker para evitar sudo permanente.
ensure_docker_group() {
    [ -z "$SUDO" ] && return 0
    id -nG "$USER" 2>/dev/null | grep -qw docker && return 0
    warn "Usuário '$USER' não está no grupo docker."
    if [ "$DISTRO_ID" = "alpine" ]; then
        $SUDO addgroup "$USER" docker 2>/dev/null || true
    else
        $SUDO usermod -aG docker "$USER" 2>/dev/null || true
    fi
    info "Adicionado ao grupo docker. ${C_BOLD}Faça re-login para efeito permanente.${C_RST}"
    info "Esta sessão continuará usando sudo para comandos Docker."
}

# Avisa sobre portas em uso que impediriam o appliance de subir.
check_ports() {
    local checker="" conflicts=""

    if command -v ss >/dev/null 2>&1; then
        checker="ss -tulnH"
    elif command -v netstat >/dev/null 2>&1; then
        checker="netstat -tuln"
    else
        warn "ss/netstat não disponível — pulando verificação de portas."
        return 0
    fi

    for p in "$@"; do
        if $checker 2>/dev/null | grep -qE "[:.]${p}([[:space:]]|$)"; then
            conflicts="${conflicts:+$conflicts }$p"
        fi
    done

    if [ -z "$conflicts" ]; then
        ok "Portas livres: $*"
    else
        warn "Portas já em uso: $conflicts"
        info "O appliance falhará ao tentar ocupar essas portas."
        info "Pare os serviços conflitantes ou desative via ZTNA_AUTOSTART_* em config.env."
    fi
}

# 1. PARAR SERVIÇOS EXISTENTES
stop_existing() {
    hdr "Verificando instalações existentes"
    local found=0

    if [ "$INIT" = "systemd" ] && systemctl is-active --quiet ztna-lab 2>/dev/null; then
        warn "Serviço systemd 'ztna-lab' em execução. Parando..."
        $SUDO systemctl stop ztna-lab
        found=1
    fi

    if [ "$INIT" = "openrc" ] && rc-service ztna-lab status 2>/dev/null | grep -q "started"; then
        warn "Serviço openrc 'ztna-lab' em execução. Parando..."
        $SUDO rc-service ztna-lab stop
        found=1
    fi

    if command -v docker >/dev/null 2>&1; then
        if $SUDO docker ps -q -f "name=ztna-appliance" 2>/dev/null | grep -q .; then
            warn "Container 'ztna-appliance' em execução. Removendo..."
            $SUDO docker rm -f ztna-appliance >/dev/null
            found=1
        fi
    fi

    [ "$found" -eq 0 ] && ok "Nenhum serviço conflitante encontrado." \
                       || ok "Serviços anteriores parados."
}

# ────────── banner ──────────
clear 2>/dev/null || true
cat <<EOF

${C_BOLD}ZTNA Lab Appliance${C_RST} — Auto-Installer & Optimizer

  Distribuição :  ${PRETTY_NAME:-$DISTRO_ID}
  Pacotes      :  ${PKG:-nenhum detectado}
  Init system  :  ${INIT}
  Privilégio   :  $([ -z "$SUDO" ] && echo "root" || echo "$USER (sudo)")

EOF

[ "$INIT" = "unknown" ] && die "Init system não reconhecido (nem systemd nem OpenRC). Instale manualmente."
[ -z "$PKG" ]           && die "Nenhum gerenciador de pacotes suportado encontrado."

stop_existing

# ────────── menu ──────────
hdr "Escolha o modo de deployment"
cat <<EOF
  ${C_BOLD}1)${C_RST} Docker
     Deploy isolado via container. (Mantém o Docker rodando na VM).

  ${C_BOLD}2)${C_RST} Bare metal ${C_DIM}(footprint mínimo)${C_RST}
     Usa Docker apenas para compilar o binário em Go.
     Depois instala o binário nativamente e ${C_RED}remove o Docker${C_RST} e os
     arquivos fonte para deixar a VM extremamente leve.

  ${C_BOLD}3)${C_RST} Cancelar
EOF

while true; do
    # "< /dev/tty" garante que read funciona mesmo quando o script é
    # obtido via pipe (curl ... | bash), redirecionando do terminal real.
    read -r -p "  Escolha [1/2/3]: " choice < /dev/tty
    case "$choice" in
        1) MODE="docker";    break ;;
        2) MODE="baremetal"; break ;;
        3) info "Cancelado."; exit 0 ;;
        *) warn "Escolha inválida — digite 1, 2 ou 3." ;;
    esac
done

# 2. CLONAR REPOSITÓRIO
hdr "Clonando Repositório Oficial"
REPO_URL="https://github.com/loardracoon/ztna-lab-appliance.git"
REPO_DIR="/opt/ztna-lab-appliance"

ensure_git_and_curl

if [ -d "$REPO_DIR" ]; then
    info "Removendo repositório antigo em $REPO_DIR..."
    $SUDO rm -rf "$REPO_DIR"
fi

info "Clonando em $REPO_DIR..."
$SUDO git clone -q "$REPO_URL" "$REPO_DIR"
cd "$REPO_DIR" || die "Falha ao acessar o diretório clonado."
ok "Repositório preparado."

# ════════════════════════════════════════════════
#  MODO DOCKER
# ════════════════════════════════════════════════
if [ "$MODE" = "docker" ]; then
    hdr "Preparando Docker Deployment"

    ensure_docker
    ensure_docker_group
    check_ports 53 80 2222 9000

    info "Iniciando appliance (primeira execução baixa ~500 MB de imagens)..."
    $SUDO make docker-up
    sleep 2

    # Verifica container antes da API — app pode ter falhado mesmo com daemon ok.
    if ! $SUDO docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^ztna-appliance$'; then
        err "Container falhou ao iniciar. Veja os logs:"
        info "  $SUDO docker compose -f $REPO_DIR/deployments/docker/docker-compose.yml --profile host logs"
        exit 1
    fi
    ok "Container ztna-appliance em execução."

    sleep 1
    if curl -s -m 5 http://localhost:9000/api/health 2>/dev/null | grep -q '"ok"'; then
        ok "Admin API ativa em :9000"
    else
        warn "Admin API ainda não respondeu (pode estar inicializando)."
    fi

    hdr "Concluído (Modo Docker)"
    cat <<EOF
  ${C_BOLD}Acesso:${C_RST}
    Admin UI       :  http://localhost:9000
    HTTP inspector :  http://localhost:80
    SSH mock       :  ssh -p 2222 admin@localhost   (qualquer senha)
    DNS            :  dig @localhost example.com
    CLI (REPL)     :  $SUDO docker exec -it ztna-appliance /ztna-lab cli

  ${C_BOLD}Operação:${C_RST}
    Logs     :  $SUDO docker compose -f $REPO_DIR/deployments/docker/docker-compose.yml --profile host logs -f
    Stop     :  $SUDO docker stop ztna-appliance
    Restart  :  $SUDO docker restart ztna-appliance
    Rebuild  :  cd $REPO_DIR && $SUDO make docker-up
EOF
    exit 0
fi

# ════════════════════════════════════════════════
#  MODO BARE METAL (Minimizando Footprint)
# ════════════════════════════════════════════════
if [ "$MODE" = "baremetal" ]; then
    hdr "Preparando Build Baremetal"

    command -v make >/dev/null 2>&1 || pkg_install make
    ensure_docker
    check_ports 53 80 2222 9000

    info "Compilando binário via Docker..."
    $SUDO make build
    [ -f dist/ztna-lab ] || die "Falha no build do binário."
    ok "Binário compilado: $(ls -lh dist/ztna-lab | awk '{print $5}')."

    hdr "Instalando serviço nativo"
    if [ "$INIT" = "openrc" ]; then
        $SUDO make install-alpine
    else
        $SUDO make install
    fi
    sleep 2

    # Verifica que o serviço subiu antes de desinstalar as ferramentas de build.
    if [ "$INIT" = "systemd" ]; then
        $SUDO systemctl is-active --quiet ztna-lab \
            || { err "Serviço falhou. Veja: sudo journalctl -u ztna-lab --no-pager -n 40"; exit 1; }
    else
        rc-service ztna-lab status 2>/dev/null | grep -q "started" \
            || { err "Serviço falhou. Veja: cat /var/log/ztna-lab/stderr.log"; exit 1; }
    fi
    ok "Serviço ztna-lab ativo."

    if curl -s -m 5 http://localhost:9000/api/health 2>/dev/null | grep -q '"ok"'; then
        ok "Admin API ativa em :9000"
    else
        warn "Admin API ainda não respondeu (pode estar inicializando)."
    fi

    # 3. LIMPEZA / FOOTPRINT (Remover tudo que não é necessário)
    hdr "Minimizando Footprint da VM (Limpando recursos de build)"

    info "Parando daemon do Docker..."
    if [ "$INIT" = "openrc" ]; then
        # Alpine usa OpenRC — 'rc-service', não 'service'
        $SUDO rc-service docker stop  2>/dev/null || true
        $SUDO rc-update del docker default 2>/dev/null || true
    else
        $SUDO systemctl stop    docker docker.socket 2>/dev/null || true
        $SUDO systemctl disable docker docker.socket 2>/dev/null || true
    fi

    info "Removendo pacotes (Docker, Make, Git)..."
    case "$PKG" in
        apt) pkg_uninstall \
                docker-ce docker-ce-cli containerd.io \
                docker-buildx-plugin docker-compose-plugin \
                docker.io docker-compose make git ;;
        apk) pkg_uninstall docker docker-cli-compose make git ;;
        dnf|yum) pkg_uninstall \
                    docker-ce docker-ce-cli containerd.io \
                    docker-buildx-plugin docker-compose-plugin make git ;;
        *) pkg_uninstall docker make git ;;
    esac

    info "Limpando cache de imagens Docker e o código-fonte clonado..."
    $SUDO rm -rf /var/lib/docker
    # rm -f no socket (não rm -rf — é um arquivo especial, não um diretório)
    $SUDO rm -f  /run/docker.sock /var/run/docker.sock
    cd /
    $SUDO rm -rf "$REPO_DIR"

    ok "Limpeza concluída! A VM agora roda apenas o essencial."

    hdr "Concluído (Modo Baremetal Leve)"
    if [ "$INIT" = "systemd" ]; then
        cat <<EOF
  ${C_BOLD}Acesso:${C_RST}
    Admin UI       :  http://localhost:9000
    HTTP inspector :  http://localhost:80
    SSH mock       :  ssh -p 2222 admin@localhost
    CLI (REPL)     :  /usr/local/bin/ztna-lab cli

  ${C_BOLD}Operação:${C_RST}
    Status    :  sudo systemctl status ztna-lab
    Logs      :  sudo journalctl -u ztna-lab -f
    Restart   :  sudo systemctl restart ztna-lab
    Config    :  sudo \$EDITOR /etc/ztna-lab/config.env  (restart após editar)

  ${C_BOLD}Desinstalar:${C_RST}
    sudo systemctl disable --now ztna-lab
    sudo rm -f /usr/local/bin/ztna-lab /etc/systemd/system/ztna-lab.service
    sudo rm -rf /etc/ztna-lab /var/lib/ztna-lab /var/log/ztna-lab
    sudo systemctl daemon-reload
EOF
    else
        cat <<EOF
  ${C_BOLD}Acesso:${C_RST}
    Admin UI       :  http://localhost:9000
    HTTP inspector :  http://localhost:80
    SSH mock       :  ssh -p 2222 admin@localhost
    CLI (REPL)     :  /usr/local/bin/ztna-lab cli

  ${C_BOLD}Operação:${C_RST}
    Status    :  rc-service ztna-lab status
    Logs      :  tail -f /var/log/ztna-lab/stdout.log
    Restart   :  rc-service ztna-lab restart
    Config    :  \$EDITOR /etc/ztna-lab/config.env  (restart após editar)

  ${C_BOLD}Desinstalar:${C_RST}
    rc-service ztna-lab stop
    rc-update del ztna-lab default
    rm -f /usr/local/bin/ztna-lab /etc/init.d/ztna-lab
    rm -rf /etc/ztna-lab /var/lib/ztna-lab /var/log/ztna-lab
EOF
    fi
fi

ok "Setup concluído."
