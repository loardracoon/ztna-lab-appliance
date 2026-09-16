// cli_client.go — remote CLI mode.
//
// When the binary is invoked as "ztna-lab cli" (or just "ztna-lab" inside a
// container already running the daemon), instead of instantiating the
// servers locally it connects to the local Admin API and offers the same
// REPL, translating every command of the existing parser into HTTP calls.
//
// Environment variables:
//   ZTNA_ADMIN_URL    default: http://127.0.0.1:9000
//   ZTNA_ADMIN_TOKEN  default: empty (no auth)

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
		return nil, fmt.Errorf("could not reach the daemon at %s: %w", c.base, err)
	}
	defer resp.Body.Close()

	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 400 {
		msg := "error"
		if v, ok := out["error"].(string); ok {
			msg = v
		}
		return out, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return out, nil
}

// runCLIClient is the entrypoint of the "cli" subcommand.
func runCLIClient() {
	c := newAPIClient()

	// Health-check mode: used by the Docker HEALTHCHECK.
	// Exits 0 if the API answers, non-zero otherwise.
	if len(os.Args) > 2 && os.Args[2] == "--health-check" {
		if _, err := c.call("GET", "/api/health", nil); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Sanity check: hit /api/health before opening the REPL
	if _, err := c.call("GET", "/api/health", nil); err != nil {
		fmt.Fprintf(os.Stderr, "✗ Daemon not reachable at %s\n  %v\n", c.base, err)
		fmt.Fprintln(os.Stderr, "  Check that the appliance is running, or adjust ZTNA_ADMIN_URL.")
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

	fmt.Println("ZTNA Lab CLI  →  connected to", c.base)
	fmt.Println("Type 'help' to list the commands.")
	fmt.Println()

	for {
		line, err := rl.Readline()
		if err != nil { // Ctrl-D, or Ctrl-C on an empty line
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

// dispatchRemote translates a REPL line into HTTP calls.
// It keeps the same syntax as the original local REPL.
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
			fmt.Println("usage: dns [start|stop|list|add|remove|cname]")
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
				fmt.Println("usage: dns add <name> <ip>")
				return
			}
			printJSON(c.call("POST", "/api/dns/records",
				map[string]string{"type": "A", "name": parts[2], "value": parts[3]}))
		case "remove":
			if len(parts) != 3 {
				fmt.Println("usage: dns remove <name>")
				return
			}
			printJSON(c.call("DELETE", "/api/dns/records/"+parts[2], nil))
		case "cname":
			if len(parts) >= 3 && parts[2] == "add" && len(parts) == 5 {
				printJSON(c.call("POST", "/api/dns/records",
					map[string]string{"type": "CNAME", "name": parts[3], "value": parts[4]}))
			} else {
				fmt.Println("usage: dns cname add <alias> <target>")
			}
		}

	case "http":
		if len(parts) < 2 {
			fmt.Println("usage: http [start|stop]")
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
			fmt.Println("usage: ssh [start|stop|who]")
		}

	case "log":
		if len(parts) >= 2 && parts[1] == "tail" {
			n := "50"
			if len(parts) >= 3 {
				n = parts[2]
			}
			printLog(c.call("GET", "/api/log/tail?n="+n, nil))
		} else {
			fmt.Println("usage: log tail [N]")
		}

	case "latency":
		if len(parts) < 3 || parts[1] != "run" {
			fmt.Println("usage: latency run <url> [count] [interval_ms]")
			return
		}
		body := map[string]any{"url": parts[2]}
		if len(parts) >= 4 {
			body["count"], _ = parseInt(parts[3])
		}
		if len(parts) >= 5 {
			body["interval_ms"], _ = parseInt(parts[4])
		}
		printJSON(c.call("POST", "/api/latency", body))

	case "clear":
		fmt.Print("\033[2J\033[H")

	case "help":
		fmt.Println(`Commands (all sent to the daemon through the Admin API):
  status                            state of every service
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
  exit                              leave the CLI (the daemon keeps running)`)

	default:
		fmt.Printf("unknown command: %s (type 'help')\n", parts[0])
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
	fmt.Printf("\n%-7s %-40s %s\n", "TYPE", "NAME", "VALUE")
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
