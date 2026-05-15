// inspector.go — handler do /inspector (página principal de teste).
//
// Renderiza a página HTML organizada em widgets:
//   - status bar com trace cliente → gateway → servidor
//   - origem & forwarding (IPs + chain XFF + geo do IP real)
//   - identidade ZTNA (user, auth source, gateway)
//   - request HTTP (method/path/host/proto)
//   - transporte (TLS info ou plain HTTP)
//   - headers agrupados em 3 categorias
//   - ferramentas de teste (download/upload/health)
package httpd

import (
	"crypto/tls"
	"embed"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"ztna-lab/logger"
)

//go:embed inspector.html
var inspectorFS embed.FS

var inspectorTmpl *template.Template

func init() {
	t, err := template.New("inspector.html").Funcs(template.FuncMap{
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "…"
		},
		"join": strings.Join,
	}).ParseFS(inspectorFS, "inspector.html")
	if err != nil {
		panic(fmt.Sprintf("inspector template: %v", err))
	}
	inspectorTmpl = t
}

// InspectorData é o modelo passado ao template.
type InspectorData struct {
	ServerTime    string
	Method        string
	Host          string
	Path          string
	Proto         string
	ContentLength int64

	RealClientIP string
	DirectPeer   string
	ForwardChain []string
	Proxied      bool

	HeaderGroups []HeaderGroup
	HeaderTotal  int

	TLS  TLSInfo
	Geo  *GeoInfo

	AuthUser    string
	AuthMethod  string
	AuthSource  string
	Gateway     string
	HasIdentity bool
}

type HeaderGroup struct {
	Category string
	Color    string // "info" | "success" | "neutral"
	Count    int
	Entries  []HeaderEntry
}

type HeaderEntry struct {
	Name  string
	Value string
}

type TLSInfo struct {
	Plain    bool
	Version  string
	Cipher   string
	SNI      string
	ProtoFwd string // X-Forwarded-Proto (cliente original)
}

func (s *Server) handleInspector(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	data := buildInspectorData(r)
	if err := inspectorTmpl.Execute(w, data); err != nil {
		logger.Log("HTTP", fmt.Sprintf("inspector exec: %v", err))
	}
}

func buildInspectorData(r *http.Request) InspectorData {
	realIP := clientIP(r)
	directPeer := r.RemoteAddr

	// Forwarding chain
	var chain []string
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for _, p := range strings.Split(xff, ",") {
			chain = append(chain, strings.TrimSpace(p))
		}
	}
	directIP := ipFromAddr(directPeer)
	if len(chain) == 0 || chain[len(chain)-1] != directIP {
		chain = append(chain, directIP)
	}

	proxied := r.Header.Get("X-Forwarded-For") != "" ||
		r.Header.Get("X-Real-IP") != "" ||
		r.Header.Get("Via") != ""

	// Auth detection
	authMethod := ""
	if a := r.Header.Get("Authorization"); a != "" {
		switch {
		case strings.HasPrefix(a, "Bearer "):
			authMethod = "Bearer token"
		case strings.HasPrefix(a, "Basic "):
			authMethod = "Basic auth"
		case strings.HasPrefix(a, "Digest "):
			authMethod = "Digest auth"
		default:
			authMethod = "Custom scheme"
		}
	}

	authUser := firstNonEmptyHeader(r,
		"X-User-Email",
		"X-Authenticated-User",
		"X-User",
		"Cf-Access-Authenticated-User-Email",
		"X-Goog-Authenticated-User-Email",
	)

	gateway := r.Header.Get("X-Auth-Source")
	if gateway == "" {
		if via := r.Header.Get("Via"); via != "" {
			if parts := strings.Fields(via); len(parts) >= 2 {
				gateway = parts[1]
			}
		}
	}

	// Headers agrupados
	groups := groupHeaders(r.Header)
	total := 0
	for _, g := range groups {
		total += g.Count
	}

	// TLS
	tlsInfo := TLSInfo{ProtoFwd: r.Header.Get("X-Forwarded-Proto")}
	if r.TLS != nil {
		tlsInfo.Version = tlsVersionName(r.TLS.Version)
		tlsInfo.Cipher = tls.CipherSuiteName(r.TLS.CipherSuite)
		tlsInfo.SNI = r.TLS.ServerName
	} else {
		tlsInfo.Plain = true
	}

	// Geo do IP real (não do peer direto, que normalmente é o gateway)
	geo := LookupGeo(realIP)

	return InspectorData{
		ServerTime:    time.Now().Format("02 Jan 2006, 15:04:05 MST"),
		Method:        r.Method,
		Host:          r.Host,
		Path:          r.URL.String(),
		Proto:         r.Proto,
		ContentLength: r.ContentLength,
		RealClientIP:  realIP,
		DirectPeer:    directPeer,
		ForwardChain:  chain,
		Proxied:       proxied,
		HeaderGroups:  groups,
		HeaderTotal:   total,
		TLS:           tlsInfo,
		Geo:           geo,
		AuthUser:      authUser,
		AuthMethod:    authMethod,
		AuthSource:    r.Header.Get("X-Auth-Source"),
		Gateway:       gateway,
		HasIdentity:   authUser != "" || authMethod != "" || gateway != "",
	}
}

// groupHeaders separa headers em forwarding / authentication / standard.
func groupHeaders(h http.Header) []HeaderGroup {
	forwarding := stringSet([]string{
		"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host",
		"X-Forwarded-Port", "X-Real-Ip", "Via", "Forwarded",
	})
	auth := stringSet([]string{
		"Authorization", "Cookie",
		"X-User", "X-User-Email", "X-Authenticated-User",
		"X-Auth-Source", "X-Auth-Token",
		"Cf-Access-Authenticated-User-Email", "Cf-Access-Jwt-Assertion",
		"X-Goog-Authenticated-User-Email", "X-Goog-Iap-Jwt-Assertion",
	})

	fwG := HeaderGroup{Category: "Forwarding", Color: "info"}
	auG := HeaderGroup{Category: "Authentication", Color: "success"}
	stG := HeaderGroup{Category: "Standard", Color: "neutral"}

	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		canonical := http.CanonicalHeaderKey(k)
		for _, v := range h[k] {
			e := HeaderEntry{Name: canonical, Value: v}
			switch {
			case forwarding[canonical]:
				fwG.Entries = append(fwG.Entries, e)
			case auth[canonical]:
				// Bearer/JWT tokens longos: trunca pra visualização
				if canonical == "Authorization" && len(v) > 60 {
					e.Value = v[:30] + "…" + v[len(v)-8:]
				}
				auG.Entries = append(auG.Entries, e)
			default:
				stG.Entries = append(stG.Entries, e)
			}
		}
	}
	fwG.Count = len(fwG.Entries)
	auG.Count = len(auG.Entries)
	stG.Count = len(stG.Entries)
	return []HeaderGroup{fwG, auG, stG}
}

// ─── helpers ───

func firstNonEmptyHeader(r *http.Request, keys ...string) string {
	for _, k := range keys {
		if v := r.Header.Get(k); v != "" {
			return v
		}
	}
	return ""
}

func ipFromAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func stringSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}
