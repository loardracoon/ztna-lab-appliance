// Package sshd é um servidor SSH mock para uso como alvo de validação ZTNA.
//
// Aceita conexões com qualquer credencial e oferece um shell interativo
// minimalista que ecoa comandos e responde a alguns comandos pré-definidos
// (whoami, all-users, exit). NÃO É um shell real — é um endpoint de teste
// para validar que o ZTNA gateway permite/bloqueia tráfego SSH.
//
// Suporta chaves host RSA e/ou ED25519, configuradas via variáveis de
// ambiente lidas por ConfigFromEnv. O modo debug pode ser ativado ou
// desativado em runtime via SetDebug, sem reiniciar o servidor.
package sshd

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"ztna-lab/logger"
)

// Session é uma sessão SSH ativa, exposta pela Admin API.
type Session struct {
	ID          int       `json:"id"`
	User        string    `json:"user"`
	IP          string    `json:"ip"`
	ConnectedAt time.Time `json:"connected_at"`
}

// Server é o listener TCP/2222.
type Server struct {
	addr   string
	cfg    Config
	config *ssh.ServerConfig

	mu       sync.Mutex
	listener net.Listener
	running  bool

	sessMu   sync.RWMutex
	sessions map[int]*Session
	nextID   int64

	// debugFlag permite alternar o modo debug em runtime sem reiniciar.
	debugFlag atomic.Bool
}

// NewServer cria o server. addr no formato ":2222".
// cfg define as chaves host e estado inicial do debug (veja ConfigFromEnv).
func NewServer(addr string, cfg Config) *Server {
	if addr == "" {
		addr = ":2222"
	}
	s := &Server{
		addr:     addr,
		cfg:      cfg,
		sessions: map[int]*Session{},
	}
	s.debugFlag.Store(cfg.Debug)
	return s
}

// SetDebug ativa ou desativa o modo debug em runtime, sem reiniciar o servidor.
// A mudança é imediata: conexões subsequentes já usam o novo estado.
func (s *Server) SetDebug(on bool) {
	s.debugFlag.Store(on)
	state := map[bool]string{true: "ON", false: "OFF"}[on]
	logger.Log("SSH ", fmt.Sprintf("debug mode %s", state))
	if on {
		logger.Log("SSH ", "[debug] ciphers: aes128-gcm@openssh.com, aes256-gcm@openssh.com, chacha20-poly1305@openssh.com, aes128-ctr, aes192-ctr, aes256-ctr")
		logger.Log("SSH ", "[debug] kex: curve25519-sha256, curve25519-sha256@libssh.org, ecdh-sha2-nistp256, ecdh-sha2-nistp384, ecdh-sha2-nistp521, diffie-hellman-group14-sha256, diffie-hellman-group14-sha1")
		logger.Log("SSH ", "[debug] macs: hmac-sha2-256-etm@openssh.com, hmac-sha2-512-etm@openssh.com, hmac-sha2-256, hmac-sha2-512, hmac-sha1")
	}
}

// IsDebug retorna o estado atual do modo debug.
func (s *Server) IsDebug() bool {
	return s.debugFlag.Load()
}

// Start carrega/gera as host keys, configura o SSH server e começa a aceitar.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("SSH server is already running")
	}

	scfg := &ssh.ServerConfig{
		// Mock: aceita qualquer credencial. Útil para teste de gateway.
		PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			logger.Log("SSH ", fmt.Sprintf("auth attempt from=%s user=%s password=%q", c.RemoteAddr(), c.User(), password))
			return &ssh.Permissions{}, nil
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			logger.Log("SSH ", fmt.Sprintf("pubkey auth from=%s user=%s type=%s", c.RemoteAddr(), c.User(), key.Type()))
			return &ssh.Permissions{}, nil
		},
		// AuthLogCallback sempre registrado; a guarda está dentro do closure
		// para que toggles via SetDebug sejam refletidos imediatamente.
		AuthLogCallback: func(conn ssh.ConnMetadata, method string, err error) {
			if !s.IsDebug() {
				return
			}
			if err != nil {
				logger.Log("SSH ", fmt.Sprintf("[debug] auth rejected from=%s user=%s method=%s err=%v",
					conn.RemoteAddr(), conn.User(), method, err))
			} else {
				logger.Log("SSH ", fmt.Sprintf("[debug] auth accepted from=%s user=%s method=%s",
					conn.RemoteAddr(), conn.User(), method))
			}
		},
	}

	added := 0
	if s.cfg.RSAKeyPath != "" {
		signer, err := loadOrGenerateRSAKey(s.cfg.RSAKeyPath)
		if err != nil {
			return fmt.Errorf("rsa host key: %w", err)
		}
		scfg.AddHostKey(signer)
		added++
		logger.Log("SSH ", "host key RSA loaded: "+s.cfg.RSAKeyPath)
	}
	if s.cfg.Ed25519KeyPath != "" {
		signer, err := loadOrGenerateEd25519Key(s.cfg.Ed25519KeyPath)
		if err != nil {
			return fmt.Errorf("ed25519 host key: %w", err)
		}
		scfg.AddHostKey(signer)
		added++
		logger.Log("SSH ", "host key Ed25519 loaded: "+s.cfg.Ed25519KeyPath)
	}
	if added == 0 {
		return fmt.Errorf("no host key configured (ZTNA_SSH_KEY and ZTNA_SSH_ED25519_KEY are both empty)")
	}

	s.config = scfg

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("could not bind %s: %w", s.addr, err)
	}
	s.listener = ln
	s.running = true

	go s.acceptLoop()

	logger.Log("SSH ", fmt.Sprintf("server started TCP%s (debug=%v)", s.addr, s.IsDebug()))
	if s.IsDebug() {
		s.SetDebug(true) // emite os logs de algoritmos no startup se debug já ativo
	}
	return nil
}

// Stop fecha o listener e termina conexões em curso.
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return fmt.Errorf("SSH server is not running")
	}
	if err := s.listener.Close(); err != nil {
		return err
	}
	s.listener = nil
	s.running = false
	logger.Log("SSH ", "server stopped")
	return nil
}

// IsRunning indica se o listener está ativo.
func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Sessions retorna a lista de sessões ativas (snapshot).
func (s *Server) Sessions() []Session {
	s.sessMu.RLock()
	defer s.sessMu.RUnlock()
	out := make([]Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, *sess)
	}
	return out
}

// ─────────────────────── accept loop ───────────────────────

func (s *Server) acceptLoop() {
	for {
		s.mu.Lock()
		ln := s.listener
		s.mu.Unlock()
		if ln == nil {
			return
		}

		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(nConn net.Conn) {
	defer nConn.Close()

	if s.IsDebug() {
		logger.Log("SSH ", fmt.Sprintf("[debug] TCP connection from=%s", nConn.RemoteAddr()))
	}

	sshConn, chans, reqs, err := ssh.NewServerConn(nConn, s.config)
	if err != nil {
		if s.IsDebug() {
			logger.Log("SSH ", fmt.Sprintf("[debug] handshake error type=%T msg=%q from=%s",
				err, err.Error(), nConn.RemoteAddr()))
		}
		logger.Log("SSH ", fmt.Sprintf("handshake failed from=%s: %v", nConn.RemoteAddr(), err))
		return
	}
	defer sshConn.Close()

	if s.IsDebug() {
		logger.Log("SSH ", fmt.Sprintf("[debug] handshake ok from=%s user=%s clientVersion=%q",
			sshConn.RemoteAddr(), sshConn.User(), sshConn.ClientVersion()))
	}

	sid := int(atomic.AddInt64(&s.nextID, 1))
	sess := &Session{
		ID:          sid,
		User:        sshConn.User(),
		IP:          ipOnly(sshConn.RemoteAddr()),
		ConnectedAt: time.Now(),
	}
	s.sessMu.Lock()
	s.sessions[sid] = sess
	s.sessMu.Unlock()
	logger.Log("SSH ", fmt.Sprintf("session opened id=%d user=%s from=%s", sid, sess.User, sess.IP))

	defer func() {
		s.sessMu.Lock()
		delete(s.sessions, sid)
		s.sessMu.Unlock()
		logger.Log("SSH ", fmt.Sprintf("session closed id=%d", sid))
	}()

	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "only session channels supported")
			continue
		}
		ch, requests, err := newCh.Accept()
		if err != nil {
			continue
		}
		go handleShell(ch, requests, s, sess)
	}
}

// handleShell oferece um "shell" minimalista. Aceita comandos:
//
//	whoami      → usuario e IP de origem da sessão atual
//	all-users   → tabela com todos os usuários conectados e seus IPs
//	exit/quit   → fecha a sessão
//	outros      → eco do que foi digitado
func handleShell(ch ssh.Channel, requests <-chan *ssh.Request, srv *Server, sess *Session) {
	defer ch.Close()

	go func() {
		for req := range requests {
			switch req.Type {
			case "shell", "pty-req":
				if req.WantReply {
					req.Reply(true, nil)
				}
			default:
				if req.WantReply {
					req.Reply(false, nil)
				}
			}
		}
	}()

	fmt.Fprintf(ch, "\r\n*** ZTNA Lab — SSH mock target ***\r\n")
	fmt.Fprintf(ch, "user=%s  ip=%s  session=%d\r\n", sess.User, sess.IP, sess.ID)
	fmt.Fprintf(ch, "Commands: whoami, all-users, exit. Anything else is echoed.\r\n\r\n")

	buf := make([]byte, 0, 256)
	prompt := func() { fmt.Fprintf(ch, "%s@ztna-lab$ ", sess.User) }
	prompt()

	one := make([]byte, 1)
	for {
		_, err := io.ReadFull(ch, one)
		if err != nil {
			return
		}
		c := one[0]
		switch c {
		case '\r', '\n':
			line := string(buf)
			buf = buf[:0]
			fmt.Fprint(ch, "\r\n")
			switch line {
			case "":
			case "whoami":
				fmt.Fprintf(ch, "user=%s  ip=%s\r\n", sess.User, sess.IP)
			case "all-users":
				printAllUsers(ch, srv)
			case "exit", "quit", "logout":
				fmt.Fprint(ch, "bye.\r\n")
				return
			default:
				fmt.Fprintf(ch, "%s\r\n", line)
			}
			prompt()
		case 0x7f, 0x08: // backspace
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
				fmt.Fprint(ch, "\b \b")
			}
		case 0x03, 0x04: // ctrl-c / ctrl-d
			fmt.Fprint(ch, "\r\nbye.\r\n")
			return
		default:
			buf = append(buf, c)
			ch.Write([]byte{c})
		}
	}
}

func printAllUsers(ch ssh.Channel, srv *Server) {
	sessions := srv.Sessions()
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })

	fmt.Fprintf(ch, "\r\n%-5s  %-16s  %-15s  %s\r\n", "ID", "USER", "IP", "CONNECTED")
	fmt.Fprintf(ch, "%s\r\n", strings.Repeat("-", 62))
	for _, s := range sessions {
		fmt.Fprintf(ch, "%-5d  %-16s  %-15s  %s\r\n",
			s.ID,
			truncate(s.User, 16),
			truncate(s.IP, 15),
			s.ConnectedAt.Format("2006-01-02 15:04:05"),
		)
	}
	if len(sessions) == 0 {
		fmt.Fprint(ch, "(no active sessions)\r\n")
	}
	fmt.Fprint(ch, "\r\n")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + ">"
}

// ─────────────────────── host keys ───────────────────────

func loadOrGenerateRSAKey(path string) (ssh.Signer, error) {
	if data, err := os.ReadFile(path); err == nil {
		if signer, err := ssh.ParsePrivateKey(data); err == nil {
			return signer, nil
		}
		logger.Log("SSH ", fmt.Sprintf("RSA host key at %s unreadable, regenerating", path))
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	pemBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	pemBytes := pem.EncodeToMemory(pemBlock)
	persistKey(path, pemBytes, "RSA")
	return ssh.ParsePrivateKey(pemBytes)
}

func loadOrGenerateEd25519Key(path string) (ssh.Signer, error) {
	if data, err := os.ReadFile(path); err == nil {
		if signer, err := ssh.ParsePrivateKey(data); err == nil {
			return signer, nil
		}
		logger.Log("SSH ", fmt.Sprintf("Ed25519 host key at %s unreadable, regenerating", path))
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(pemBlock)
	persistKey(path, pemBytes, "Ed25519")
	return ssh.ParsePrivateKey(pemBytes)
}

func persistKey(path string, data []byte, keyType string) {
	if path == "" {
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		logger.Log("SSH ", fmt.Sprintf("warning: could not persist %s host key to %s: %v", keyType, path, err))
	} else {
		logger.Log("SSH ", fmt.Sprintf("generated new %s host key at %s", keyType, path))
	}
}

func ipOnly(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
