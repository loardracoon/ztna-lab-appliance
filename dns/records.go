// Package dns implementa o servidor DNS local do appliance + um store
// thread-safe de registros A e CNAME persistido em JSON.
//
// O store é mantido a nível de pacote (variáveis globais protegidas por
// mutex) porque a Admin API o manipula via funções de pacote (AddA,
// AddCNAME, Remove, ListRecords) — mesmo quando o servidor DNS está parado.
package dns

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"ztna-lab/logger"
)

// recordStore é o estado in-memory dos registros, lido/escrito com mutex.
type recordStore struct {
	mu     sync.RWMutex
	A      map[string]string `json:"a"`
	CNAME  map[string]string `json:"cname"`
	path   string
	loaded bool
}

var store = &recordStore{
	A:     map[string]string{},
	CNAME: map[string]string{},
}

// LoadRecords carrega o JSON de registros do disco. Chamado pelo NewServer.
// Se o arquivo não existir, mantém os maps vazios e não retorna erro.
func LoadRecords(path string) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	store.path = path
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			store.loaded = true
			return nil
		}
		return err
	}
	tmp := struct {
		A     map[string]string `json:"a"`
		CNAME map[string]string `json:"cname"`
	}{}
	if err := json.Unmarshal(data, &tmp); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	if tmp.A != nil {
		store.A = lower(tmp.A)
	}
	if tmp.CNAME != nil {
		store.CNAME = lower(tmp.CNAME)
	}
	store.loaded = true
	logger.Log("DNS ", fmt.Sprintf("loaded %d A and %d CNAME records from %s", len(store.A), len(store.CNAME), path))
	return nil
}

// saveLocked grava em disco. Chamador deve já estar com store.mu travado em write.
func saveLocked() error {
	if store.path == "" {
		return nil // sem path = só em memória, não persiste
	}
	out := struct {
		A     map[string]string `json:"a"`
		CNAME map[string]string `json:"cname"`
	}{A: store.A, CNAME: store.CNAME}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	// gravação atômica: temp file + rename
	tmpFile := store.path + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpFile, store.path)
}

func lower(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}

// ─────────────── API pública (usada pela Admin API e pela CLI) ───────────────

// ListRecords retorna cópias dos mapas atuais.
func ListRecords() (a map[string]string, cname map[string]string) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	aCopy := make(map[string]string, len(store.A))
	cCopy := make(map[string]string, len(store.CNAME))
	for k, v := range store.A {
		aCopy[k] = v
	}
	for k, v := range store.CNAME {
		cCopy[k] = v
	}
	return aCopy, cCopy
}

// AddA adiciona um registro A. Sobrescreve se já existir.
func AddA(name, ip string) error {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name == "" {
		return fmt.Errorf("name vazio")
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("ip inválido: %q", ip)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.A[name] = ip
	// um nome não pode ter A e CNAME simultaneamente
	delete(store.CNAME, name)
	if err := saveLocked(); err != nil {
		return err
	}
	logger.Log("DNS ", fmt.Sprintf("record added: %s A %s", name, ip))
	return nil
}

// AddCNAME adiciona um CNAME. Sobrescreve se já existir.
func AddCNAME(alias, target string) error {
	alias = strings.ToLower(strings.TrimSuffix(alias, "."))
	target = strings.ToLower(strings.TrimSuffix(target, "."))
	if alias == "" || target == "" {
		return fmt.Errorf("alias e target são obrigatórios")
	}
	if alias == target {
		return fmt.Errorf("alias e target não podem ser iguais")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.CNAME[alias] = target
	delete(store.A, alias)
	if err := saveLocked(); err != nil {
		return err
	}
	logger.Log("DNS ", fmt.Sprintf("record added: %s CNAME %s", alias, target))
	return nil
}

// Remove apaga A e/ou CNAME que casem com o nome.
func Remove(name string) error {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	store.mu.Lock()
	defer store.mu.Unlock()
	_, hadA := store.A[name]
	_, hadC := store.CNAME[name]
	if !hadA && !hadC {
		return fmt.Errorf("registro %q não encontrado", name)
	}
	delete(store.A, name)
	delete(store.CNAME, name)
	if err := saveLocked(); err != nil {
		return err
	}
	logger.Log("DNS ", fmt.Sprintf("record removed: %s", name))
	return nil
}

// lookupA tenta resolver name para um IPv4, seguindo CNAMEs até 8 níveis.
// Retorna ("", false) se não encontrar.
func lookupA(name string) (ip string, cnameChain []string, ok bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	for i := 0; i < 8; i++ {
		if ip, hasA := store.A[name]; hasA {
			return ip, cnameChain, true
		}
		next, hasC := store.CNAME[name]
		if !hasC {
			return "", cnameChain, false
		}
		cnameChain = append(cnameChain, name+" -> "+next)
		name = next
	}
	return "", cnameChain, false // loop ou profundidade excessiva
}
