package sshd

import (
	"os"
	"strings"
)

// Config define as opções do servidor SSH.
// Populada via ConfigFromEnv a partir das variáveis de ambiente:
//
//	ZTNA_SSH_KEY          caminho da chave RSA (gerada se não existir)
//	ZTNA_SSH_ED25519_KEY  caminho da chave Ed25519 (opcional)
//	ZTNA_SSH_DEBUG        "true" ou "1" para ativar debug (padrão: false)
//
// Ao menos uma das key paths deve estar preenchida antes de chamar Start.
// O debug pode ser alterado em runtime via Server.SetDebug sem reiniciar.
type Config struct {
	RSAKeyPath     string
	Ed25519KeyPath string
	Debug          bool
}

// ConfigFromEnv lê as variáveis de ambiente e retorna a Config.
// Não define defaults de path — o chamador é responsável pelo fallback.
func ConfigFromEnv() Config {
	cfg := Config{}
	if v := os.Getenv("ZTNA_SSH_KEY"); v != "" {
		cfg.RSAKeyPath = v
	}
	if v := os.Getenv("ZTNA_SSH_ED25519_KEY"); v != "" {
		cfg.Ed25519KeyPath = v
	}
	v := strings.ToLower(os.Getenv("ZTNA_SSH_DEBUG"))
	cfg.Debug = v == "true" || v == "1"
	return cfg
}
