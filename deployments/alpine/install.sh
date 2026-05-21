#!/bin/sh
# install.sh — instala o ZTNA Lab Appliance no Alpine Linux (OpenRC).
#
# Uso (a partir do Makefile):
#   sudo make install-alpine
#
# Uso direto:
#   sudo BINARY_SRC=./dist/ztna-lab sh deployments/alpine/install.sh

set -eu

# ─── parâmetros ───
BINARY_SRC="${BINARY_SRC:-./dist/ztna-lab}"
BINARY_DST="/usr/local/bin/ztna-lab"
SERVICE_USER="ztna-lab"
SERVICE_NAME="ztna-lab"
CONFIG_DIR="/etc/ztna-lab"
DATA_DIR="/var/lib/ztna-lab"
LOG_DIR="/var/log/ztna-lab"
INITSCRIPT_SRC="deployments/alpine/ztna-lab.openrc"
INITSCRIPT_DST="/etc/init.d/ztna-lab"
ENV_SRC="deployments/linux/config.env.example"
ENV_DST="$CONFIG_DIR/config.env"

# ─── helpers ───
red()    { printf "\033[31m%s\033[0m\n" "$*"; }
green()  { printf "\033[32m%s\033[0m\n" "$*"; }
yellow() { printf "\033[33m%s\033[0m\n" "$*"; }
info()   { printf "  %s\n" "$*"; }

die() { red "✗ $*" >&2; exit 1; }

# ─── pre-flight ───
[ "$(id -u)" -eq 0 ] || die "Execute como root: sudo sh $0"
command -v rc-update >/dev/null 2>&1 || die "OpenRC não detectado. Este script é para Alpine. Para systemd, use deployments/linux/install.sh."
[ -f "$BINARY_SRC" ] || die "Binário não encontrado em $BINARY_SRC. Rode 'make build' primeiro."
[ -x "$BINARY_SRC" ] || die "Binário em $BINARY_SRC não é executável."

green "▶ Instalando ZTNA Lab Appliance no Alpine (OpenRC)"
echo

# ─── checa portas ocupadas ───
info "Verificando conflitos de porta..."
conflicts=""
for p in 53 80 2222 9000; do
    if netstat -tuln 2>/dev/null | grep -qE "[:.]${p}\b" || \
       (command -v ss >/dev/null 2>&1 && ss -tulnH 2>/dev/null | grep -qE "[:.]${p}\b"); then
        conflicts="$conflicts $p"
    fi
done
if [ -n "$conflicts" ]; then
    yellow "⚠ Portas em uso:$conflicts"
    info "Vai dar conflito ao iniciar. Pare os serviços ocupando essas portas"
    info "ou ajuste ZTNA_AUTOSTART_* no $ENV_DST."
    echo
fi

# ─── libcap pra setcap ───
if ! command -v setcap >/dev/null 2>&1; then
    info "Instalando libcap (para setcap)..."
    apk add --no-cache libcap >/dev/null
fi

# ─── usuário de sistema ───
info "Criando usuário de sistema '$SERVICE_USER'..."
if id "$SERVICE_USER" >/dev/null 2>&1; then
    info "  já existe, ok."
else
    addgroup -S "$SERVICE_USER"
    adduser -S -G "$SERVICE_USER" -H -s /sbin/nologin "$SERVICE_USER"
    info "  criado."
fi

# ─── diretórios ───
info "Criando diretórios..."
install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA_DIR"
install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$LOG_DIR"
install -d -m 0755 -o root -g root "$CONFIG_DIR"

# ─── binário ───
info "Instalando binário em $BINARY_DST..."
install -m 0755 "$BINARY_SRC" "$BINARY_DST"
setcap 'cap_net_bind_service=+ep' "$BINARY_DST"

# ─── initscript ───
info "Instalando initscript OpenRC..."
install -m 0755 "$INITSCRIPT_SRC" "$INITSCRIPT_DST"

# ─── config ───
info "Instalando config.env..."
if [ -f "$ENV_DST" ]; then
    yellow "  $ENV_DST já existe, preservando. Novo template em $ENV_DST.new"
    install -m 0644 "$ENV_SRC" "$ENV_DST.new"
else
    install -m 0644 "$ENV_SRC" "$ENV_DST"
    info "  edite $ENV_DST se precisar customizar."
fi

# ─── enable + start ───
info "Habilitando e iniciando o serviço..."
rc-update add "$SERVICE_NAME" default >/dev/null 2>&1 || true
rc-service "$SERVICE_NAME" restart >/dev/null 2>&1 || rc-service "$SERVICE_NAME" start

sleep 2
if rc-service "$SERVICE_NAME" status 2>/dev/null | grep -q "started"; then
    green "✓ Serviço $SERVICE_NAME ativo."
else
    red "✗ Serviço falhou ao iniciar. Veja:"
    info "  rc-service $SERVICE_NAME status"
    info "  cat $LOG_DIR/stdout.log"
    info "  cat $LOG_DIR/stderr.log"
    exit 1
fi

echo
green "▶ Instalação concluída"
echo

# IP da máquina pra ajudar
HOST_IP=$(ip route get 1 2>/dev/null | awk '{print $7; exit}' 2>/dev/null || echo localhost)

info "Painel admin :  http://$HOST_IP:9000"
info "Plano teste  :  http://$HOST_IP:80   ssh -p 2222   dns udp/53"
info "Logs         :  tail -f $LOG_DIR/stdout.log"
info "Status       :  rc-service $SERVICE_NAME status"
info "Config       :  $ENV_DST  (rc-service $SERVICE_NAME restart após editar)"
info "CLI remota   :  $BINARY_DST cli"
echo
