// Package logger is the shared logger used by the whole binary.
//
// Output is duplicated: log file + stdout. Every line carries a timestamp
// and the short module name:
//
//	2025-05-11 13:42:01  [DNS ] query received from 10.0.0.5: example.com A
//
// Recycling: the file is rotated whenever it reaches EITHER the line limit
// (ZTNA_LOG_MAX_LINES, default 5000) or the byte limit (ZTNA_LOG_MAX_BYTES,
// default 10 MB), whichever comes first. On rotation the file is renamed to
// <path>.1 (overwriting the previous backup) and a fresh file is opened, so
// disk usage is bounded at roughly 2x the limit and the tail view stays fast.
//
// Per-module gating: every module (SYS, DNS, HTTP, SSH, ADM, …) can be turned
// off at runtime with SetEnabled. A disabled module's lines are dropped
// entirely — not written to the file and not printed to stdout — which is how
// a chatty module (typically HTTP, which logs every request on the test plane)
// is stopped from burying the others. ZTNA_LOG_DISABLED_MODULES sets the
// initial state. Module state changes are always recorded, even for a module
// that is off, so the log always explains its own silence.
//
// Thread-safe via mutex. Init is idempotent — a later call reopens the file
// if the path changed.
package logger

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxLines = 5000             // lines per file before recycling
	defaultMaxBytes = 10 * 1024 * 1024 // 10 MB per file before recycling
	minMaxLines     = 100              // guard against a pathological env value
	maxTailBytes    = 8 * 1024 * 1024  // upper bound on what Tail reads into memory
)

// knownModules are the modules the appliance ships with, in the order the
// admin panel displays them. Any other module name used with Log registers
// itself on first use.
var knownModules = []string{"SYS", "DNS", "HTTP", "SSH", "ADM"}

var (
	mu       sync.Mutex
	file     *os.File
	curPath  string
	written  int64 // bytes in the current file
	lineCnt  int64 // lines in the current file
	maxLines int64 = defaultMaxLines
	maxBytes int64 = defaultMaxBytes

	// enabled maps a normalized module name to whether its lines are kept.
	enabled = defaultModuleState()
)

// ModuleState is one module and whether its lines are being recorded.
type ModuleState struct {
	Module  string `json:"module"`
	Enabled bool   `json:"enabled"`
}

func defaultModuleState() map[string]bool {
	m := make(map[string]bool, len(knownModules))
	for _, name := range knownModules {
		m[name] = true
	}
	return m
}

// normModule maps the padded module names used at call sites ("SSH ") onto
// the canonical key ("SSH").
func normModule(module string) string {
	return strings.ToUpper(strings.TrimSpace(module))
}

// Stats describes the current state of the log file. It is what the admin
// panel shows next to the log view so the recycling behaviour is visible.
type Stats struct {
	Path      string `json:"path"`
	Size      int64  `json:"size_bytes"`
	Lines     int64  `json:"lines"`
	MaxLines  int64  `json:"max_lines"`
	MaxBytes  int64  `json:"max_bytes"`
	HasBackup bool   `json:"has_backup"`
}

// Init opens the log file at path, creating the directory and file if
// needed. If path cannot be opened, it falls back to <tmpdir>/ztna_lab.log
// so that the tail endpoint keeps working instead of failing with
// "logger not initialized". Only if the fallback also fails do we degrade
// to stdout/stderr-only logging.
func Init(path string) {
	mu.Lock()
	defer mu.Unlock()

	maxLines = envInt64("ZTNA_LOG_MAX_LINES", defaultMaxLines, minMaxLines)
	maxBytes = envInt64("ZTNA_LOG_MAX_BYTES", defaultMaxBytes, 64*1024)
	applyDisabledModulesEnv(os.Getenv("ZTNA_LOG_DISABLED_MODULES"))

	if file != nil && curPath == path {
		return
	}
	if file != nil {
		_ = file.Close()
		file = nil
	}

	f, err := openLog(path)
	if err != nil {
		fallback := filepath.Join(os.TempDir(), "ztna_lab.log")
		fmt.Fprintf(os.Stderr, "logger: cannot open %s: %v (falling back to %s)\n", path, err, fallback)
		if f, err = openLog(fallback); err != nil {
			fmt.Fprintf(os.Stderr, "logger: cannot open %s either: %v (logging to stdout only)\n", fallback, err)
			curPath = ""
			return
		}
		path = fallback
	}

	file = f
	curPath = path

	// Seed the counters from the existing file so that a restart on an
	// already-large file still triggers recycling at the right moment.
	written, lineCnt = 0, 0
	if fi, err := f.Stat(); err == nil {
		written = fi.Size()
	}
	if n, err := countLines(path); err == nil {
		lineCnt = n
	}
	if written >= maxBytes || lineCnt >= maxLines {
		rotate()
	}
}

func openLog(path string) (*os.File, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

// Log writes a line to the log, prefixed with the short module name in
// brackets. Common modules: "DNS ", "HTTP", "SSH ", "SYS ", "ADM ".
// Always pass 4 characters to keep the columns aligned.
func Log(module, message string) {
	mu.Lock()
	defer mu.Unlock()

	key := normModule(module)
	if on, known := enabled[key]; known && !on {
		return // module switched off: drop the line entirely
	} else if !known {
		enabled[key] = true // first sighting of a module registers it, enabled
	}
	writeLocked(module, message)
}

// writeLocked formats and records one line, bypassing the per-module switch.
// Callers that must always leave a trace (module toggles, rotation notices)
// use it directly. Must be called with mu held.
func writeLocked(module, message string) {
	ts := time.Now().Format("2006-01-02 15:04:05")
	line := fmt.Sprintf("%s  [%s] %s\n", ts, module, message)

	fmt.Print(line)
	if file == nil {
		return
	}
	n, _ := file.WriteString(line)
	_ = file.Sync()
	written += int64(n)
	lineCnt++
	if lineCnt >= maxLines || written >= maxBytes {
		rotate()
	}
}

// SetEnabled turns a module's logging on or off at runtime. Turning it off
// drops its lines completely — they never reach the file or stdout. The state
// change itself is always recorded, so a log that suddenly goes quiet still
// says why.
func SetEnabled(module string, on bool) {
	key := normModule(module)
	if key == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if cur, known := enabled[key]; known && cur == on {
		return // already in that state; do not spam the log
	}
	enabled[key] = on
	state := "off"
	if on {
		state = "on"
	}
	writeLocked("SYS ", fmt.Sprintf("log module %s switched %s", key, state))
}

// IsEnabled reports whether a module's lines are being recorded. Unknown
// modules count as enabled — they register themselves on first use.
func IsEnabled(module string) bool {
	key := normModule(module)
	mu.Lock()
	defer mu.Unlock()
	on, known := enabled[key]
	return !known || on
}

// Modules lists every known module and its state: the shipped modules first,
// in display order, then anything that registered itself later, sorted.
func Modules() []ModuleState {
	mu.Lock()
	defer mu.Unlock()

	seen := make(map[string]bool, len(enabled))
	out := make([]ModuleState, 0, len(enabled))
	for _, name := range knownModules {
		if on, ok := enabled[name]; ok {
			out = append(out, ModuleState{Module: name, Enabled: on})
			seen[name] = true
		}
	}
	extra := make([]string, 0, len(enabled))
	for name := range enabled {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		out = append(out, ModuleState{Module: name, Enabled: enabled[name]})
	}
	return out
}

// applyDisabledModulesEnv resets the module registry to the environment's
// view of it: every shipped module on, then the ones named in
// ZTNA_LOG_DISABLED_MODULES ("HTTP,DNS") switched off. Init owns module
// state, so a second Init re-reads the environment rather than inheriting
// runtime toggles. Must be called with mu held.
func applyDisabledModulesEnv(raw string) {
	enabled = defaultModuleState()
	for _, part := range strings.Split(raw, ",") {
		if key := normModule(part); key != "" {
			enabled[key] = false
		}
	}
}

// Path returns the log file currently being written, or "" when logging is
// degraded to stdout only.
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	return curPath
}

// Stat returns a snapshot of the log file state.
func Stat() Stats {
	mu.Lock()
	defer mu.Unlock()
	st := Stats{
		Path:     curPath,
		Size:     written,
		Lines:    lineCnt,
		MaxLines: maxLines,
		MaxBytes: maxBytes,
	}
	if curPath != "" {
		if _, err := os.Stat(curPath + ".1"); err == nil {
			st.HasBackup = true
		}
	}
	return st
}

// rotate closes the current file, renames it to <path>.1 and opens a fresh
// one. Must be called with mu held.
func rotate() {
	if file != nil {
		_ = file.Close()
		file = nil
	}

	backup := curPath + ".1"
	_ = os.Remove(backup)
	_ = os.Rename(curPath, backup)

	f, err := openLog(curPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger: rotate failed, logging to stdout only: %v\n", err)
		written, lineCnt = 0, 0
		return
	}
	file = f
	written, lineCnt = 0, 0

	ts := time.Now().Format("2006-01-02 15:04:05")
	notice := fmt.Sprintf("%s  [SYS ] log recycled (previous file saved as %s)\n", ts, backup)
	n, _ := file.WriteString(notice)
	_ = file.Sync()
	fmt.Print(notice)
	written += int64(n)
	lineCnt++
}

// Tail returns the last n lines of the log. If the current file holds fewer
// than n lines (typically right after a recycle) the remainder is taken from
// the .1 backup so the view does not go blank.
func Tail(n int) ([]string, error) {
	mu.Lock()
	path := curPath
	mu.Unlock()

	if path == "" {
		return nil, fmt.Errorf("log file is not available (check ZTNA_LOG_PATH and its permissions)")
	}
	if n <= 0 {
		return nil, nil
	}

	lines, err := tailFile(path, n)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(lines) < n {
		if prev, err := tailFile(path+".1", n-len(lines)); err == nil && len(prev) > 0 {
			lines = append(prev, lines...)
		}
	}
	return lines, nil
}

// tailFile reads the last n lines of a file by walking backwards from the
// end in chunks, so the cost is proportional to what is returned rather than
// to the size of the file.
func tailFile(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if size == 0 {
		return nil, nil
	}

	const chunk = 64 * 1024
	var buf []byte
	pos := size
	for pos > 0 {
		readSize := int64(chunk)
		if pos < readSize {
			readSize = pos
		}
		pos -= readSize

		b := make([]byte, readSize)
		if _, err := f.ReadAt(b, pos); err != nil && err != io.EOF {
			return nil, err
		}
		buf = append(b, buf...)

		// n complete lines need n+1 newlines once the final one is counted;
		// stopping at > n leaves a partial first line that is dropped below.
		if bytes.Count(buf, []byte{'\n'}) > n || len(buf) >= maxTailBytes {
			break
		}
	}

	text := strings.TrimSuffix(string(buf), "\n")
	if text == "" {
		return nil, nil
	}
	all := strings.Split(text, "\n")
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

// countLines counts the newlines in a file without loading it into memory.
func countLines(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var (
		total int64
		buf   = make([]byte, 64*1024)
	)
	for {
		n, err := f.Read(buf)
		total += int64(bytes.Count(buf[:n], []byte{'\n'}))
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

func envInt64(key string, def, minimum int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < minimum {
		fmt.Fprintf(os.Stderr, "logger: ignoring invalid %s=%q (using %d)\n", key, v, def)
		return def
	}
	return n
}
