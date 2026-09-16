package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reset puts the package globals back to a known state between tests.
func reset(t *testing.T) string {
	t.Helper()
	mu.Lock()
	if file != nil {
		_ = file.Close()
	}
	file, curPath, written, lineCnt = nil, "", 0, 0
	enabled = defaultModuleState()
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		if file != nil {
			_ = file.Close()
			file = nil
		}
		curPath = ""
		mu.Unlock()
	})
	return filepath.Join(t.TempDir(), "sub", "ztna_lab.log")
}

func TestInitCreatesMissingDirectory(t *testing.T) {
	path := reset(t)
	t.Setenv("ZTNA_LOG_MAX_LINES", "1000")
	Init(path)

	if Path() != path {
		t.Fatalf("Path() = %q, want %q", Path(), path)
	}
	Log("SYS ", "hello")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("log file not created: %v", err)
	}
}

func TestInitFallsBackWhenPathUnusable(t *testing.T) {
	reset(t)
	dir := t.TempDir()
	blocker := filepath.Join(dir, "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// <file>/ztna_lab.log can never be opened, so Init must fall back
	// instead of leaving Tail permanently broken.
	Init(filepath.Join(blocker, "ztna_lab.log"))
	if Path() == "" {
		t.Fatal("logger degraded to stdout-only instead of falling back to a temp file")
	}
	Log("SYS ", "after fallback")
	lines, err := Tail(10)
	if err != nil {
		t.Fatalf("Tail after fallback: %v", err)
	}
	if len(lines) == 0 || !strings.Contains(lines[len(lines)-1], "after fallback") {
		t.Fatalf("fallback log did not capture the line: %v", lines)
	}
	_ = os.Remove(Path())
}

func TestRecyclesAtLineLimit(t *testing.T) {
	path := reset(t)
	t.Setenv("ZTNA_LOG_MAX_LINES", "100")
	Init(path)

	for i := 0; i < 250; i++ {
		Log("SYS ", fmt.Sprintf("line %d", i))
	}

	st := Stat()
	if st.Lines > st.MaxLines {
		t.Fatalf("current file holds %d lines, above the %d limit", st.Lines, st.MaxLines)
	}
	if !st.HasBackup {
		t.Fatal("expected a .1 backup after recycling")
	}

	// The file must never grow without bound: current + backup are both
	// capped, so at most ~2x the limit is ever on disk.
	for _, p := range []string{path, path + ".1"} {
		n, err := countLines(p)
		if err != nil {
			t.Fatalf("countLines(%s): %v", p, err)
		}
		if n > 100 {
			t.Fatalf("%s holds %d lines, above the 100 limit", p, n)
		}
	}
}

func TestRecyclesAtByteLimit(t *testing.T) {
	path := reset(t)
	t.Setenv("ZTNA_LOG_MAX_LINES", "1000000")
	t.Setenv("ZTNA_LOG_MAX_BYTES", "65536")
	Init(path)

	big := strings.Repeat("x", 512)
	for i := 0; i < 400; i++ {
		Log("SYS ", big)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > 65536+1024 {
		t.Fatalf("file grew to %d bytes despite a 65536 byte limit", fi.Size())
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected a .1 backup after byte-limit recycling: %v", err)
	}
}

func TestInitRecyclesAnOversizedExistingFile(t *testing.T) {
	path := reset(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "old line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ZTNA_LOG_MAX_LINES", "100")
	Init(path)

	if st := Stat(); st.Lines > st.MaxLines {
		t.Fatalf("restart on an oversized file left %d lines in place", st.Lines)
	}
}

func TestTailReturnsNewestLinesLast(t *testing.T) {
	path := reset(t)
	t.Setenv("ZTNA_LOG_MAX_LINES", "10000")
	Init(path)

	for i := 0; i < 300; i++ {
		Log("SYS ", fmt.Sprintf("line %d", i))
	}

	lines, err := Tail(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 {
		t.Fatalf("Tail(5) returned %d lines", len(lines))
	}
	if !strings.HasSuffix(lines[4], "line 299") {
		t.Fatalf("last line = %q, want it to end with 'line 299'", lines[4])
	}
	if !strings.HasSuffix(lines[0], "line 295") {
		t.Fatalf("first line = %q, want it to end with 'line 295'", lines[0])
	}
}

func TestTailSpansTheBackupAfterRecycling(t *testing.T) {
	path := reset(t)
	t.Setenv("ZTNA_LOG_MAX_LINES", "100")
	Init(path)

	for i := 0; i < 102; i++ {
		Log("SYS ", fmt.Sprintf("line %d", i))
	}

	// Only a couple of lines live in the fresh file; the rest must come
	// from the .1 backup so the panel does not go blank right after a recycle.
	lines, err := Tail(50)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 50 {
		t.Fatalf("Tail(50) returned %d lines, want 50", len(lines))
	}
	if !strings.HasSuffix(lines[len(lines)-1], "line 101") {
		t.Fatalf("last line = %q, want it to end with 'line 101'", lines[len(lines)-1])
	}
}

func TestTailCapsAtAvailableLines(t *testing.T) {
	path := reset(t)
	Init(path)
	Log("SYS ", "only one")

	lines, err := Tail(500)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("Tail(500) on a one-line file returned %d lines", len(lines))
	}
}

func TestTailWithoutAFileReportsAnError(t *testing.T) {
	reset(t)
	if _, err := Tail(10); err == nil {
		t.Fatal("expected an error when no log file is open")
	}
}

func TestDisabledModuleIsDroppedEntirely(t *testing.T) {
	path := reset(t)
	Init(path)

	SetEnabled("HTTP", false)
	for i := 0; i < 20; i++ {
		Log("HTTP", fmt.Sprintf("request %d", i))
	}
	Log("SSH ", "session opened id=1")

	lines, err := Tail(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if strings.Contains(l, "[HTTP]") {
			t.Fatalf("a disabled module still reached the log: %q", l)
		}
	}
	if !containsLine(lines, "session opened id=1") {
		t.Fatal("SSH line missing while HTTP was disabled")
	}
}

func TestDisablingOneModuleKeepsTheOthersVisible(t *testing.T) {
	path := reset(t)
	Init(path)

	// The reported symptom: HTTP logs every request on the test plane and
	// pushes SSH out of the tail window.
	Log("SSH ", "session opened id=1")
	for i := 0; i < 200; i++ {
		Log("HTTP", fmt.Sprintf("from=10.0.0.1 GET /health #%d", i))
	}
	if lines, _ := Tail(80); containsLine(lines, "session opened id=1") {
		t.Fatal("precondition failed: SSH should have been pushed out of an 80-line window")
	}

	SetEnabled("HTTP", false)
	Log("SSH ", "session opened id=2")
	for i := 0; i < 200; i++ {
		Log("HTTP", fmt.Sprintf("from=10.0.0.1 GET /health #%d", i))
	}

	lines, err := Tail(80)
	if err != nil {
		t.Fatal(err)
	}
	if !containsLine(lines, "session opened id=2") {
		t.Fatal("SSH still buried after switching HTTP off")
	}
}

func TestModuleToggleIsAlwaysRecorded(t *testing.T) {
	path := reset(t)
	Init(path)

	// Even switching SYS off must leave a trace, otherwise a silent log is
	// indistinguishable from a broken one.
	SetEnabled("SYS", false)
	lines, err := Tail(10)
	if err != nil {
		t.Fatal(err)
	}
	if !containsLine(lines, "log module SYS switched off") {
		t.Fatalf("the switch-off was not recorded: %v", lines)
	}
	Log("SYS ", "this one must be dropped")
	lines, _ = Tail(10)
	if containsLine(lines, "this one must be dropped") {
		t.Fatal("SYS lines still recorded after switching the module off")
	}
}

func TestModulePaddingAndCaseAreNormalized(t *testing.T) {
	path := reset(t)
	Init(path)

	SetEnabled("ssh", false) // call sites use "SSH " with padding
	Log("SSH ", "should be dropped")
	if lines, _ := Tail(20); containsLine(lines, "should be dropped") {
		t.Fatal(`SetEnabled("ssh") did not match Log("SSH ")`)
	}
	if IsEnabled("SSH ") {
		t.Fatal("IsEnabled disagrees with SetEnabled about the same module")
	}
}

func TestUnknownModuleRegistersEnabled(t *testing.T) {
	path := reset(t)
	Init(path)

	Log("GEO ", "lookup ok")
	if lines, _ := Tail(20); !containsLine(lines, "lookup ok") {
		t.Fatal("a new module should log by default")
	}
	found := false
	for _, m := range Modules() {
		if m.Module == "GEO" {
			found, _ = true, m.Enabled
			if !m.Enabled {
				t.Fatal("a newly seen module should register as enabled")
			}
		}
	}
	if !found {
		t.Fatalf("new module missing from Modules(): %v", Modules())
	}
}

func TestDisabledModulesEnvIsApplied(t *testing.T) {
	path := reset(t)
	t.Setenv("ZTNA_LOG_DISABLED_MODULES", "http, dns")
	Init(path)

	Log("HTTP", "nope")
	Log("DNS ", "nope")
	Log("SSH ", "yes")
	lines, _ := Tail(20)
	if containsLine(lines, "nope") {
		t.Fatalf("ZTNA_LOG_DISABLED_MODULES ignored: %v", lines)
	}
	if !containsLine(lines, "yes") {
		t.Fatal("ZTNA_LOG_DISABLED_MODULES disabled a module it should not have")
	}
}

func TestModulesListsShippedModulesInOrder(t *testing.T) {
	path := reset(t)
	Init(path)

	got := Modules()
	if len(got) < len(knownModules) {
		t.Fatalf("Modules() = %v, want at least the shipped modules", got)
	}
	for i, want := range knownModules {
		if got[i].Module != want {
			t.Fatalf("Modules()[%d] = %q, want %q", i, got[i].Module, want)
		}
	}
}

func containsLine(lines []string, substr string) bool {
	for _, l := range lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}
