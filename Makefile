# glamor — монорепо: backend/ (Go-демон), web/ (React UI), src-tauri/ (shell).
# Контракт кодогенерации: сгенерённое коммитим, `make gen` идемпотентен.

GO ?= go
GOLANGCI_LINT ?= golangci-lint
BACKEND := backend

.PHONY: gen gen-go gen-ts build test lint dev web-build ci

## gen: кодогенерация из api/openapi.yaml (Go-модели + TS-типы)
gen: gen-go gen-ts

gen-go:
	cd $(BACKEND) && $(GO) tool oapi-codegen -config ../api/oapi-codegen.yaml ../api/openapi.yaml

gen-ts:
	npm run gen:api --prefix web

## build: сборка демона и CLI
build:
	cd $(BACKEND) && $(GO) build -mod=vendor -o ../bin/glamord ./cmd/glamord
	cd $(BACKEND) && $(GO) build -mod=vendor -o ../bin/glamor ./cmd/glamor

## test: все Go-тесты (e2e — под build tag e2e, по умолчанию выключены)
test:
	cd $(BACKEND) && $(GO) test -mod=vendor -race -count=1 ./...

## lint: golangci-lint (конфиг backend/.golangci.yml)
lint:
	cd $(BACKEND) && $(GOLANGCI_LINT) run --build-tags e2e ./...

## dev: запуск демона из исходников
dev:
	cd $(BACKEND) && $(GO) run -mod=vendor ./cmd/glamord

## web-build: сборка фронта
web-build:
	npm run build --prefix web

## ci: полный локальный прогон (зеркало .github/workflows/ci.yml)
ci: build test lint web-build
