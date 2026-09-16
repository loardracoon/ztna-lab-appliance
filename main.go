// main.go — interactive REPL and latency tool.
//
// IMPORTANT: the binary's main() lives in main_appliance.go.
// This file exposes:
//   - runREPL()    — readline REPL, used when the binary runs without a
//     subcommand on a TTY
//   - runLatency() — latency tool, called both by the REPL and by the
//     Admin API (through runLatencyAPI in main_appliance.go)
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

// REPL global state: the server instances the commands act on. Unlike the
// daemon (which keeps its own references), the local REPL creates and
// manages its own.
var (
	replDNS  *dns.Server
	replHTTP *httpd.Server
	replSSH  *sshd.Server
)

// runREPL is the entrypoint of the local interactive shell (legacy mode).
// Note: the recommended production usage is `ztna-lab daemon` + `ztna-lab cli`.
// The local REPL creates the servers in-process, bypassing the Admin API.
func runREPL() {
	// Build the server instances. Paths come from the same env vars the
	// daemon uses, with defaults suited to local runs.
	dnsPath := envDefault("ZTNA_DNS_RECORDS", "dns_records.json")

	sshCfg := sshd.ConfigFromEnv()
	if sshCfg.RSAKeyPath == "" && sshCfg.Ed25519KeyPath == "" {
		sshCfg.RSAKeyPath = "ssh_host_key"
	}

	replDNS = dns.NewServer(dnsPath, "1.1.1.1:53")
	replHTTP = httpd.NewServer(":80")
	replSSH = sshd.NewServer(":2222", sshCfg)

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

	fmt.Println("ZTNA Lab v2.0 (local REPL mode)")
	fmt.Println("Type 'help' to list the commands. 'daemon' starts appliance mode.")
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

	// best-effort shutdown
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

// dispatchLocal handles a local REPL command, calling the packages
// directly instead of going through HTTP.
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
			fmt.Println("usage: dns [start|stop|list|add|remove|cname]")
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
				fmt.Println("usage: dns add <name> <ip>")
				return
			}
			report(dns.AddA(parts[2], parts[3]))
		case "remove":
			if len(parts) != 3 {
				fmt.Println("usage: dns remove <name>")
				return
			}
			report(dns.Remove(parts[2]))
		case "cname":
			if len(parts) == 5 && parts[2] == "add" {
				report(dns.AddCNAME(parts[3], parts[4]))
			} else {
				fmt.Println("usage: dns cname add <alias> <target>")
			}
		default:
			fmt.Println("unknown dns subcommand")
		}

	case "http":
		if len(parts) != 2 {
			fmt.Println("usage: http [start|stop]")
			return
		}
		if parts[1] == "start" {
			report(replHTTP.Start())
		} else if parts[1] == "stop" {
			report(replHTTP.Stop())
		} else {
			fmt.Println("usage: http [start|stop]")
		}

	case "ssh":
		if len(parts) < 2 {
			fmt.Println("usage: ssh [start|stop|who]")
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
		if len(parts) < 2 || parts[1] != "tail" {
			fmt.Println("usage: log tail [N]")
			return
		}
		{
			n := 50
			if len(parts) >= 3 {
				if v, err := strconv.Atoi(parts[2]); err == nil {
					n = v
				}
			}
			lines, err := logger.Tail(n)
			if err != nil {
				fmt.Println("error:", err)
				return
			}
			for _, l := range lines {
				fmt.Println(l)
			}
		}

	case "latency":
		if len(parts) < 3 || parts[1] != "run" {
			fmt.Println("usage: latency run <url> [count] [interval_ms]")
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
			fmt.Println("error:", err)
			return
		}
		printLatency(res)

	case "clear":
		fmt.Print("\033[2J\033[H")

	case "help":
		fmt.Println(`REPL commands:
  status                            state of the local servers
  dns start | stop                  control the DNS server
  dns list                          list A and CNAME records
  dns add <name> <ip>               add an A record
  dns remove <name>                 remove a record
  dns cname add <alias> <target>    add a CNAME record
  http start | stop                 control the HTTP test server (port 80)
  ssh start | stop | who            control SSH and list sessions
  log tail [N]                      last N lines of the log
  latency run <url> [count] [ms]    latency test
  clear                             clear the screen
  exit                              quit (stops the local servers)`)

	default:
		fmt.Printf("unknown command: %s (type 'help')\n", parts[0])
	}
}

// ─────────────────────── latency ───────────────────────

// LatencyResult mirrors what the Admin API exposes as JSON.
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

// runLatency fires `count` GET requests at `url`, measuring the total time
// of each one. Keep-alive is disabled (every request opens a new
// connection), which is truer to an end-to-end latency test.
func runLatency(url string, count int, interval time.Duration) (LatencyResult, error) {
	if count <= 0 {
		count = 50
	}
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return LatencyResult{}, fmt.Errorf("url must start with http:// or https://")
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

	// jitter = standard deviation
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
	fmt.Printf("\n  %-7s %-40s %s\n", "TYPE", "NAME", "VALUE")
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
