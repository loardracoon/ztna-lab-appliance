# Alpine Linux deployment (OpenRC)

Instala o ZTNA Lab Appliance nativamente em Alpine, sem container — como serviço OpenRC. Útil pra appliance enxuto: imagem base Alpine + binário Go estático fica em torno de **40 MB total** em disco.

## Pré-requisitos

* Alpine 3.18+ (anteriores podem não ter Go 1.22 nos repositórios)
* `apk`, `rc-update`, `rc-service` (vêm de base)
* Acesso root (Alpine padrão já te loga como root)

## Quick install via setup.sh

```sh
./setup.sh
# escolha "2) Bare metal"
```

O script detecta Alpine + OpenRC automaticamente e roteia pro `deployments/alpine/install.sh`.

## Quick install manual

```sh
# 1. Instala Docker pra buildar o binário (~250 MB temporário; pode remover depois)
apk add docker make
rc-update add docker default
service docker start

# 2. Build
make build

# 3. Instala como serviço OpenRC
make install-alpine
```

## O que o `install.sh` faz

1. Verifica conflitos nas portas 53, 80, 2222, 9000
2. Instala `libcap` (pro `setcap`) se faltar
3. Cria usuário sistema `ztna-lab` (sem shell, sem home)
4. Cria `/var/lib/ztna-lab`, `/var/log/ztna-lab`, `/etc/ztna-lab`
5. Copia o binário pra `/usr/local/bin/ztna-lab` e aplica `cap_net_bind_service=+ep`
6. Instala `/etc/init.d/ztna-lab` (OpenRC initscript)
7. Copia `config.env.example` pra `/etc/ztna-lab/config.env`
8. `rc-update add ztna-lab default` + `rc-service ztna-lab start`

## Layout pós-instalação

| Caminho | Conteúdo |
|---|---|
| `/usr/local/bin/ztna-lab` | binário com `cap_net_bind_service` |
| `/etc/init.d/ztna-lab` | initscript OpenRC |
| `/etc/ztna-lab/config.env` | variáveis de ambiente |
| `/var/lib/ztna-lab/` | `dns_records.json`, `ssh_host_key` |
| `/var/log/ztna-lab/` | `ztna_lab.log` + `stdout.log` + `stderr.log` |

## Operação

```sh
rc-service ztna-lab status
rc-service ztna-lab restart
rc-service ztna-lab reload     # SIGHUP, sem restart
tail -f /var/log/ztna-lab/stdout.log
/usr/local/bin/ztna-lab cli    # REPL remoto via admin API
curl http://localhost:9000/api/health
```

## Editar configuração

```sh
vi /etc/ztna-lab/config.env
rc-service ztna-lab restart
```

## Conflito de porta 53

Alpine Virt padrão **não tem resolver local**, então a 53 fica livre na maioria dos casos. Confirma:

```sh
ss -tulnp | grep ':53'
# ou
netstat -tulnp | grep ':53'
```

Se algo aparecer, opção mais simples é desligar o autostart do DNS no appliance:

```sh
echo 'ZTNA_AUTOSTART_DNS=false' >> /etc/ztna-lab/config.env
rc-service ztna-lab restart
```

E sobe o DNS sob demanda via Admin API: `curl -X POST http://localhost:9000/api/dns/start`.

## Remover Docker depois do build

Se o appliance está estabilizado e você quer deixar o sistema mais enxuto:

```sh
service docker stop
rc-update del docker default
apk del docker
```

O serviço `ztna-lab` continua rodando — Docker era só ferramenta de build.

## Desinstalar

```sh
sudo make uninstall-alpine
```

Pede confirmação antes de apagar dados, logs, config e usuário — útil em downgrades onde você quer preservar o estado.
