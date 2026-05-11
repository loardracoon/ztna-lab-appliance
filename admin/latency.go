package admin

import (
	"time"
)

// LatencyResult espelha o output do "latency run" da CLI.
// Mantenha sincronizado com a struct usada em main.go.
type LatencyResult struct {
	URL       string  `json:"url"`
	Total     int     `json:"total"`
	Success   int     `json:"success"`
	Min       int64   `json:"min_ms"`
	Avg       int64   `json:"avg_ms"`
	P50       int64   `json:"p50_ms"`
	P95       int64   `json:"p95_ms"`
	P99       int64   `json:"p99_ms"`
	Max       int64   `json:"max_ms"`
	Jitter    int64   `json:"jitter_ms"`
	Verdict   string  `json:"verdict"`
}

// LatencyFn é injetada pelo main para evitar import cycle.
// O main faz: admin.LatencyFn = main.runLatencyAPI
var LatencyFn func(url string, count int, interval time.Duration) (LatencyResult, error)

func runLatency(url string, count int, interval time.Duration) (LatencyResult, error) {
	if LatencyFn == nil {
		return LatencyResult{}, errLatencyUnavailable
	}
	return LatencyFn(url, count, interval)
}

var errLatencyUnavailable = &simpleErr{"latency runner not registered"}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }
