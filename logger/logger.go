// Package logger é um logger compartilhado pelo binário inteiro.
//
// Saída duplicada: arquivo + stdout. Cada linha tem timestamp + módulo:
//
//	2025-05-11 13:42:01  [DNS ] query received from 10.0.0.5: example.com A
//
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

var (
	mu      sync.Mutex
	file    *os.File
	curPath string
)

// Init abre o arquivo de log no path informado, criando-o se necessário.
// Se já houver um arquivo aberto, ele é fechado primeiro.
func Init(path string) {
	mu.Lock()
	defer mu.Unlock()

	if file != nil && curPath == path {
		return // já inicializado com o mesmo path
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
		_, _ = file.WriteString(line)
		_ = file.Sync()
	}
}

// Tail retorna as últimas n linhas do arquivo de log.
// Implementação simples: lê o arquivo inteiro (logs ficam pequenos no caso
// típico de um appliance de lab). Para logs muito grandes seria melhor um
// reverse-read em blocos.
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
	sc.Buffer(make([]byte, 1024*1024), 1024*1024) // até 1 MB por linha
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
