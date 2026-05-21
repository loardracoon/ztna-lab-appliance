package sshd

import (
	"encoding/json"
	"os"
)

// Config define as opções do servidor SSH lidas de um arquivo JSON simples.
//
// Exemplo de /data/sshd.json:
//
//	{
//	  "rsa_key_path":     "/data/ssh_host_rsa_key",
//	  "ed25519_key_path": "/data/ssh_host_ed25519_key",
//	  "debug":            true
//	}
//
// Ao menos um dos campos *_key_path deve estar preenchido. Se o arquivo não
// existir, LoadConfig retorna Config{} sem erro — o chamador define os defaults.
type Config struct {
	RSAKeyPath     string `json:"rsa_key_path"`
	Ed25519KeyPath string `json:"ed25519_key_path"`
	Debug          bool   `json:"debug"`
}

// LoadConfig lê o arquivo JSON em path. Se o arquivo não existir retorna
// Config{} sem erro — o chamador é responsável por preencher os defaults.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var cfg Config
	return cfg, json.Unmarshal(data, &cfg)
}
