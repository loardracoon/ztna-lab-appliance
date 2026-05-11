# Docker / Podman deployment

Roda o ZTNA Lab Appliance como container. A imagem final é baseada em
`gcr.io/distroless/static-debian12:debug-nonroot` (~20 MB total).

## Pré-requisitos

* Docker 20.10+ **ou** Podman 4+
* Plugin `docker compose` v2 (ou `podman-compose`)

Nada mais. O build acontece dentro de um container `golang:1.22-alpine`
durante o multi-stage do Dockerfile — você não precisa de Go na sua máquina.

## Quick start (Makefile, recomendado)

A partir da raiz do repositório:

```bash
make docker-up         # build + up com profile host
make docker-logs       # tail dos logs
make docker-cli        # abre o REPL CLI dentro do container
make docker-down       # para
```

## Comandos diretos

```bash
# Da raiz do repo:
docker compose -f deployments/docker/docker-compose.yml --profile host build
docker compose -f deployments/docker/docker-compose.yml --profile host up -d
docker compose -f deployments/docker/docker-compose.yml --profile host logs -f
docker compose -f deployments/docker/docker-compose.yml --profile host down
```

Idêntico com `podman compose` (Podman 4.4+) ou `podman-compose`.

## Profiles

| Profile  | network_mode     | Quando usar                                              |
|----------|------------------|----------------------------------------------------------|
| `host`   | host (recomendado) | Uso normal. Latência fiel, sem NAT, UDP/53 funciona limpo |
| `bridge` | bridge + ports   | Quando precisar isolamento. NAT do userland-proxy adiciona ~1-3ms |

## Acessos após `up`

* Admin UI/API : `http://<host>:9000`
* HTTP test    : `http://<host>:80`
* SSH mock     : `ssh -p 2222 admin@<host>` (senha: `admin`)
* DNS          : `dig @<host> ztnatest.sophizo.com.br`
* CLI          : `docker exec -it ztna-appliance /ztna-lab cli`

## Persistência

Volume nomeado `ztna-data` montado em `/data` no container. Guarda:

* `dns_records.json` — registros A e CNAME
* `ssh_host_key`     — chave RSA gerada na primeira execução
* `ztna_lab.log`     — log unificado
* `ztna_lab.history` — histórico do REPL

Para zerar o estado: `docker compose ... down -v` (cuidado: remove o volume).

## Conflito de porta 53 no host

Em distros com `systemd-resolved` ouvindo na 53 (Ubuntu padrão, Fedora):

```bash
# verifica:
sudo ss -tulnp | grep ':53'

# opção 1: desabilitar só o stub local do resolved (não afeta resolução DNS)
sudo mkdir -p /etc/systemd/resolved.conf.d
echo -e "[Resolve]\nDNSStubListener=no" | sudo tee /etc/systemd/resolved.conf.d/no-stub.conf
sudo ln -sf /run/systemd/resolve/resolv.conf /etc/resolv.conf
sudo systemctl restart systemd-resolved

# opção 2: desabilitar autostart do DNS no appliance e subir só quando precisar
# (editar docker-compose.yml: ZTNA_AUTOSTART_DNS: "false")
# depois via API: curl -X POST http://localhost:9000/api/dns/start
```

## Build sem compose

Útil pra CI ou pra publicar a imagem em registry:

```bash
# da raiz do repo:
docker build -f deployments/docker/Dockerfile -t ztna-lab-appliance:1.0 .

# rodar manualmente:
docker run -d --name ztna-appliance \
  --network host \
  --cap-add NET_BIND_SERVICE \
  -v ztna-data:/data \
  ztna-lab-appliance:1.0
```
