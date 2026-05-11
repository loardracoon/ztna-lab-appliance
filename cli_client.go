// cli_client.go — modo CLI remoto.
//
// Quando o binário é invocado com "ztna-lab cli" (ou só "ztna-lab" dentro
// de um container já rodando o daemon), em vez de instanciar os servidores
// localmente, ele se conecta à Admin API local e oferece o REPL, traduzindo
// cada comando do parser existente em chamadas HTTP.
//
// Variáveis de ambiente:
//   ZTNA_ADMIN_URL    default: http://127.0.0.1:9000
//   ZTNA_ADMIN_TOKEN  default: vazio (sem auth)

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/chzyer/readline"
)

type apiClient struct {
	base   string
	token  string
	client *http.Client
}

func newAPIClient() *apiClient {
	base := os.Getenv("ZTNA_ADMIN_URL")
	if base == "" {
		base = "http://127.0.0.1:9000"
	}
	return &apiClient{
		base:   strings.TrimRight(base, "/"),
		token:  os.Getenv("ZTNA_ADMIN_TOKEN"),
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *apiClient) call(method, path string, body any) (map[string]any, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("não foi possível conectar ao daemon em %s: %w", c.base, err)
	}
	defer resp.Body.Close()

	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 400 {
		msg := "erro"
		if v, ok := out["error"].(string); ok {
			msg = v
		}
		return out, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return out, nil
}

// runCLIClient é o entrypoint do subcomando "cli".
func runCLIClient() {
	c := newAPIClient()

	// Modo health-check: usado pelo HEALTHCHECK do Docker.
	// Sai 0 se a API responde, !=0 se não.
	if len(os.Args) > 2 && os.Args[2] == "--health-check" {
		if _, err := c.call("GET", "/api/health", nil); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Sanity check: conecta ao /api/health antes de abrir o REPL
	if _, err := c.call("GET", "/api/health", nil); err != nil {
		fmt.Fprintf(os.Stderr, "✗ Daemon não acessível em %s\n  %v\n", c.base, err)
		fmt.Fprintln(os.Stderr, "  Verifique se o appliance está rodando ou ajuste ZTNA_ADMIN_URL.")
		os.Exit(1)
	}

	historyFile := os.Getenv("ZTNA_HISTORY_FILE")
	if historyFile == "" {
		historyFile = "/data/ztna_lab.history"
	}
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "ztna> ",
		HistoryFile:     historyFile,
		HistoryLimit:    200,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer rl.Close()

	fmt.Println("ZTNA Lab CLI  →  conectado a", c.base)
	fmt.Println("Digite 'help' para ver os comandos.")
	fmt.Println()

	for {
		line, err := rl.Readline()
		if err != nil { // Ctrl-D ou Ctrl-C com linha vazia
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}
		dispatchRemote(c, line)
	}
}

// dispatchRemote traduz a linha do REPL em chamadas HTTP.
// Mantém a mesma sintaxe do REPL local original.
func dispatchRemote(c *apiClient, line string) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return
	}

	switch parts[0] {

	case "status":
		printJSON(c.call("GET", "/api/status", nil))

	case "dns":
		if len(parts) < 2 {
			fmt.Println("uso: dns [start|stop|list|add|remove|cname]")
			return
		}
		switch parts[1] {
		case "start":
			printJSON(c.call("POST", "/api/dns/start", nil))
		case "stop":
			printJSON(c.call("POST", "/api/dns/stop", nil))
		case "list":
			printDNSList(c.call("GET", "/api/dns/records", nil))
		case "add":
			if len(parts) != 4 {
				fmt.Println("uso: dns add <nome> <ip>")
				return
			}
			printJSON(c.call("POST", "/api/dns/records",
				map[string]string{"type": "A", "name": parts[2], "value": parts[3]}))
		case "remove":
			if len(parts) != 3 {
				fmt.Println("uso: dns remove <nome>")
				return
			}
			printJSON(c.call("DELETE", "/api/dns/records/"+parts[2], nil))
		case "cname":
			if len(parts) >= 3 && parts[2] == "add" && len(parts) == 5 {
				printJSON(c.call("POST", "/api/dns/records",
					map[string]string{"type": "CNAME", "name": parts[3], "value": parts[4]}))
			} else {
				fmt.Println("uso: dns cname add <alias> <alvo>")
			}
		}

	case "http":
		if len(parts) < 2 {
			fmt.Println("uso: http [start|stop]")
			return
		}
		printJSON(c.call("POST", "/api/http/"+parts[1], nil))

	case "ssh":
		switch {
		case len(parts) == 2 && (parts[1] == "start" || parts[1] == "stop"):
			printJSON(c.call("POST", "/api/ssh/"+parts[1], nil))
		case len(parts) == 2 && parts[1] == "who":
			printJSON(c.call("GET", "/api/ssh/sessions", nil))
		default:
			fmt.Println("uso: ssh [start|stop|who]")
		}

	case "log":
		if len(parts) >= 2 && parts[1] == "tail" {
			n := "50"
			if len(parts) >= 3 {
				n = parts[2]
			}
			printLog(c.call("GET", "/api/log/tail?n="+n, nil))
		} else {
			fmt.Println("uso: log tail [N]")
		}

	case "latency":
		if len(parts) < 3 || parts[1] != "run" {
			fmt.Println("uso: latency run <url> [count] [interval_ms]")
			return
		}
		body := map[string]any{"url": parts[2]}
		if len(parts) >= 4 {
			fmt.Sscanf(parts[3], "%d", new(int)) // ignora erro
			body["count"], _ = parseInt(parts[3])
		}
		if len(parts) >= 5 {
			body["interval_ms"], _ = parseInt(parts[4])
		}
		printJSON(c.call("POST", "/api/latency", body))

	case "clear":
		fmt.Print("\033[2J\033[H")

	case "help":
		fmt.Println(`Comandos (todos enviados ao daemon via Admin API):
  status                            estado de todos os serviços
  dns start | stop                  controle do DNS
  dns list                          lista registros A e CNAME
  dns add <nome> <ip>               adiciona registro A
  dns remove <nome>                 remove registro
  dns cname add <alias> <alvo>      adiciona CNAME
  http start | stop                 controle do HTTP test (porta 80)
  ssh start | stop | who            controle do SSH e sessões
  log tail [N]                      últimas N linhas do log
  latency run <url> [count] [ms]    teste de latência
  clear                             limpa a tela
  exit                              sai do CLI (não para o daemon)`)

	default:
		fmt.Printf("comando desconhecido: %s (digite 'help')\n", parts[0])
	}
}

func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

func printJSON(out map[string]any, err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}

func printDNSList(out map[string]any, err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return
	}
	fmt.Printf("\n%-7s %-40s %s\n", "TIPO", "NOME", "VALOR")
	fmt.Println(strings.Repeat("─", 80))
	if a, ok := out["a"].(map[string]any); ok {
		for n, v := range a {
			fmt.Printf("%-7s %-40s %v\n", "A", n, v)
		}
	}
	if cn, ok := out["cname"].(map[string]any); ok {
		for n, v := range cn {
			fmt.Printf("%-7s %-40s %v\n", "CNAME", n, v)
		}
	}
	fmt.Println()
}

func printLog(out map[string]any, err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return
	}
	if lines, ok := out["lines"].([]any); ok {
		for _, l := range lines {
			fmt.Println(l)
		}
	}
}
