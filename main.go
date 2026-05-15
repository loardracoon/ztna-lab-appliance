// main.go — REPL interativo e ferramenta de latência.
//
// IMPORTANTE: a função main() do binário vive em main_appliance.go.
// Este arquivo expõe:
//   - runREPL()  — REPL com readline, chamado quando o binário é executado
//                  sem subcomando e em ambiente TTY
//   - runLatency() — ferramenta de latência, chamada tanto pelo REPL quanto
//                    pela Admin API (via runLatencyAPI em main_appliance.go)
package main

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chzyer/readline"

	"ztna-lab/dns"
	"ztna-lab/httpd"
	"ztna-lab/logger"
	"ztna-lab/sshd"
)

// Estado global do REPL: instâncias dos servidores manipulados pelos
// comandos. Diferente do daemon (que mantém suas próprias referências),
// o REPL local cria e gerencia as suas.
var (
	replDNS  *dns.Server
	replHTTP *httpd.Server
	replSSH  *sshd.Server
)

// runREPL é o entrypoint do shell interativo local (modo legado).
// Nota: o uso recomendado em produção é `ztna-lab daemon` + `ztna-lab cli`.
// O REPL local cria os servidores nele mesmo, sem passar pela Admin API.
func runREPL() {
	// Inicializa instâncias dos servidores. Paths vêm de env vars (mesmos
	// usados pelo daemon), com defaults pra uso local.
	dnsPath := envDefault("ZTNA_DNS_RECORDS", "dns_records.json")
	sshKey := envDefault("ZTNA_SSH_KEY", "ssh_host_key")

	replDNS = dns.NewServer(dnsPath, "1.1.1.1:53")
	replHTTP = httpd.NewServer(":80")
	replSSH = sshd.NewServer(":2222", sshKey)

	historyFile := envDefault("ZTNA_HISTORY_FILE", ".ztna_lab.history")
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "ztna> ",
		HistoryFile:     historyFile,
		HistoryLimit:    200,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer rl.Close()

	fmt.Println("ZTNA Lab v2.0 (modo REPL local)")
	fmt.Println("Digite 'help' para ver os comandos. 'daemon' inicia o modo appliance.")
	fmt.Println()

	for {
		line, err := rl.Readline()
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}
		dispatchLocal(line)
	}

	// shutdown best-effort
	if replDNS.IsRunning() {
		_ = replDNS.Stop()
	}
	if replHTTP.IsRunning() {
		_ = replHTTP.Stop()
	}
	if replSSH.IsRunning() {
		_ = replSSH.Stop()
	}
}

// dispatchLocal processa um comando do REPL local (acessando diretamente
// os pacotes, sem HTTP).
func dispatchLocal(line string) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return
	}

	switch parts[0] {

	case "status":
		fmt.Printf("  DNS  : %s\n", upDown(replDNS.IsRunning()))
		fmt.Printf("  HTTP : %s\n", upDown(replHTTP.IsRunning()))
		fmt.Printf("  SSH  : %s\n", upDown(replSSH.IsRunning()))

	case "dns":
		if len(parts) < 2 {
			fmt.Println("uso: dns [start|stop|list|add|remove|cname]")
			return
		}
		switch parts[1] {
		case "start":
			report(replDNS.Start())
		case "stop":
			report(replDNS.Stop())
		case "list":
			a, c := dns.ListRecords()
			printRecords(a, c)
		case "add":
			if len(parts) != 4 {
				fmt.Println("uso: dns add <nome> <ip>")
				return
			}
			report(dns.AddA(parts[2], parts[3]))
		case "remove":
			if len(parts) != 3 {
				fmt.Println("uso: dns remove <nome>")
				return
			}
			report(dns.Remove(parts[2]))
		case "cname":
			if len(parts) == 5 && parts[2] == "add" {
				report(dns.AddCNAME(parts[3], parts[4]))
			} else {
				fmt.Println("uso: dns cname add <alias> <alvo>")
			}
		default:
			fmt.Println("subcomando dns desconhecido")
		}

	case "http":
		if len(parts) != 2 {
			fmt.Println("uso: http [start|stop]")
			return
		}
		if parts[1] == "start" {
			report(replHTTP.Start())
		} else if parts[1] == "stop" {
			report(replHTTP.Stop())
		} else {
			fmt.Println("uso: http [start|stop]")
		}

	case "ssh":
		if len(parts) < 2 {
			fmt.Println("uso: ssh [start|stop|who]")
			return
		}
		switch parts[1] {
		case "start":
			report(replSSH.Start())
		case "stop":
			report(replSSH.Stop())
		case "who":
			for _, s := range replSSH.Sessions() {
				fmt.Printf("  #%d  user=%s  ip=%s  conn=%s\n",
					s.ID, s.User, s.IP, s.ConnectedAt.Format(time.RFC3339))
			}
		}

	case "log":
		if len(parts) >= 2 && parts[1] == "tail" {
			n := 50
			if len(parts) >= 3 {
				if v, err := strconv.Atoi(parts[2]); err == nil {
					n = v
				}
			}
			lines, err := logger.Tail(n)
			if err != nil {
				fmt.Println("erro:", err)
				return
			}
			for _, l := range lines {
				fmt.Println(l)
			}
		}

	case "latency":
		if len(parts) < 3 || parts[1] != "run" {
			fmt.Println("uso: latency run <url> [count] [interval_ms]")
			return
		}
		count := 50
		interval := 200 * time.Millisecond
		if len(parts) >= 4 {
			if v, err := strconv.Atoi(parts[3]); err == nil {
				count = v
			}
		}
		if len(parts) >= 5 {
			if v, err := strconv.Atoi(parts[4]); err == nil {
				interval = time.Duration(v) * time.Millisecond
			}
		}
		res, err := runLatency(parts[2], count, interval)
		if err != nil {
			fmt.Println("erro:", err)
			return
		}
		printLatency(res)

	case "clear":
		fmt.Print("\033[2J\033[H")

	case "help":
		fmt.Println(`Comandos do REPL:
  status                            estado dos servidores locais
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
  exit                              sai (encerra os servidores locais)`)

	default:
		fmt.Printf("comando desconhecido: %s (digite 'help')\n", parts[0])
	}
}

// ─────────────────────── latency ───────────────────────

// LatencyResult espelha o que a Admin API expõe via JSON.
type LatencyResult struct {
	URL     string
	Total   int
	Success int
	Min     time.Duration
	Avg     time.Duration
	P50     time.Duration
	P95     time.Duration
	P99     time.Duration
	Max     time.Duration
	Jitter  time.Duration
	Verdict string
}

// runLatency dispara `count` requisições GET para `url`, medindo o tempo
// total de cada uma. Não usa keep-alive (cada requisição abre conexão
// nova) — mais fiel pra teste de latência ponta-a-ponta.
func runLatency(url string, count int, interval time.Duration) (LatencyResult, error) {
	if count <= 0 {
		count = 50
	}
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return LatencyResult{}, fmt.Errorf("url deve começar com http:// ou https://")
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}

	samples := make([]time.Duration, 0, count)
	success := 0
	for i := 0; i < count; i++ {
		start := time.Now()
		resp, err := client.Get(url)
		dur := time.Since(start)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode < 500 {
				success++
				samples = append(samples, dur)
			}
		}
		if i < count-1 {
			time.Sleep(interval)
		}
	}

	if len(samples) == 0 {
		return LatencyResult{
			URL:     url,
			Total:   count,
			Success: 0,
			Verdict: "TOTAL FAILURE",
		}, nil
	}

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })

	var sum time.Duration
	for _, s := range samples {
		sum += s
	}
	avg := sum / time.Duration(len(samples))

	// jitter = stddev
	var sqSum float64
	for _, s := range samples {
		d := float64(s - avg)
		sqSum += d * d
	}
	jitter := time.Duration(math.Sqrt(sqSum / float64(len(samples))))

	res := LatencyResult{
		URL:     url,
		Total:   count,
		Success: success,
		Min:     samples[0],
		Avg:     avg,
		P50:     samples[len(samples)*50/100],
		P95:     samples[min(len(samples)-1, len(samples)*95/100)],
		P99:     samples[min(len(samples)-1, len(samples)*99/100)],
		Max:     samples[len(samples)-1],
		Jitter:  jitter,
	}
	res.Verdict = verdict(res)
	return res, nil
}

func verdict(r LatencyResult) string {
	if r.Success == 0 {
		return "TOTAL FAILURE"
	}
	failRate := float64(r.Total-r.Success) / float64(r.Total)
	switch {
	case failRate > 0.5:
		return "MOSTLY FAILING"
	case failRate > 0.1:
		return "DEGRADED"
	case r.P95 > 1*time.Second:
		return "SLOW"
	case r.P95 > 250*time.Millisecond:
		return "OK"
	default:
		return "GOOD"
	}
}

func printLatency(r LatencyResult) {
	fmt.Println()
	fmt.Printf("  URL     : %s\n", r.URL)
	fmt.Printf("  Samples : %d/%d successful\n", r.Success, r.Total)
	fmt.Printf("  Min     : %s\n", r.Min.Round(time.Millisecond))
	fmt.Printf("  Avg     : %s\n", r.Avg.Round(time.Millisecond))
	fmt.Printf("  P50     : %s\n", r.P50.Round(time.Millisecond))
	fmt.Printf("  P95     : %s\n", r.P95.Round(time.Millisecond))
	fmt.Printf("  P99     : %s\n", r.P99.Round(time.Millisecond))
	fmt.Printf("  Max     : %s\n", r.Max.Round(time.Millisecond))
	fmt.Printf("  Jitter  : %s\n", r.Jitter.Round(time.Millisecond))
	fmt.Printf("  Verdict : %s\n", r.Verdict)
	fmt.Println()
}

// ─────────────────────── helpers ───────────────────────

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func upDown(b bool) string {
	if b {
		return "UP"
	}
	return "down"
}

func report(err error) {
	if err != nil {
		fmt.Println("✗", err)
		return
	}
	fmt.Println("✓ ok")
}

func printRecords(a, c map[string]string) {
	fmt.Printf("\n  %-7s %-40s %s\n", "TIPO", "NOME", "VALOR")
	fmt.Println("  ", strings.Repeat("─", 78))
	for n, v := range a {
		fmt.Printf("  %-7s %-40s %s\n", "A", n, v)
	}
	for n, v := range c {
		fmt.Printf("  %-7s %-40s %s\n", "CNAME", n, v)
	}
	fmt.Println()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
