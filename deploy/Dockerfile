# ── 构建阶段 / build stage ──
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 纯 Go 零 CGO 构建全部二进制
RUN CGO_ENABLED=0 go build -o /out/dkd ./dk-build/cmd/dkd/ \
    && CGO_ENABLED=0 go build -o /out/dkctl ./dk-build/cmd/dkctl/ \
    && CGO_ENABLED=0 go build -o /out/dk-ingest ./dk-build/cmd/dk-ingest/ \
    && CGO_ENABLED=0 go build -o /out/dk-incremental ./dk-build/cmd/dk-incremental/ \
    && CGO_ENABLED=0 go build -o /out/dk-rebalance ./dk-build/cmd/dk-rebalance/ \
    && CGO_ENABLED=0 go build -o /out/dk ./dk-service/cmd/dk/ \
    && CGO_ENABLED=0 go build -o /out/mcp-server ./dk-service/cmd/mcp-server/ \
    && CGO_ENABLED=0 go build -o /out/dk-evolve-serve ./dk-build/cmd/dk-evolve-serve/

# ── 运行阶段 / runtime stage ──
FROM alpine:3.19

RUN apk add --no-cache git ca-certificates tzdata \
    && addgroup -S dkd && adduser -S -G dkd dkd

# 二进制
COPY --from=builder /out/dkd /usr/local/bin/dkd
COPY --from=builder /out/dkctl /usr/local/bin/dkctl
COPY --from=builder /out/dk-ingest /usr/local/bin/dk-ingest
COPY --from=builder /out/dk-incremental /usr/local/bin/dk-incremental
COPY --from=builder /out/dk-rebalance /usr/local/bin/dk-rebalance
COPY --from=builder /out/dk /usr/local/bin/dk
COPY --from=builder /out/mcp-server /usr/local/bin/mcp-server
COPY --from=builder /out/dk-evolve-serve /usr/local/bin/dk-evolve-serve

# 持久化目录
RUN mkdir -p /var/lib/dkd/workspaces /var/lib/dkd/cache /var/lib/dkd/bundles \
    && chown -R dkd:dkd /var/lib/dkd

USER dkd
WORKDIR /var/lib/dkd

# 默认 daemon 模式；配置经 volume 挂载
ENV DKD_CONFIG=/etc/dkd/config.yaml
EXPOSE 7810

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:7810/healthz || exit 1

ENTRYPOINT ["dkd"]
CMD ["daemon", "--config", "/etc/dkd/config.yaml"]
