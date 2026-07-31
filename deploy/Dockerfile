# ── 构建阶段 / build stage ──
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 纯 Go 零 CGO 构建全部二进制
RUN CGO_ENABLED=0 go build -o /out/cairnd ./build/cmd/cairnd/ \
    && CGO_ENABLED=0 go build -o /out/cairnctl ./build/cmd/cairnctl/ \
    && CGO_ENABLED=0 go build -o /out/cairn-ingest ./build/cmd/cairn-ingest/ \
    && CGO_ENABLED=0 go build -o /out/cairn-incremental ./build/cmd/cairn-incremental/ \
    && CGO_ENABLED=0 go build -o /out/cairn-rebalance ./build/cmd/cairn-rebalance/ \
    && CGO_ENABLED=0 go build -o /out/cairn ./service/cmd/cairn/ \
    && CGO_ENABLED=0 go build -o /out/cairn-mcp ./service/cmd/cairn-mcp/ \
    && CGO_ENABLED=0 go build -o /out/cairn-evolve ./build/cmd/cairn-evolve/

# ── 运行阶段 / runtime stage ──
FROM alpine:3.19

RUN apk add --no-cache git ca-certificates tzdata \
    && addgroup -S cairnd && adduser -S -G cairnd cairnd

# 二进制
COPY --from=builder /out/cairnd /usr/local/bin/cairnd
COPY --from=builder /out/cairnctl /usr/local/bin/cairnctl
COPY --from=builder /out/cairn-ingest /usr/local/bin/cairn-ingest
COPY --from=builder /out/cairn-incremental /usr/local/bin/cairn-incremental
COPY --from=builder /out/cairn-rebalance /usr/local/bin/cairn-rebalance
COPY --from=builder /out/cairn /usr/local/bin/cairn
COPY --from=builder /out/cairn-mcp /usr/local/bin/cairn-mcp
COPY --from=builder /out/cairn-evolve /usr/local/bin/cairn-evolve

# 持久化目录
RUN mkdir -p /var/lib/cairnd/workspaces /var/lib/cairnd/cache /var/lib/cairnd/bundles \
    && chown -R cairnd:cairnd /var/lib/cairnd

USER cairnd
WORKDIR /var/lib/cairnd

# 默认 daemon 模式；配置经 volume 挂载
ENV CAIRND_CONFIG=/etc/cairnd/config.yaml
EXPOSE 7810

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:7810/healthz || exit 1

ENTRYPOINT ["cairnd"]
CMD ["daemon", "--config", "/etc/cairnd/config.yaml"]
