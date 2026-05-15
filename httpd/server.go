// Package httpd é o servidor HTTP de teste (porta 80) usado como alvo dos
// ZTNA gateways. Mostra exatamente o que chega: IP, headers, body — útil
// pra debugar transformações feitas por proxies, gateways e WAFs.
//
// Handlers:
//   /            → inspector (página principal com widgets, em inspector.go)
//   /inspector   → mesma página
//   /headers     → headers em text/plain (curl-friendly)
//   /download/N  → N bytes aleatórios
//   /upload      → POST de body, retorna size
//   /health      → liveness probe JSON
package httpd

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ztna-lab/logger"
)

// Server é o listener HTTP de teste.
type Server struct {
	addr string

	mu      sync.Mutex
	srv     *http.Server
	running bool
}

// NewServer cria o server. addr no formato ":80" ou "0.0.0.0:80".
func NewServer(addr string) *Server {
	if addr == "" {
		addr = ":80"
	}
	return &Server{addr: addr}
}

// Start sobe o servidor. Não bloqueia.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("HTTP já está rodando")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)                   // redireciona pra /inspector ou 404
	mux.HandleFunc("/inspector", s.handleInspector)     // em inspector.go
	mux.HandleFunc("/headers", s.handleHeaders)
	mux.HandleFunc("/download/", s.handleDownload)
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/health", s.handleHealth)

	s.srv = &http.Server{
		Addr:              s.addr,
		Handler:           s.logMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		s.srv = nil
		return fmt.Errorf("falha ao bindar %s: %w", s.addr, err)
	}

	go func() {
		_ = s.srv.Serve(ln)
	}()

	s.running = true
	logger.Log("HTTP", fmt.Sprintf("server started TCP%s", s.addr))
	return nil
}

// Stop encerra com grace period.
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return fmt.Errorf("HTTP não está rodando")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		return err
	}
	s.srv = nil
	s.running = false
	logger.Log("HTTP", "server stopped")
	return nil
}

// IsRunning indica se o listener está ativo.
func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// ─────────────────────── middleware ───────────────────────

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Log("HTTP", fmt.Sprintf("from=%s  %s %s  ua=%q  %s",
			clientIP(r), r.Method, r.URL.Path, r.UserAgent(),
			time.Since(start).Round(time.Millisecond)))
	})
}

// ─────────────────────── handlers ───────────────────────

// handleRoot trata "/" — redireciona pra /inspector (página principal).
// Qualquer outro path cai aqui também (porque "/" é o catch-all do mux),
// então tratamos isso com NotFound explícito.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		s.handleInspector(w, r)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	keys := make([]string, 0, len(r.Header))
	for k := range r.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range r.Header[k] {
			fmt.Fprintf(w, "%s: %s\n", k, v)
		}
	}
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	sizeStr := strings.TrimPrefix(r.URL.Path, "/download/")
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil || size <= 0 || size > 1<<30 {
		http.Error(w, "size must be 1..1073741824 bytes (1 GiB max)", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="random-%d.bin"`, size))
	buf := make([]byte, 64*1024)
	remaining := size
	for remaining > 0 {
		n := int64(len(buf))
		if n > remaining {
			n = remaining
		}
		_, _ = rand.Read(buf[:n])
		if _, err := w.Write(buf[:n]); err != nil {
			return
		}
		remaining -= n
	}
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		w.Header().Set("Allow", "POST, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	n, err := io.Copy(io.Discard, r.Body)
	if err != nil {
		http.Error(w, "read error: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"received_bytes":%d,"content_type":%q}`, n, r.Header.Get("Content-Type"))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}

// ─────────────────────── helpers ───────────────────────

// clientIP retorna o IP do cliente, respeitando X-Forwarded-For e X-Real-IP
// se presentes (caso o appliance esteja atrás de um proxy/gateway).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx >= 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if real := r.Header.Get("X-Real-IP"); real != "" {
		return strings.TrimSpace(real)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
