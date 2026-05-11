#!/usr/bin/env bash
# uninstall.sh — remove o ZTNA Lab Appliance.
#
# Uso:
#   sudo make uninstall
#   sudo bash deployments/linux/uninstall.sh

set -euo pipefail

SERVICE_NAME="ztna-lab"
SERVICE_USER="ztna-lab"
BINARY_DST="/usr/local/bin/ztna-lab"
UNIT_DST="/etc/systemd/system/ztna-lab.service"
CONFIG_DIR="/etc/ztna-lab"
DATA_DIR="/var/lib/ztna-lab"
LOG_DIR="/var/log/ztna-lab"

red()    { printf "\033[31m%s\033[0m\n" "$*"; }
green()  { printf "\033[32m%s\033[0m\n" "$*"; }
yellow() { printf "\033[33m%s\033[0m\n" "$*"; }
info()   { printf "  %s\n" "$*"; }

[ "$(id -u)" -eq 0 ] || { red "Use: sudo bash $0"; exit 1; }

green "▶ Removendo ZTNA Lab Appliance"
echo

if systemctl list-unit-files | grep -q "^$SERVICE_NAME.service"; then
    info "Parando e desabilitando serviço..."
    systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    systemctl disable "$SERVICE_NAME" 2>/dev/null || true
fi

info "Removendo unit file..."
rm -f "$UNIT_DST"
systemctl daemon-reload

info "Removendo binário..."
rm -f "$BINARY_DST"

read -p "  Apagar dados em $DATA_DIR (chave SSH, registros DNS)? [s/N] " ans
if [[ "$ans" =~ ^[Ss]$ ]]; then
    rm -rf "$DATA_DIR"
    info "  $DATA_DIR removido."
else
    yellow "  $DATA_DIR preservado."
fi

read -p "  Apagar logs em $LOG_DIR? [s/N] " ans
if [[ "$ans" =~ ^[Ss]$ ]]; then
    rm -rf "$LOG_DIR"
    info "  $LOG_DIR removido."
else
    yellow "  $LOG_DIR preservado."
fi

read -p "  Apagar configuração em $CONFIG_DIR? [s/N] " ans
if [[ "$ans" =~ ^[Ss]$ ]]; then
    rm -rf "$CONFIG_DIR"
    info "  $CONFIG_DIR removido."
else
    yellow "  $CONFIG_DIR preservado."
fi

read -p "  Remover usuário de sistema '$SERVICE_USER'? [s/N] " ans
if [[ "$ans" =~ ^[Ss]$ ]]; then
    userdel "$SERVICE_USER" 2>/dev/null || true
    info "  usuário removido."
fi

echo
green "✓ Desinstalação concluída."
