# syntax=docker/dockerfile:1.7
#
# Dockerfile do ZTNA Lab Appliance.
# Build context é a raiz do repositório — veja docker-compose.yml.
#
# Para buildar diretamente (sem compose):
#   docker build -f deployments/docker/Dockerfile -t ztna-lab-appliance:1.0 .

# ---- estágio de build ----
FROM golang:1.22-alpine AS build

WORKDIR /src

# Cache de dependências (camada separada)
COPY go.mod go.sum ./
RUN go mod download

# Código-fonte completo (raiz do repo)
COPY . .

# Binário estático Linux/amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /out/ztna-lab .

# Sanity check
RUN ls -lh /out/ztna-lab

# ---- estágio runtime ----
# distroless/static-debian12 com tag :debug-nonroot inclui busybox para
# permitir `docker exec` na CLI. A versão sem :debug não tem shell, mas
# também não permite exec interativo.
FROM gcr.io/distroless/static-debian12:debug-nonroot

WORKDIR /data

ENV ZTNA_LOG_PATH=/data/ztna_lab.log \
    ZTNA_DNS_RECORDS=/data/dns_records.json \
    ZTNA_SSH_KEY=/data/ssh_host_key \
    ZTNA_HISTORY_FILE=/data/ztna_lab.history \
    ZTNA_ADMIN_ADDR=0.0.0.0:9000

COPY --from=build /out/ztna-lab /ztna-lab

# Plano de teste + plano de admin
EXPOSE 80/tcp 2222/tcp 53/udp 9000/tcp

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/ztna-lab", "cli", "--health-check"]

ENTRYPOINT ["/ztna-lab"]
CMD ["daemon"]
