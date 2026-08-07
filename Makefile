# ServerCLI 集成构建入口（并行组件集成后使用）
SHELL := /bin/bash
GO := go
NPM := npm

.PHONY: all build backend frontend test smoke test-primary test-child clean help

all: build

## 构建后端两个二进制到 bin/
backend:
	cd backend && $(GO) build -o ../bin/servercli-control-plane ./cmd/control-plane
	cd backend && $(GO) build -o ../bin/servercli-node-agent ./cmd/node-agent

## 构建前端静态资源
frontend:
	cd frontend && $(NPM) run build

build: backend frontend

## 后端单元/集成测试
test:
	cd backend && $(GO) test ./... -count=1

## 端到端冒烟测试（测试主+子）
smoke:
	./scripts/smoke-test.sh

## 启动测试主实例
test-primary:
	./scripts/start.sh --env test --role primary

## 启动测试子实例
test-child:
	./scripts/start.sh --env test --role child --instance test-child-1

help:
	@grep -E '^[a-zA-Z_-]+:' Makefile | sed 's/:.*//' | sort -u
