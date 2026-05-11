# Bare metal Linux deployment

Instala o ZTNA Lab Appliance diretamente em qualquer Linux com **systemd**
(Ubuntu/Debian/RHEL/Rocky/CentOS/Alma/Fedora/SUSE/Arch — todos servem).

## Pré-requisitos

* Linux com systemd e `systemctl`
* Acesso `sudo`
* Docker instalado **só na máquina onde você vai compilar** (usado como
  builder do binário Go). Pode ser uma máquina diferente do host final —
  basta copiar o binário gerado.

## Instalação rápida

A partir da raiz do repositório:

```bash
make build         # gera dist/ztna-lab (~8 MB, estático)
sudo make install  # instala e inicia o serviço
```

A imagem `golang:1.22-alpine` é baixada na primeira vez (~500 MB) e fica em
cache. Os builds seguintes levam ~10 segundos.

## O que o `install.sh` faz

1. Verifica conflitos de porta nas 53, 80, 2222 e 9000 e avisa
2. Cria usuário de sistema `ztna-lab` (sem shell)
3. Cria `/var/lib/ztna-lab`, `/var/log/ztna-lab`, `/etc/ztna-lab`
4. Copia o binário pra `/usr/local/bin/ztna-lab`
5. Instala a unit em `/etc/systemd/system/ztna-lab.service`
6. Copia `config.env.example` para `/etc/ztna-lab/config.env` se não existir
7. `daemon-reload`, `enable`, `start` e checa se ficou ativo

## Layout pós-instalação

| Caminho | Conteúdo |
|---|---|
| `/usr/local/bin/ztna-lab` | binário |
| `/etc/systemd/system/ztna-lab.service` | unit do systemd |
| `/etc/ztna-lab/config.env` | variáveis de ambiente |
| `/var/lib/ztna-lab/` | `dns_records.json`, `ssh_host_key` |
| `/var/log/ztna-lab/` | `ztna_lab.log` |

## Operação

```bash
sudo systemctl status ztna-lab
sudo systemctl restart ztna-lab
sudo journalctl -u ztna-lab -f          # logs em tempo real
ztna-lab cli                            # REPL remoto (fala com daemon local)
curl http://localhost:9000/api/health
```

## Editar configuração

```bash
sudo $EDITOR /etc/ztna-lab/config.env
sudo systemctl restart ztna-lab
```

Variáveis disponíveis: `ZTNA_ADMIN_ADDR`, `ZTNA_ADMIN_TOKEN`,
`ZTNA_AUTOSTART_{DNS,HTTP,SSH}`, paths customizados. Ver
`config.env.example`.

## Hardening do systemd

A unit já vem com sandbox razoável:

* `User=ztna-lab` (não-root)
* `AmbientCapabilities=CAP_NET_BIND_SERVICE` (única capability para 53/80)
* `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`,
  `PrivateDevices`, `ProtectKernelTunables`, `ProtectKernelModules`,
  `RestrictNamespaces`, `LockPersonality`, `RestrictRealtime`
* `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX` (sem AF_PACKET, sem
  netlink raw)
* `ReadWritePaths` restrito a `/var/lib/ztna-lab` e `/var/log/ztna-lab`

Se rodar `systemd-analyze security ztna-lab` o score deve ser razoável
(< 3.5). Suficiente para um appliance de lab.

## Conflito de porta 53

Em distros que rodam `systemd-resolved` por padrão (Ubuntu desde 18.04,
Fedora recente), a 53 fica ocupada pelo stub local. Você tem três opções:

**a)** Desabilitar o stub do resolved (não afeta resolução DNS do host):
```bash
sudo mkdir -p /etc/systemd/resolved.conf.d
echo -e "[Resolve]\nDNSStubListener=no" | sudo tee /etc/systemd/resolved.conf.d/no-stub.conf
sudo ln -sf /run/systemd/resolve/resolv.conf /etc/resolv.conf
sudo systemctl restart systemd-resolved
```

**b)** Parar `systemd-resolved` de vez:
```bash
sudo systemctl disable --now systemd-resolved
```

**c)** Deixar o DNS do appliance desligado por padrão e subir só quando
precisar — no `config.env`:
```
ZTNA_AUTOSTART_DNS=false
```
e via API: `curl -X POST http://localhost:9000/api/dns/start`

## Desinstalar

```bash
sudo make uninstall
```

O script pede confirmação antes de apagar dados, logs, config e usuário —
então em downgrades/upgrades você pode preservar o estado.
