// Package admin expõe a API HTTP de gerenciamento do appliance ZTNA Lab.
//
// A API é separada do plano de teste (porta 80). Aqui ficam:
//   - controle de start/stop dos servidores DNS, HTTP e SSH
//   - CRUD de registros DNS (A e CNAME)
//   - leitura de status, sessões SSH e tail de log
//   - execução do latency runner
//
// Autenticação opcional via header "Authorization: Bearer <token>",
// configurada por ZTNA_ADMIN_TOKEN. Se vazio, a API fica aberta (modo lab).
package admin

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ztna-lab/dns"
	"ztna-lab/httpd"
	"ztna-lab/logger"
	"ztna-lab/sshd"
)

//go:embed ui.html
var indexHTML []byte

// Server é a API de gerenciamento. Mantém referências aos servidores
// rodando para start/stop e consulta de estado.
type Server struct {
	addr  string
	token string

	dnsSrv  *dns.Server
	httpSrv *httpd.Server
	sshSrv  *sshd.Server

	httpServer *http.Server
}

// New constrói o servidor. addr é "host:port" (ex.: "0.0.0.0:9000").
// token vazio desativa autenticação.
func New(addr, token string, dnsSrv *dns.Server, httpSrv *httpd.Server, sshSrv *sshd.Server) *Server {
	return &Server{
		addr:    addr,
		token:   token,
		dnsSrv:  dnsSrv,
		httpSrv: httpSrv,
		sshSrv:  sshSrv,
	}
}

// Start sobe a API. Não bloqueia.
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// UI estática
	mux.HandleFunc("/", s.handleIndex)

	// API JSON
	mux.HandleFunc("/api/status", s.auth(s.handleStatus))
	mux.HandleFunc("/api/dns/start", s.auth(s.handleDNSStart))
	mux.HandleFunc("/api/dns/stop", s.auth(s.handleDNSStop))
	mux.HandleFunc("/api/dns/records", s.auth(s.handleDNSRecords))
	mux.HandleFunc("/api/dns/records/", s.auth(s.handleDNSRecordDelete))
	mux.HandleFunc("/api/http/start", s.auth(s.handleHTTPStart))
	mux.HandleFunc("/api/http/stop", s.auth(s.handleHTTPStop))
	mux.HandleFunc("/api/ssh/start", s.auth(s.handleSSHStart))
	mux.HandleFunc("/api/ssh/stop", s.auth(s.handleSSHStop))
	mux.HandleFunc("/api/ssh/sessions", s.auth(s.handleSSHSessions))
	mux.HandleFunc("/api/log/tail", s.auth(s.handleLogTail))
	mux.HandleFunc("/api/latency", s.auth(s.handleLatency))
	mux.HandleFunc("/api/health", s.handleHealth) // sem auth: usado por healthcheck

	s.httpServer = &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Log("ADM ", fmt.Sprintf("Admin API started  TCP %s  auth:%s", s.addr, authMode(s.token)))
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Log("ADM ", "Admin API error: "+err.Error())
		}
	}()
	return nil
}

// Stop encerra a API com grace period.
func (s *Server) Stop() error {
	if s.httpServer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.httpServer.Shutdown(ctx)
}

// ---------- middleware ----------

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") || strings.TrimPrefix(h, "Bearer ") != s.token {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		next(w, r)
	}
}

func authMode(token string) string {
	if token == "" {
		return "open"
	}
	return "bearer"
}

// ---------- handlers ----------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"dns":  map[string]any{"running": s.dnsSrv != nil && s.dnsSrv.IsRunning(), "port": 53},
		"http": map[string]any{"running": s.httpSrv != nil && s.httpSrv.IsRunning(), "port": 80},
		"ssh":  map[string]any{"running": s.sshSrv != nil && s.sshSrv.IsRunning(), "port": 2222},
		"version": "appliance-1.0",
	})
}

// --- DNS ---

func (s *Server) handleDNSStart(w http.ResponseWriter, r *http.Request) {
	if err := s.dnsSrv.Start(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func (s *Server) handleDNSStop(w http.ResponseWriter, r *http.Request) {
	if err := s.dnsSrv.Stop(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// GET  /api/dns/records         → lista A e CNAME
// POST /api/dns/records         → adiciona { "type": "A|CNAME", "name": "...", "value": "..." }
func (s *Server) handleDNSRecords(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a, cname := dns.ListRecords()
		writeJSON(w, http.StatusOK, map[string]any{"a": a, "cname": cname})
	case http.MethodPost:
		var body struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		var err error
		switch strings.ToUpper(body.Type) {
		case "A":
			err = dns.AddA(body.Name, body.Value)
		case "CNAME":
			err = dns.AddCNAME(body.Name, body.Value)
		default:
			err = fmt.Errorf("type must be A or CNAME")
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"status": "added"})
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// DELETE /api/dns/records/{name}
func (s *Server) handleDNSRecordDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/dns/records/")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	if err := dns.Remove(name); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// --- HTTP test server ---

func (s *Server) handleHTTPStart(w http.ResponseWriter, r *http.Request) {
	if err := s.httpSrv.Start(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func (s *Server) handleHTTPStop(w http.ResponseWriter, r *http.Request) {
	if err := s.httpSrv.Stop(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// --- SSH ---

func (s *Server) handleSSHStart(w http.ResponseWriter, r *http.Request) {
	if err := s.sshSrv.Start(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func (s *Server) handleSSHStop(w http.ResponseWriter, r *http.Request) {
	if err := s.sshSrv.Stop(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) handleSSHSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.sshSrv.Sessions())
}

// --- Log ---

func (s *Server) handleLogTail(w http.ResponseWriter, r *http.Request) {
	n := 50
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 1000 {
			n = parsed
		}
	}
	lines, err := logger.Tail(n)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

// --- Latency ---

func (s *Server) handleLatency(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL      string `json:"url"`
		Count    int    `json:"count"`
		IntervalMs int  `json:"interval_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if body.Count == 0 {
		body.Count = 50
	}
	if body.IntervalMs == 0 {
		body.IntervalMs = 200
	}
	// Reaproveita a função do main.go. Assinatura assumida:
	//   func RunLatency(url string, count int, interval time.Duration) (LatencyResult, error)
	// Ajustar o import se a função estiver em outro pacote.
	res, err := runLatency(body.URL, body.Count, time.Duration(body.IntervalMs)*time.Millisecond)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
