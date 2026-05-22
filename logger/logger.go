// Package logger é um logger compartilhado pelo binário inteiro.
//
// Saída duplicada: arquivo + stdout. Cada linha tem timestamp + módulo:
//
//	2025-05-11 13:42:01  [DNS ] query received from 10.0.0.5: example.com A
//
// Rotação por tamanho: quando o arquivo atinge maxFileSize, ele é renomeado
// para <path>.1 (sobrescrevendo o backup anterior) e um novo arquivo é aberto.
// Thread-safe via mutex. Init é idempotente — chamadas subsequentes
// reabrem o arquivo se o path mudar.
package logger

import (
	"bufio"
	"fmt"
	"os"
	"sync"
	"time"
)

const maxFileSize = 10 * 1024 * 1024 // 10 MB por arquivo; mantém 1 backup (.log.1)

var (
	mu      sync.Mutex
	file    *os.File
	curPath string
	written int64 // bytes escritos no arquivo atual desde a última rotação
)

// Init abre o arquivo de log no path informado, criando-o se necessário.
// Se já houver um arquivo aberto, ele é fechado primeiro.
func Init(path string) {
	mu.Lock()
	defer mu.Unlock()

	if file != nil && curPath == path {
		return
	}
	if file != nil {
		_ = file.Close()
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger: cannot open %s: %v (logging to stderr only)\n", path, err)
		file = nil
		curPath = ""
		return
	}
	file = f
	curPath = path

	// Inicializa written com o tamanho atual do arquivo para que uma rotação
	// seja disparada corretamente mesmo após um restart com arquivo já grande.
	if fi, err := f.Stat(); err == nil {
		written = fi.Size()
	}
}

// Log escreve uma linha no log com o módulo curto entre colchetes.
// Módulos curtos comuns: "DNS ", "HTTP", "SSH ", "SYS ", "ADM ".
// Sempre passe 4 chars pra manter o alinhamento.
func Log(module, message string) {
	mu.Lock()
	defer mu.Unlock()

	ts := time.Now().Format("2006-01-02 15:04:05")
	line := fmt.Sprintf("%s  [%s] %s\n", ts, module, message)

	fmt.Print(line)
	if file != nil {
		n, _ := file.WriteString(line)
		_ = file.Sync()
		written += int64(n)
		if written >= maxFileSize {
			rotate()
		}
	}
}

// rotate fecha o arquivo atual, renomeia para <path>.1 e abre um novo.
// Deve ser chamado com mu mantido.
func rotate() {
	_ = file.Close()
	file = nil

	backup := curPath + ".1"
	_ = os.Rename(curPath, backup)

	f, err := os.OpenFile(curPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger: rotate failed, logging to stderr only: %v\n", err)
		return
	}
	file = f
	written = 0
	ts := time.Now().Format("2006-01-02 15:04:05")
	_, _ = file.WriteString(fmt.Sprintf("%s  [SYS ] log rotated (previous saved as %s)\n", ts, backup))
}

// Tail retorna as últimas n linhas do arquivo de log.
func Tail(n int) ([]string, error) {
	mu.Lock()
	path := curPath
	mu.Unlock()

	if path == "" {
		return nil, fmt.Errorf("logger not initialized")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Mantém uma janela circular das últimas n linhas.
	buf := make([]string, 0, n)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		if len(buf) >= n {
			buf = buf[1:]
		}
		buf = append(buf, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return buf, nil
}
