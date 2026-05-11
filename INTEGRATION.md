# Guia de Integração

Este pacote contém **apenas as adições** necessárias para transformar o
ZTNA Lab Tools v2.0 em um appliance containerizado/instalável. Você
precisa mesclar com seu código-fonte Go existente seguindo os 4 passos
abaixo.

## Passo 1 — Copiar seu código-fonte existente para a raiz

Coloque seus arquivos do projeto v2.0 na raiz deste repositório, ao lado
dos arquivos novos. O resultado deve ficar assim:

```
ztna-lab-appliance/
├── main.go                  ← SEU código original
├── go.mod                   ← SEU
├── go.sum                   ← SEU
├── dns/                     ← SEU pacote (records.go + server.go)
├── httpd/                   ← SEU pacote
├── sshd/                    ← SEU pacote
├── logger/                  ← SEU pacote
│
├── main_appliance.go        ← do pacote (este repo)
├── cli_client.go            ← do pacote
├── admin/                   ← do pacote
├── deployments/             ← do pacote
└── ... (README, Makefile, etc.)
```

## Passo 2 — Renomear `main()` no seu `main.go` original

Você terá dois arquivos `.go` com função `main()`, e o Go não permite isso.
A solução é simples: renomeie a `main()` do seu `main.go` original para
`runREPL()`.

**Antes** (seu `main.go`):
```go
func main() {
    logger.Init("ztna_lab.log")
    // ... inicialização dos servidores ...
    // ... loop do REPL com readline ...
}
```

**Depois**:
```go
func runREPL() {
    // ... inicialização dos servidores ...
    // ... loop do REPL com readline ...
    //
    // REMOVA a chamada a logger.Init() — o main() novo
    // (em main_appliance.go) já faz isso antes de chamar runREPL().
}
```

Se a sua `main()` original tinha um dispatch que olhava `os.Args[1]` pra
decidir entre REPL, latency tool, etc., **remova esse dispatch**. O novo
`main()` em `main_appliance.go` faz esse roteamento agora (subcomandos
`daemon`, `cli`, e fallback para REPL quando TTY é detectado).

## Passo 3 — Conferir as assinaturas que o `admin/server.go` espera

O `admin/server.go` chama funções dos seus pacotes. Estas são as
assinaturas esperadas — se o seu código não bate exatamente, ou você
ajusta no seu código, ou ajusta no `admin/server.go`. O `go build` vai
apontar exatamente o que está errado.

**Pacote `dns`:**
```go
type Server struct { /* ... */ }
func NewServer(recordsPath, upstream string) *Server
func (s *Server) Start() error
func (s *Server) Stop() error
func (s *Server) IsRunning() bool

// funções de pacote (estado global atual do projeto v2.0):
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
func Init(path string)
func Log(module, message string)
func Tail(n int) ([]string, error)   // se ainda não existe, é trivial: ler as últimas N linhas do arquivo
```

A diferença mais comum vai ser que os servidores DNS/HTTP/SSH no v2.0
talvez recebam paths fixos ou nem tenham `NewServer(...)`. Nesse caso é
refatorar: extrair o path como argumento. Pequenas mudanças.

## Passo 4 — Implementar o `runLatencyAPI`

No arquivo `main_appliance.go`, lá no final, tem um stub:

```go
func runLatencyAPI(url string, count int, interval time.Duration) (admin.LatencyResult, error) {
    return admin.LatencyResult{}, fmt.Errorf("not implemented: ...")
}
```

Substitua isso pela chamada à sua função real de latência (a que implementa
`latency run` no REPL atual), convertendo o retorno para
`admin.LatencyResult`. Exemplo se a sua função se chama `runLatency` e
devolve `time.Duration` nos campos:

```go
func runLatencyAPI(url string, count int, interval time.Duration) (admin.LatencyResult, error) {
    r := runLatency(url, count, interval)  // sua função existente
    return admin.LatencyResult{
        URL:     url,
        Total:   r.Total,
        Success: r.Success,
        Min:     r.Min.Milliseconds(),
        Avg:     r.Avg.Milliseconds(),
        P50:     r.P50.Milliseconds(),
        P95:     r.P95.Milliseconds(),
        P99:     r.P99.Milliseconds(),
        Max:     r.Max.Milliseconds(),
        Jitter:  r.Jitter.Milliseconds(),
        Verdict: r.Verdict,
    }, nil
}
```

Se a sua função apenas imprime e não retorna struct, refatore primeiro
para retornar os valores (e separar a impressão num wrapper que o REPL
chama).

## Passo 5 — Buildar

```bash
make build         # docker faz tudo, gera dist/ztna-lab
```

Se der erro de compilação, o Go aponta exatamente arquivo, linha e
expressão problemática. Os erros vão ser todos do tipo "função X não
existe" ou "tipo Y não tem método Z" — todos referentes às assinaturas do
Passo 3.

## Verificação

```bash
# bare metal
sudo make install
sudo systemctl status ztna-lab
curl http://localhost:9000/api/health
# {"status":"ok"}

# ou docker
make docker-up
curl http://localhost:9000/api/health
```

Se a admin API responder `{"status":"ok"}`, a integração foi bem-sucedida.

---

Se travar em qualquer ponto, manda o erro de compilação que você está
vendo. Os erros do Go são bem informativos e geralmente o fix é de uma
linha.
