package dns

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	mdns "github.com/miekg/dns"

	"ztna-lab/logger"
)

// Server é o listener UDP/53 que responde queries DNS.
type Server struct {
	addr     string
	upstream string

	mu      sync.Mutex
	srv     *mdns.Server
	running bool
}

// NewServer constrói o server. recordsPath é o JSON de registros locais
// (será carregado imediatamente). upstream é o resolver pra forward das
// queries que não casam com registros locais — ex.: "1.1.1.1:53".
func NewServer(recordsPath, upstream string) *Server {
	if err := LoadRecords(recordsPath); err != nil {
		logger.Log("DNS ", "warning loading records: "+err.Error())
	}
	if upstream == "" {
		upstream = "1.1.1.1:53"
	}
	return &Server{
		addr:     ":53",
		upstream: upstream,
	}
}

// Start sobe o servidor UDP. Não bloqueia.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("DNS já está rodando")
	}

	mux := mdns.NewServeMux()
	mux.HandleFunc(".", s.handle)

	s.srv = &mdns.Server{
		Addr:    s.addr,
		Net:     "udp",
		Handler: mux,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.srv.ListenAndServe()
	}()

	// pequena espera pra detectar erros imediatos (porta ocupada, sem
	// permissão, etc.)
	select {
	case err := <-errCh:
		s.srv = nil
		return fmt.Errorf("falha ao iniciar DNS: %w", err)
	case <-time.After(200 * time.Millisecond):
	}

	s.running = true
	logger.Log("DNS ", fmt.Sprintf("server started UDP%s  upstream=%s", s.addr, s.upstream))
	return nil
}

// Stop encerra o servidor com timeout curto.
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return fmt.Errorf("DNS não está rodando")
	}
	if err := s.srv.Shutdown(); err != nil {
		return err
	}
	s.srv = nil
	s.running = false
	logger.Log("DNS ", "server stopped")
	return nil
}

// IsRunning indica se o listener está ativo.
func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// handle processa uma query: tenta local, senão faz forward.
func (s *Server) handle(w mdns.ResponseWriter, r *mdns.Msg) {
	if len(r.Question) == 0 {
		mdns.HandleFailed(w, r)
		return
	}
	q := r.Question[0]
	clientIP := remoteIP(w.RemoteAddr())

	switch q.Qtype {
	case mdns.TypeA:
		s.answerA(w, r, q, clientIP)
	case mdns.TypeCNAME:
		s.answerCNAME(w, r, q, clientIP)
	default:
		// outros tipos (AAAA, MX, TXT, ...) vão direto pra upstream
		s.forward(w, r, q, clientIP)
	}
}

func (s *Server) answerA(w mdns.ResponseWriter, r *mdns.Msg, q mdns.Question, client string) {
	name := strings.ToLower(strings.TrimSuffix(q.Name, "."))
	ip, chain, ok := lookupA(name)
	if !ok {
		// nada local — forward
		s.forward(w, r, q, client)
		return
	}

	reply := new(mdns.Msg)
	reply.SetReply(r)
	reply.Authoritative = true

	// inclui o trail de CNAMEs no payload se houve cadeia
	for _, hop := range chain {
		parts := strings.SplitN(hop, " -> ", 2)
		if len(parts) != 2 {
			continue
		}
		c := &mdns.CNAME{
			Hdr:    mdns.RR_Header{Name: dnsFqdn(parts[0]), Rrtype: mdns.TypeCNAME, Class: mdns.ClassINET, Ttl: 60},
			Target: dnsFqdn(parts[1]),
		}
		reply.Answer = append(reply.Answer, c)
	}

	a := &mdns.A{
		Hdr: mdns.RR_Header{Name: q.Name, Rrtype: mdns.TypeA, Class: mdns.ClassINET, Ttl: 60},
		A:   net.ParseIP(ip).To4(),
	}
	reply.Answer = append(reply.Answer, a)

	_ = w.WriteMsg(reply)
	logger.Log("DNS ", fmt.Sprintf("from=%s  A %s = %s  (local)", client, name, ip))
}

func (s *Server) answerCNAME(w mdns.ResponseWriter, r *mdns.Msg, q mdns.Question, client string) {
	name := strings.ToLower(strings.TrimSuffix(q.Name, "."))
	store.mu.RLock()
	target, ok := store.CNAME[name]
	store.mu.RUnlock()

	if !ok {
		s.forward(w, r, q, client)
		return
	}
	reply := new(mdns.Msg)
	reply.SetReply(r)
	reply.Authoritative = true
	reply.Answer = append(reply.Answer, &mdns.CNAME{
		Hdr:    mdns.RR_Header{Name: q.Name, Rrtype: mdns.TypeCNAME, Class: mdns.ClassINET, Ttl: 60},
		Target: dnsFqdn(target),
	})
	_ = w.WriteMsg(reply)
	logger.Log("DNS ", fmt.Sprintf("from=%s  CNAME %s = %s  (local)", client, name, target))
}

func (s *Server) forward(w mdns.ResponseWriter, r *mdns.Msg, q mdns.Question, client string) {
	c := &mdns.Client{Net: "udp", Timeout: 3 * time.Second}
	resp, _, err := c.Exchange(r, s.upstream)
	if err != nil {
		logger.Log("DNS ", fmt.Sprintf("from=%s  forward error: %v", client, err))
		mdns.HandleFailed(w, r)
		return
	}
	_ = w.WriteMsg(resp)
	logger.Log("DNS ", fmt.Sprintf("from=%s  %s %s  (forwarded)", client, mdns.TypeToString[q.Qtype], q.Name))
}

func dnsFqdn(s string) string {
	if strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}

func remoteIP(addr net.Addr) string {
	if a, ok := addr.(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return addr.String()
}
