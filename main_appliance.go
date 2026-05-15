// main_appliance.go — modificações no main.go original para suportar
// os subcomandos "daemon" e "cli".
//
// Integre com o main.go existente: as funções abaixo substituem/aumentam
// a função main() original. As funções runREPL, runLatency etc. do projeto
// original permanecem.

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ztna-lab/admin"
	"ztna-lab/dns"
	"ztna-lab/httpd"
	"ztna-lab/logger"
	"ztna-lab/sshd"
)

func main() {
	// Inicializa logger sempre apontando para path persistente em /data dentro do container.
	logPath := os.Getenv("ZTNA_LOG_PATH")
	if logPath == "" {
		logPath = "/data/ztna_lab.log"
	}
	logger.Init(logPath)

	// Roteamento por subcomando.
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "daemon":
		runDaemon()
	case "cli":
		runCLIClient()
	case "version":
		fmt.Println("ztna-lab appliance 1.0")
	case "":
		// Sem argumento: comportamento depende do TTY.
		// Container interativo (docker run -it) → REPL local
		// Container headless (docker run -d) → daemon
		if isatty(os.Stdin) {
			runREPL() // REPL local original
		} else {
			runDaemon()
		}
	default:
		fmt.Fprintf(os.Stderr, "comando desconhecido: %s\n", cmd)
		fmt.Fprintln(os.Stderr, "uso: ztna-lab [daemon|cli|version]")
		os.Exit(2)
	}
}

// runDaemon sobe todos os servidores e a Admin API, e bloqueia até receber
// SIGTERM/SIGINT.
func runDaemon() {
	logger.Log("SYS ", "ZTNA Lab appliance starting (daemon mode)")

	// Path do JSON de registros DNS (persistido em /data).
	dnsPath := os.Getenv("ZTNA_DNS_RECORDS")
	if dnsPath == "" {
		dnsPath = "/data/dns_records.json"
	}
	sshKeyPath := os.Getenv("ZTNA_SSH_KEY")
	if sshKeyPath == "" {
		sshKeyPath = "/data/ssh_host_key"
	}

	// Constrói os servidores. As funções New* abaixo precisam existir nos
	// pacotes do projeto. Se as suas hoje ainda usam paths fixos, refatore
	// para receber o path como argumento.
	dnsSrv := dns.NewServer(dnsPath, "1.1.1.1:53")
	httpSrv := httpd.NewServer(":80")
	sshSrv := sshd.NewServer(":2222", sshKeyPath)

	// Wire-up do latency runner para a Admin API.
	admin.LatencyFn = runLatencyAPI

	// Auto-start configurável por env. Default: liga tudo.
	if env("ZTNA_AUTOSTART_DNS", "true") == "true" {
		if err := dnsSrv.Start(); err != nil {
			logger.Log("SYS ", "DNS autostart failed: "+err.Error())
		}
	}
	if env("ZTNA_AUTOSTART_HTTP", "true") == "true" {
		if err := httpSrv.Start(); err != nil {
			logger.Log("SYS ", "HTTP autostart failed: "+err.Error())
		}
	}
	if env("ZTNA_AUTOSTART_SSH", "true") == "true" {
		if err := sshSrv.Start(); err != nil {
			logger.Log("SYS ", "SSH autostart failed: "+err.Error())
		}
	}

	// Admin API.
	adminAddr := env("ZTNA_ADMIN_ADDR", "0.0.0.0:9000")
	adminToken := os.Getenv("ZTNA_ADMIN_TOKEN")
	adm := admin.New(adminAddr, adminToken, dnsSrv, httpSrv, sshSrv)
	if err := adm.Start(); err != nil {
		logger.Log("SYS ", "Admin API failed: "+err.Error())
		os.Exit(1)
	}

	fmt.Println("✓ ZTNA Lab appliance pronto")
	fmt.Println("  Plano de teste     :  http://0.0.0.0:80   ssh -p 2222   dns udp/53")
	fmt.Printf("  Plano de admin     :  http://%s\n", adminAddr)
	fmt.Println("  CLI                :  docker exec -it <container> /ztna-lab cli")

	// Aguarda sinal de shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh

	logger.Log("SYS ", "shutdown signal received")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = adm.Stop()
	_ = sshSrv.Stop()
	_ = httpSrv.Stop()
	_ = dnsSrv.Stop()
	_ = ctx
	logger.Log("SYS ", "ZTNA Lab appliance stopped")
}

// runLatencyAPI é o adapter da função runLatency (em main.go) para o formato
// esperado pela Admin API.
func runLatencyAPI(url string, count int, interval time.Duration) (admin.LatencyResult, error) {
	r, err := runLatency(url, count, interval)
	if err != nil {
		return admin.LatencyResult{}, err
	}
	return admin.LatencyResult{
		URL:     r.URL,
		Total:   r.Total,
		Success: r.Success,
		Min:     r.Min.Milliseconds(),
		Avg:     r.Avg.Milliseconds(),
		P50:     r.P50.Milliseconds(),
		P95:     r.P95.Milliseconds(),
		P99:     r.P99.Milliseconds(),
		Max:     r.Max.Milliseconds(),
		Jitter:  r.Jitter.Milliseconds(),
		Verdict: r.Verdict,
	}, nil
}

// ---------- helpers ----------

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return strings.ToLower(v)
	}
	return def
}

// isatty detecta se stdin é um terminal interativo. Implementação mínima
// usando syscall — substitua por uma lib se preferir.
func isatty(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
