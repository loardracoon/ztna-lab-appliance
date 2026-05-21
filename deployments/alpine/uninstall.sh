#!/bin/sh
# uninstall.sh — remove o ZTNA Lab Appliance do Alpine.

set -eu

SERVICE_NAME="ztna-lab"
SERVICE_USER="ztna-lab"
BINARY_DST="/usr/local/bin/ztna-lab"
INITSCRIPT_DST="/etc/init.d/ztna-lab"
CONFIG_DIR="/etc/ztna-lab"
DATA_DIR="/var/lib/ztna-lab"
LOG_DIR="/var/log/ztna-lab"

red()    { printf "\033[31m%s\033[0m\n" "$*"; }
green()  { printf "\033[32m%s\033[0m\n" "$*"; }
yellow() { printf "\033[33m%s\033[0m\n" "$*"; }
info()   { printf "  %s\n" "$*"; }

[ "$(id -u)" -eq 0 ] || { red "Use: sudo sh $0"; exit 1; }

green "▶ Removendo ZTNA Lab Appliance"
echo

# Stop + disable service
if [ -f "$INITSCRIPT_DST" ]; then
    info "Parando e desabilitando serviço..."
    rc-service "$SERVICE_NAME" stop >/dev/null 2>&1 || true
    rc-update del "$SERVICE_NAME" default >/dev/null 2>&1 || true
fi

info "Removendo initscript e binário..."
rm -f "$INITSCRIPT_DST" "$BINARY_DST"

# Confirmações por etapa
printf "  Apagar dados em %s (chave SSH, registros DNS)? [s/N] " "$DATA_DIR"
read -r ans
case "$ans" in
    [Ss]*)
        rm -rf "$DATA_DIR"
        info "  $DATA_DIR removido."
        ;;
    *)
        yellow "  $DATA_DIR preservado."
        ;;
esac

printf "  Apagar logs em %s? [s/N] " "$LOG_DIR"
read -r ans
case "$ans" in
    [Ss]*)
        rm -rf "$LOG_DIR"
        info "  $LOG_DIR removido."
        ;;
    *)
        yellow "  $LOG_DIR preservado."
        ;;
esac

printf "  Apagar configuração em %s? [s/N] " "$CONFIG_DIR"
read -r ans
case "$ans" in
    [Ss]*)
        rm -rf "$CONFIG_DIR"
        info "  $CONFIG_DIR removido."
        ;;
    *)
        yellow "  $CONFIG_DIR preservado."
        ;;
esac

printf "  Remover usuário '%s'? [s/N] " "$SERVICE_USER"
read -r ans
case "$ans" in
    [Ss]*)
        deluser "$SERVICE_USER" 2>/dev/null || true
        delgroup "$SERVICE_USER" 2>/dev/null || true
        info "  usuário removido."
        ;;
esac

echo
green "✓ Desinstalação concluída."
