# =============================================================================
# ServerCLI 多阶段镜像
#   Stage 1: 构建前端 (React + Vite -> frontend/dist)
#   Stage 2: 交叉编译 Go 后端 (control-plane + node-agent, 纯 Go 无 CGO)
#   Stage 3: 精简运行时 (debian bookworm-slim)
# 构建参数:
#   VERSION  - 版本号(写入 /version 接口, 默认 0.1.0)
#   BUILD    - 构建来源(默认 dev)
#   COMMIT   - git commit(默认 unknown)
# 运行时通过环境变量配置, 见 .env.example / doc/11_IMPLEMENTATION_CONTRACT.md
# =============================================================================

# ---- Stage 1: Frontend ------------------------------------------------------
FROM node:22-alpine AS frontend-builder

WORKDIR /build/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# ---- Stage 2: Backend -------------------------------------------------------
FROM golang:1.26-alpine AS backend-builder

ARG VERSION=0.1.0
ARG BUILD=dev
ARG COMMIT=unknown
ARG TARGETOS
ARG TARGETARCH

ENV GO111MODULE=on \
    CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64}

WORKDIR /build
COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.build=${BUILD} -X main.commit=${COMMIT}" \
        -o /out/servercli-control-plane ./cmd/control-plane \
    && go build -trimpath -ldflags "-s -w" -o /out/servercli-node-agent ./cmd/node-agent

# ---- Stage 3: Runtime -------------------------------------------------------
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && update-ca-certificates

COPY --from=backend-builder /out/servercli-control-plane /app/bin/servercli-control-plane
COPY --from=backend-builder /out/servercli-node-agent   /app/bin/servercli-node-agent
COPY --from=frontend-builder /build/frontend/dist       /app/frontend/dist
COPY commands/                                          /app/commands
COPY deploy/docker/docker-entrypoint.sh                 /usr/local/bin/docker-entrypoint.sh

RUN chmod +x /usr/local/bin/docker-entrypoint.sh /app/bin/servercli-control-plane /app/bin/servercli-node-agent \
    && mkdir -p /app/state /app/logs /app/run

# 默认主控制面: 前端 9044 / 后端 API 9045 (node-agent 无监听端口, 主动外连主节点)
EXPOSE 9044 9045

ENV APP_ENV=production \
    NODE_ROLE=primary \
    INSTANCE_NAME=production-primary \
    FRONTEND_DIST_DIR=/app/frontend/dist \
    COMMANDS_DIR=/app/commands \
    AGENT_STATE_DIR=/app/state \
    LOG_DIR=/app/logs

WORKDIR /app
ENTRYPOINT ["docker-entrypoint.sh"]
