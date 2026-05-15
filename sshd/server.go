// Package sshd é um servidor SSH mock para uso como alvo de validação ZTNA.
//
// Aceita conexões com qualquer credencial e oferece um shell interativo
// minimalista que ecoa comandos e responde a alguns comandos pré-definidos
// (whoami, exit). NÃO É um shell real — é um endpoint de teste pra validar
// que o ZTNA gateway permite/bloqueia tráfego SSH e ver o que chega.
//
// A chave host RSA é persistida no disco para que clientes SSH não fiquem
// vendo "host key changed" a cada restart do appliance.
package sshd

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
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
	addr     string
	keyPath  string
	config   *ssh.ServerConfig

	mu       sync.Mutex
	listener net.Listener
	running  bool

	sessMu   sync.RWMutex
	sessions map[int]*Session
	nextID   int64
}

// NewServer cria o server. addr no formato ":2222". hostKeyPath é o
// caminho da chave host (será criada se não existir).
func NewServer(addr, hostKeyPath string) *Server {
	if addr == "" {
		addr = ":2222"
	}
	return &Server{
		addr:     addr,
		keyPath:  hostKeyPath,
		sessions: map[int]*Session{},
	}
}

// Start carrega/gera a host key, configura o SSH server e começa a aceitar.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("SSH já está rodando")
	}

	signer, err := loadOrGenerateHostKey(s.keyPath)
	if err != nil {
		return fmt.Errorf("host key: %w", err)
	}

	s.config = &ssh.ServerConfig{
		// Mock: aceita qualquer credencial. Útil para teste de gateway.
		PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			logger.Log("SSH ", fmt.Sprintf("auth attempt from=%s user=%s password=%q", c.RemoteAddr(), c.User(), password))
			return &ssh.Permissions{}, nil
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			logger.Log("SSH ", fmt.Sprintf("pubkey auth from=%s user=%s type=%s", c.RemoteAddr(), c.User(), key.Type()))
			return &ssh.Permissions{}, nil
		},
	}
	s.config.AddHostKey(signer)

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("falha ao bindar %s: %w", s.addr, err)
	}
	s.listener = ln
	s.running = true

	go s.acceptLoop()

	logger.Log("SSH ", fmt.Sprintf("server started TCP%s", s.addr))
	return nil
}

// Stop fecha o listener e termina conexões em curso.
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return fmt.Errorf("SSH não está rodando")
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
			// listener fechado ou erro fatal
			return
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(nConn net.Conn) {
	defer nConn.Close()

	sshConn, chans, reqs, err := ssh.NewServerConn(nConn, s.config)
	if err != nil {
		logger.Log("SSH ", fmt.Sprintf("handshake failed from=%s: %v", nConn.RemoteAddr(), err))
		return
	}
	defer sshConn.Close()

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
		go handleShell(ch, requests, sess)
	}
}

// handleShell oferece um "shell" minimalista. Aceita comandos:
//   whoami → echo do user
//   exit / quit → fecha a sessão
//   qualquer outra coisa → echo
func handleShell(ch ssh.Channel, requests <-chan *ssh.Request, sess *Session) {
	defer ch.Close()

	// aceita os requests de pty/shell, ignora o resto
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
	fmt.Fprintf(ch, "user=%s  session=%d\r\n", sess.User, sess.ID)
	fmt.Fprintf(ch, "Commands: whoami, exit. Anything else is echoed.\r\n\r\n")

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
				fmt.Fprintf(ch, "%s\r\n", sess.User)
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

// ─────────────────────── host key ───────────────────────

func loadOrGenerateHostKey(path string) (ssh.Signer, error) {
	if data, err := os.ReadFile(path); err == nil {
		signer, err := ssh.ParsePrivateKey(data)
		if err == nil {
			return signer, nil
		}
		logger.Log("SSH ", fmt.Sprintf("host key at %s is unreadable, regenerating: %v", path, err))
	}

	// gera RSA-2048
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	pemBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	pemBytes := pem.EncodeToMemory(pemBlock)

	if path != "" {
		if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
			logger.Log("SSH ", fmt.Sprintf("warning: could not persist host key to %s: %v", path, err))
		} else {
			logger.Log("SSH ", "generated new host key at "+path)
		}
	}
	return ssh.ParsePrivateKey(pemBytes)
}

func ipOnly(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
