# Makefile — entrypoint único pros dois caminhos de deploy.
#
# Bare metal Linux:
#   make build         compila binário linux/amd64 (usa Docker como builder, não precisa de Go local)
#   sudo make install  instala como serviço systemd
#   sudo make uninstall
#
# Docker / Podman:
#   make docker-build
#   make docker-up
#   make docker-down
#   make docker-logs
#
# Genéricos:
#   make test          roda testes Go dentro de container (não precisa de Go local)
#   make clean
#   make help

BINARY      := ztna-lab
VERSION     := 1.0.0
IMAGE       := ztna-lab-appliance:$(VERSION)
GO_IMAGE    := golang:1.22-alpine
ROOT        := $(shell pwd)

.DEFAULT_GOAL := help

# ─────────────────────────── help ────────────────────────────

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ───────────────────────── build (Go) ────────────────────────

.PHONY: build
build: dist/$(BINARY) ## Compila binário linux/amd64 estático (~8 MB)

dist/$(BINARY): $(shell find . -name '*.go' -not -path './dist/*' 2>/dev/null)
	@mkdir -p dist
	docker run --rm \
	  -v $(ROOT):/src -w /src \
	  -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 \
	  $(GO_IMAGE) \
	  go build -ldflags="-s -w" -o dist/$(BINARY) .
	@ls -lh dist/$(BINARY)

.PHONY: test
test: ## Roda go test ./... em container
	docker run --rm -v $(ROOT):/src -w /src $(GO_IMAGE) go test ./...

.PHONY: clean
clean: ## Remove binários gerados
	rm -rf dist/

# ─────────────────────── bare metal Linux ────────────────────

.PHONY: install
install: build ## Instala como service systemd (precisa sudo)
	@if [ "$$(id -u)" -ne 0 ]; then echo "Use: sudo make install"; exit 1; fi
	BINARY_SRC=$(ROOT)/dist/$(BINARY) bash deployments/linux/install.sh

.PHONY: uninstall
uninstall: ## Remove o serviço (precisa sudo; pede confirmação pra apagar /var/lib)
	@if [ "$$(id -u)" -ne 0 ]; then echo "Use: sudo make uninstall"; exit 1; fi
	bash deployments/linux/uninstall.sh

.PHONY: status
status: ## Status do service systemd
	systemctl status ztna-lab --no-pager || true

.PHONY: logs
logs: ## Tail dos logs (journalctl)
	journalctl -u ztna-lab -f -n 100

# ────────────────────────── Docker ───────────────────────────

COMPOSE := docker compose -f deployments/docker/docker-compose.yml
PROFILE := --profile host

.PHONY: docker-build
docker-build: ## Build da imagem Docker
	$(COMPOSE) build

.PHONY: docker-up
docker-up: ## Sobe o appliance via docker compose (network host)
	$(COMPOSE) $(PROFILE) up -d
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
