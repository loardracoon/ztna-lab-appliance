# Makefile — entrypoint único pros dois caminhos de deploy.
#
# Bare metal Linux (systemd):
#   make build              compila binário linux/amd64
#   sudo make install       instala como serviço systemd
#   sudo make uninstall
#
# Bare metal Alpine (OpenRC):
#   sudo make install-alpine
#   sudo make uninstall-alpine
#
# Docker / Podman:
#   make docker-build
#   make docker-up
#   make docker-redeploy    rebuild + recreate (use após atualizar o código)
#   make docker-down
#   make docker-logs
#
# Genéricos:
#   make test               roda testes Go em container
#   make clean              remove dist/
#   make help

BINARY      := ztna-lab
VERSION     := 1.5.0
GO_IMAGE    := golang:1.22-alpine
ROOT        := $(shell pwd)

.DEFAULT_GOAL := help

# ─────────────────────────── help ────────────────────────────

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# ───────────────────────── build (Go) ────────────────────────

.PHONY: build
build: dist/$(BINARY) ## Compila binário linux/amd64 estático (~9 MB)

# Importante: `go mod tidy` antes do build torna o processo self-healing —
# regenera go.sum se estiver faltando, corrompido ou desatualizado.
dist/$(BINARY): $(shell find . -name '*.go' -not -path './dist/*' 2>/dev/null) go.mod
	@mkdir -p dist
	docker run --rm \
	  -v $(ROOT):/src -w /src \
	  -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 \
	  $(GO_IMAGE) \
	  sh -c "go mod tidy && go build -ldflags='-s -w' -o dist/$(BINARY) ."
	@ls -lh dist/$(BINARY)

.PHONY: test
test: ## Roda go test ./... em container
	docker run --rm -v $(ROOT):/src -w /src $(GO_IMAGE) \
	  sh -c "go mod tidy && go test ./..."

.PHONY: clean
clean: ## Remove binários gerados
	rm -rf dist/

# ─────────────────── bare metal — systemd ────────────────────

.PHONY: install
install: build ## Instala como service systemd (Debian/Ubuntu/RHEL/Fedora/etc.)
	@if [ "$$(id -u)" -ne 0 ]; then echo "Use: sudo make install"; exit 1; fi
	BINARY_SRC=$(ROOT)/dist/$(BINARY) bash deployments/linux/install.sh

.PHONY: uninstall
uninstall: ## Remove o serviço systemd (precisa sudo)
	@if [ "$$(id -u)" -ne 0 ]; then echo "Use: sudo make uninstall"; exit 1; fi
	bash deployments/linux/uninstall.sh

.PHONY: status
status: ## Status do serviço (systemd ou openrc)
	@if command -v systemctl >/dev/null 2>&1; then \
	   systemctl status ztna-lab --no-pager || true; \
	 elif command -v rc-service >/dev/null 2>&1; then \
	   rc-service ztna-lab status || true; \
	 fi

.PHONY: logs
logs: ## Tail dos logs (systemd → journalctl, openrc → /var/log/ztna-lab/)
	@if command -v journalctl >/dev/null 2>&1; then \
	   journalctl -u ztna-lab -f -n 100; \
	 else \
	   tail -f /var/log/ztna-lab/stdout.log /var/log/ztna-lab/stderr.log; \
	 fi

# ──────────────────── bare metal — Alpine ────────────────────

.PHONY: install-alpine
install-alpine: build ## Instala como service OpenRC (Alpine)
	@if [ "$$(id -u)" -ne 0 ]; then echo "Use: sudo make install-alpine"; exit 1; fi
	BINARY_SRC=$(ROOT)/dist/$(BINARY) sh deployments/alpine/install.sh

.PHONY: uninstall-alpine
uninstall-alpine: ## Remove o serviço OpenRC (precisa sudo)
	@if [ "$$(id -u)" -ne 0 ]; then echo "Use: sudo make uninstall-alpine"; exit 1; fi
	sh deployments/alpine/uninstall.sh

# ────────────────────────── Docker ───────────────────────────

COMPOSE := docker compose -f deployments/docker/docker-compose.yml
PROFILE := --profile host

# Both services live behind a profile, so every compose command needs
# $(PROFILE) — without it compose selects no service and silently does nothing.

.PHONY: docker-build
docker-build: ## Build da imagem Docker a partir do código atual
	$(COMPOSE) $(PROFILE) build --pull

.PHONY: docker-up
docker-up: ## Sobe o appliance via docker compose (network host)
	$(COMPOSE) $(PROFILE) up -d --build
	@$(COMPOSE) $(PROFILE) ps

# Re-deploy: compose only builds when the image tag is missing, so a plain
# `up -d` happily reuses a stale image and keeps running the old binary.
# Rebuilding and forcing recreation is what makes a re-deploy actually pick
# up the code in the working tree.
.PHONY: docker-redeploy
docker-redeploy: ## Rebuild a imagem do código atual e recria o container
	$(COMPOSE) $(PROFILE) build --pull
	$(COMPOSE) $(PROFILE) up -d --force-recreate
	@$(COMPOSE) $(PROFILE) ps

.PHONY: docker-down
docker-down: ## Para o appliance
	$(COMPOSE) $(PROFILE) down

.PHONY: docker-logs
docker-logs: ## Tail dos logs do container
	$(COMPOSE) $(PROFILE) logs -f

.PHONY: docker-cli
docker-cli: ## Abre o REPL CLI dentro do container
	docker exec -it ztna-appliance /ztna-lab cli

.PHONY: docker-clean
docker-clean: ## Remove imagem e volume
	$(COMPOSE) $(PROFILE) down -v --rmi local
