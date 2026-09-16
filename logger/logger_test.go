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
