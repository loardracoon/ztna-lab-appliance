#!/usr/bin/env bash
# install.sh — instala o ZTNA Lab Appliance como serviço systemd.
#
# Uso (a partir do Makefile, recomendado):
#   sudo make install
#
# Uso direto:
#   sudo BINARY_SRC=./dist/ztna-lab bash deployments/linux/install.sh

set -euo pipefail

# ────────────── parâmetros ──────────────
BINARY_SRC="${BINARY_SRC:-./dist/ztna-lab}"
BINARY_DST="/usr/local/bin/ztna-lab"
SERVICE_USER="ztna-lab"
SERVICE_NAME="ztna-lab"
CONFIG_DIR="/etc/ztna-lab"
DATA_DIR="/var/lib/ztna-lab"
LOG_DIR="/var/log/ztna-lab"
UNIT_SRC="deployments/linux/ztna-lab.service"
UNIT_DST="/etc/systemd/system/ztna-lab.service"
ENV_SRC="deployments/linux/config.env.example"
ENV_DST="$CONFIG_DIR/config.env"

# ────────────── helpers ──────────────
red()    { printf "\033[31m%s\033[0m\n" "$*"; }
green()  { printf "\033[32m%s\033[0m\n" "$*"; }
yellow() { printf "\033[33m%s\033[0m\n" "$*"; }
info()   { printf "  %s\n" "$*"; }

die() { red "✗ $*"; exit 1; }

require_root() {
    [ "$(id -u)" -eq 0 ] || die "Execute como root: sudo bash $0"
}

require_systemd() {
    command -v systemctl >/dev/null 2>&1 || die "Este script requer systemd."
}

check_binary() {
    [ -f "$BINARY_SRC" ] || die "Binário não encontrado em $BINARY_SRC. Rode 'make build' primeiro."
    [ -x "$BINARY_SRC" ] || die "Binário em $BINARY_SRC não é executável."
}

check_port_conflicts() {
    local has_conflict=0
    for port in 53 80 2222 9000; do
        if ss -tulnp 2>/dev/null | grep -qE ":$port\b"; then
            yellow "⚠ Porta $port já está em uso no host:"
            ss -tulnp 2>/dev/null | grep -E ":$port\b" | sed 's/^/    /'
            has_conflict=1
        fi
    done
    if [ "$has_conflict" -eq 1 ]; then
        yellow "  Vai dar conflito ao iniciar. Pare os serviços ocupando essas portas"
        yellow "  ou ajuste ZTNA_AUTOSTART_* no $ENV_DST."
        echo
    fi
}

# ────────────── steps ──────────────
require_root
require_systemd
check_binary

green "▶ Instalando ZTNA Lab Appliance"
echo

info "Verificando conflitos de porta..."
check_port_conflicts

info "Criando usuário de sistema '$SERVICE_USER'..."
if id "$SERVICE_USER" >/dev/null 2>&1; then
    info "  já existe, ok."
else
    useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
    info "  criado."
fi

info "Criando diretórios..."
install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA_DIR"
install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$LOG_DIR"
install -d -m 0755 -o root           -g root           "$CONFIG_DIR"

info "Instalando binário em $BINARY_DST..."
install -m 0755 "$BINARY_SRC" "$BINARY_DST"

info "Instalando unit do systemd..."
install -m 0644 "$UNIT_SRC" "$UNIT_DST"

info "Instalando config.env..."
if [ -f "$ENV_DST" ]; then
    yellow "  $ENV_DST já existe, preservando. Novo template em $ENV_DST.new"
    install -m 0644 "$ENV_SRC" "$ENV_DST.new"
else
    install -m 0644 "$ENV_SRC" "$ENV_DST"
    info "  edite $ENV_DST se precisar customizar."
fi

info "Recarregando systemd..."
systemctl daemon-reload

info "Habilitando e iniciando o serviço..."
systemctl enable "$SERVICE_NAME" >/dev/null 2>&1
systemctl restart "$SERVICE_NAME"
sleep 2

if systemctl is-active --quiet "$SERVICE_NAME"; then
    green "✓ Serviço $SERVICE_NAME ativo."
else
    red "✗ Serviço falhou ao iniciar. Veja:"
    echo "    journalctl -u $SERVICE_NAME --no-pager -n 30"
    exit 1
fi

echo
green "▶ Instalação concluída"
echo
info "Painel admin :  http://$(hostname -I | awk '{print $1}'):9000"
info "Plano teste  :  http://<host>:80   |   ssh -p 2222 <host>"
info "Logs         :  journalctl -u $SERVICE_NAME -f"
info "Status       :  systemctl status $SERVICE_NAME"
info "Config       :  $ENV_DST  (reinicia o serviço após editar)"
info "CLI remota   :  /usr/local/bin/ztna-lab cli"
echo
