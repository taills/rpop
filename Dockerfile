ARG DOCKER_REGISTRY_MIRROR=docker.io

# ---- 前端：构建管理控制台，产物随后通过 go:embed 内嵌进二进制 ----
FROM ${DOCKER_REGISTRY_MIRROR}/library/node:22-alpine AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --registry=https://registry.npmmirror.com
COPY web/ ./
RUN npm run build

# ---- 后端：go-sqlite3 依赖 CGO，使用 musl 静态链接，产出可在 alpine 直接运行的单一二进制 ----
FROM ${DOCKER_REGISTRY_MIRROR}/library/golang:1.26-alpine AS builder
RUN apk add --no-cache build-base
ENV CGO_ENABLED=1 GOOS=linux GOPROXY=https://goproxy.cn,direct GOPRIVATE=gitlab.towere.cc
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web/embed.go ./web/embed.go
COPY --from=frontend /src/web/dist ./web/dist
ARG VERSION=dev
RUN go build -trimpath \
    -tags "netgo osusergo sqlite_omit_load_extension" \
    -ldflags "-s -w -X main.Version=${VERSION} -linkmode external -extldflags '-static'" \
    -o /out/rpop ./cmd/rpop

FROM ${DOCKER_REGISTRY_MIRROR}/library/alpine:latest
LABEL authors="Towere TaiYi Lab"
RUN apk --no-cache add ca-certificates tzdata && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone
WORKDIR /app
COPY --from=builder /out/rpop /app/rpop
# SQLite 配置库与应用/访问日志需挂卷持久化
VOLUME ["/app/data", "/app/logs"]
# RPOP_ADDR 为管理控制台监听地址（host 网络下可改为 127.0.0.1:<端口>）；健康检查跟随该地址。
# 各站点的监听端口在控制台中配置，运行时需另行映射（或使用 host 网络）
ENV RPOP_ADDR=0.0.0.0:8080 \
    RPOP_DB=/app/data/rpop.db \
    RPOP_LOG_DIR=/app/logs
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=10s --start-period=20s --retries=3 \
    CMD ["/app/rpop", "-health-check"]
ENTRYPOINT ["/app/rpop"]
