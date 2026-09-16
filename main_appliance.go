// main_appliance.go — entrypoint of the binary, with the "daemon" and
// "cli" subcommands.
//
// It works together with main.go: the functions below replace/extend the
// original main(). runREPL, runLatency and friends stay where they are.

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
	// The logger always points at a persistent path (/data inside the container).
	logPath := os.Getenv("ZTNA_LOG_PATH")
	if logPath == "" {
		logPath = "/data/ztna_lab.log"
	}
	logger.Init(logPath)

	// Subcommand routing.
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "daemon":
		runDaemon()
	case "cli":
		runCLIClient()
	case "fwopt":
		runFwopt(os.Args[2:])
	case "version":
		fmt.Println("ztna-lab appliance 1.0")
	case "":
		// No argument: behaviour depends on the TTY.
		// Interactive container (docker run -it) -> local REPL
		// Headless container (docker run -d)     -> daemon
		if isatty(os.Stdin) {
			runREPL() // original local REPL
		} else {
			runDaemon()
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		fmt.Fprintln(os.Stderr, "usage: ztna-lab [daemon|cli|fwopt|version]")
		os.Exit(2)
	}
}

// runDaemon starts every server plus the Admin API and blocks until it
// receives SIGTERM/SIGINT.
func runDaemon() {
	logger.Log("SYS ", "ZTNA Lab appliance starting (daemon mode)")

	// Path of the DNS records JSON (persisted under /data).
	dnsPath := os.Getenv("ZTNA_DNS_RECORDS")
	if dnsPath == "" {
		dnsPath = "/data/dns_records.json"
	}

	// SSH server config read from the environment.
	sshCfg := sshd.ConfigFromEnv()
	if sshCfg.RSAKeyPath == "" && sshCfg.Ed25519KeyPath == "" {
		sshCfg.RSAKeyPath = "/data/ssh_host_key"
	}

	dnsSrv := dns.NewServer(dnsPath, "1.1.1.1:53")
	httpSrv := httpd.NewServer(":80")
	sshSrv := sshd.NewServer(":2222", sshCfg)

	// Wire the latency runner into the Admin API.
	admin.LatencyFn = runLatencyAPI

	// Auto-start is configurable per service. Default: start everything.
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

	fmt.Println("✓ ZTNA Lab appliance ready")
	fmt.Println("  Test plane         :  http://0.0.0.0:80   ssh -p 2222   dns udp/53")
	fmt.Printf("  Admin plane        :  http://%s\n", adminAddr)
	fmt.Printf("  Log file           :  %s\n", logger.Path())
	fmt.Println("  CLI                :  docker exec -it <container> /ztna-lab cli")

	// Wait for the shutdown signal.
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

// runLatencyAPI adapts runLatency (in main.go) to the shape the Admin API
// expects.
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

// isatty reports whether stdin is an interactive terminal. Minimal
// implementation — swap in a library if you prefer.
func isatty(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
