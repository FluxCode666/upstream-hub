# 一体化镜像：前端 build → 嵌入 Go 二进制 → Debian 运行时
#
# 构建 context 必须是 repo 根目录：
#   docker build -t upstream-hub:dev .
#   或在 docker-compose 里写 context: .
# cf-solver 包较大，单独放在 GitHub Release，构建时下载并校验 SHA-256。

# ---------- Stage 1: 前端 ----------
FROM node:20-alpine AS frontend-builder
WORKDIR /web

RUN corepack enable && corepack prepare pnpm@10.4.0 --activate

COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml ./
RUN pnpm install --no-frozen-lockfile

COPY frontend/ ./
RUN pnpm build

# ---------- Stage 2: 后端 ----------
FROM golang:1.23-bookworm AS go-builder
WORKDIR /src

COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
RUN rm -rf ./web/dist
COPY --from=frontend-builder /web/dist ./web/dist

RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/upstream-hub \
        ./cmd/server

# ---------- Stage 3: 运行时 ----------
# 只在 TARGETARCH=amd64 时下载 x64 便携包；arm64 保留面板及云打码，但不携带不兼容的二进制。
FROM debian:12-slim
ARG TARGETARCH
ARG CFSOLVER_URL=https://github.com/FluxCode666/upstream-hub/releases/download/cf-solver-v1.4.0-rc.2/cf-solver-1.4.0-rc.2-linux-x64.tar.gz
ARG CFSOLVER_SHA256=5439e0c47da9d0bbb16905192311ee841041ff52ac966e96789bccdce52cd1ea
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates curl tzdata wget libstdc++6 libgcc-s1 libc6 \
        fonts-noto-cjk && \
    rm -rf /var/lib/apt/lists/* && \
    useradd -u 10001 -M -s /usr/sbin/nologin upstream && \
    mkdir -p /opt/cf-solver-template && \
    if [ "$TARGETARCH" = "amd64" ]; then \
        curl --fail --location --retry 3 --output /tmp/cf-solver.tar.gz "$CFSOLVER_URL" && \
        echo "$CFSOLVER_SHA256  /tmp/cf-solver.tar.gz" | sha256sum --check --strict && \
        tar -xzf /tmp/cf-solver.tar.gz -C /opt/cf-solver-template --strip-components=1 && \
        chmod -R a+rX,u+w /opt/cf-solver-template && \
        rm /tmp/cf-solver.tar.gz; \
    fi
WORKDIR /app
COPY --from=go-builder /out/upstream-hub /app/upstream-hub
ENV UPSTREAMHUB_CFSOLVER_ROOT=/app/cf-solver
RUN mkdir -p /app/data && chown -R upstream:upstream /app
USER upstream
VOLUME ["/app/data"]
EXPOSE 8418
# 首次启动复制为 uid 10001 可写的运行目录；arm64 空目录会跳过。
CMD ["sh", "-c", "set -e; if [ -f /opt/cf-solver-template/run.sh ] && [ ! -x /app/cf-solver/run.sh ]; then cp -a /opt/cf-solver-template /app/cf-solver; fi; exec /app/upstream-hub"]
