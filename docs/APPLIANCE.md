# ZTNA Lab — Modo Appliance (Docker / Podman)

Este pacote adiciona um modo *appliance* ao ZTNA Lab Tools v2.0 sem alterar
a lógica dos servidores DNS, HTTP e SSH existentes.

## Visão geral

```
┌──────────────────────── container ztna-appliance ─────────────────────────┐
│                                                                           │
│   PID 1: ztna-lab daemon                                                  │
│   ┌──────────────────────────────────────────────────────────────────┐   │
│   │  Plano de teste                                                  │   │
│   │   • DNS Server     UDP 53   ← clientes do laboratório            │   │
│   │   • HTTP Inspector TCP 80   ← alvo dos testes ZTNA               │   │
│   │   • SSH Mock       TCP 2222                                      │   │
│   └──────────────────────────────────────────────────────────────────┘   │
│   ┌──────────────────────────────────────────────────────────────────┐   │
│   │  Plano de gerenciamento (NOVO)                                   │   │
│   │   • Admin API      TCP 9000   GET/POST/DELETE em /api/...        │   │
│   │   • Web UI         TCP 9000/                                     │   │
│   └──────────────────────────────────────────────────────────────────┘   │
│                                                                           │
│   docker exec -it ztna-appliance /ztna-lab cli                            │
│     ↳ REPL local que fala com a Admin API em 127.0.0.1:9000              │
│                                                                           │
│   Volume: /data → dns_records.json, ssh_host_key, ztna_lab.log,           │
│                   ztna_lab.history                                        │
└───────────────────────────────────────────────────────────────────────────┘
```

## Estrutura dos arquivos novos

```
ztna-lab/
├── admin/
│   ├── server.go        ← Admin API + handlers
│   ├── latency.go       ← shim para o latency runner (evita import cycle)
│   └── ui.html          ← Web UI single-file (embedada via //go:embed)
├── cmd/
│   ├── main_appliance.go ← novo main com subcomandos daemon/cli
│   └── cli_client.go     ← REPL remoto que fala com a Admin API
├── Dockerfile
├── docker-compose.yml
└── ... (resto do projeto inalterado)
```

> Os arquivos em `cmd/` devem ser mesclados ao `main.go` original. Em particular
> a função `main()` antiga vira `runREPL()` (mantém comportamento exato), e o
> novo `main()` em `cmd/main_appliance.go` faz o roteamento dos subcomandos.

## Pré-requisitos de integração com o código existente

Antes de buildar, o código atual precisa expor algumas APIs que a Admin
consome. A maioria provavelmente já existe — é só conferir as assinaturas:

**Pacote `dns`:**
```go
type Server struct { /* ... */ }
func NewServer(recordsPath, upstream string) *Server
func (s *Server) Start() error
func (s *Server) Stop() error
func (s *Server) IsRunning() bool

func ListRecords() (a map[string]string, cname map[string]string)
func AddA(name, ip string) error
func AddCNAME(alias, target string) error
func Remove(name string) error
```

**Pacote `httpd`:**
```go
type Server struct { /* ... */ }
func NewServer(addr string) *Server
func (s *Server) Start() error
func (s *Server) Stop() error
func (s *Server) IsRunning() bool
```

**Pacote `sshd`:**
```go
type Server struct { /* ... */ }
func NewServer(addr, hostKeyPath string) *Server
func (s *Server) Start() error
func (s *Server) Stop() error
func (s *Server) IsRunning() bool

type Session struct {
    ID          int       `json:"id"`
    User        string    `json:"user"`
    IP          string    `json:"ip"`
    ConnectedAt time.Time `json:"connected_at"`
}
func (s *Server) Sessions() []Session
```

**Pacote `logger`:**
```go
func Tail(n int) ([]string, error)   // já é usado pelo `log tail` da CLI
```

Se algum desses não bater exatamente, ajuste o `admin/server.go` — são
chamadas pontuais e bem localizadas.

## Build e execução

```bash
# Local (sem container)
go build -o ztna-lab .
./ztna-lab daemon &
./ztna-lab cli           # outra janela

# Container (Docker ou Podman)
docker compose --profile host up -d --build
# ou
podman-compose --profile host up -d --build

# UI de admin
open http://localhost:9000

# CLI dentro do container
docker exec -it ztna-appliance /ztna-lab cli
```

## Endpoints da Admin API

| Método | Path | Descrição |
|---|---|---|
| GET    | `/api/health` | Healthcheck (sem auth) |
| GET    | `/api/status` | Status dos 3 servidores + versão |
| POST   | `/api/dns/start` \| `/api/dns/stop` | Controle do DNS |
| GET    | `/api/dns/records` | Lista registros A e CNAME |
| POST   | `/api/dns/records` | Adiciona `{type:"A\|CNAME", name, value}` |
| DELETE | `/api/dns/records/{name}` | Remove registro |
| POST   | `/api/http/start` \| `/api/http/stop` | Controle do HTTP test |
| POST   | `/api/ssh/start` \| `/api/ssh/stop` | Controle do SSH |
| GET    | `/api/ssh/sessions` | Sessões SSH ativas |
| GET    | `/api/log/tail?n=50` | Últimas N linhas do log |
| POST   | `/api/latency` | `{url, count, interval_ms}` → percentis |

## Variáveis de ambiente

| Variável | Default | Descrição |
|---|---|---|
| `ZTNA_ADMIN_ADDR` | `0.0.0.0:9000` | Bind da Admin API |
| `ZTNA_ADMIN_TOKEN` | *(vazio)* | Bearer token; vazio = sem auth |
| `ZTNA_AUTOSTART_DNS` | `true` | Sobe DNS no boot |
| `ZTNA_AUTOSTART_HTTP` | `true` | Sobe HTTP no boot |
| `ZTNA_AUTOSTART_SSH` | `true` | Sobe SSH no boot |
| `ZTNA_LOG_PATH` | `/data/ztna_lab.log` | Caminho do log |
| `ZTNA_DNS_RECORDS` | `/data/dns_records.json` | JSON de registros |
| `ZTNA_SSH_KEY` | `/data/ssh_host_key` | Chave RSA do SSH mock |

## Observações operacionais

**Conflito de UDP/53 no host.** Em distros com `systemd-resolved` ouvindo na
53, o port mapping falha. Ou desabilita o stub local do resolved
(`systemctl stop systemd-resolved` + ajusta `/etc/resolv.conf`) ou usa o
profile `host` do compose, que nesse caso vai dar conflito também — nesse
caso, considere desabilitar o autostart do DNS (`ZTNA_AUTOSTART_DNS=false`)
e subi-lo só quando precisar.

**Capabilities.** O container usa `NET_BIND_SERVICE` para bindar 53/80 sem
rodar como root. A imagem distroless já roda como UID `nonroot`.

**Persistência.** Tudo em `/data`. Removendo o volume você zera os registros
DNS e a chave host do SSH (que será regenerada).

**Auth do plano de admin.** Por default o lab fica aberto. Para qualquer uso
fora de uma rede de laboratório controlada, ative `ZTNA_ADMIN_TOKEN`. A Web
UI guarda o token em `localStorage` no primeiro acesso. Para algo realmente
exposto, ponha um reverse proxy (Traefik, nginx) na frente com TLS — combina
bem com o stack que você já usa no Private Gateway.

**Healthcheck.** Está configurado como `ztna-lab cli --health-check`. Esse
flag ainda não foi adicionado ao `cli_client.go` no código que entreguei —
adicione um early-return em `runCLIClient()`:

```go
if len(os.Args) > 2 && os.Args[2] == "--health-check" {
    if _, err := newAPIClient().call("GET", "/api/health", nil); err != nil {
        os.Exit(1)
    }
    os.Exit(0)
}
```
