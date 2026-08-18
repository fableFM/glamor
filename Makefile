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

## --- Управление сервисами (демон + web dev-сервер) ---

PORT ?= 7380
WEB_PORT ?= 5173
RUN_DIR := .run

.PHONY: start stop status

## start: собрать и запустить glamord (:PORT) и web dev-сервер (:WEB_PORT)
start: build
	@mkdir -p $(RUN_DIR)
	@if [ -f $(RUN_DIR)/glamord.pid ] && kill -0 "$$(cat $(RUN_DIR)/glamord.pid)" 2>/dev/null; then \
		echo "glamord уже запущен (pid $$(cat $(RUN_DIR)/glamord.pid))"; \
	else \
		( set -m; \
		  GLAMOR_HTTP_PORT=$(PORT) nohup ./bin/glamord > $(RUN_DIR)/glamord.out 2>&1 & \
		  echo $$! > $(RUN_DIR)/glamord.pid ); \
		echo "glamord запущен на http://127.0.0.1:$(PORT) (лог: ~/.glamor/glamord.log)"; \
	fi
	@sleep 1
	@if [ -f $(RUN_DIR)/web.pid ] && kill -0 "$$(cat $(RUN_DIR)/web.pid)" 2>/dev/null; then \
		echo "web уже запущен (pid $$(cat $(RUN_DIR)/web.pid))"; \
	else \
		( set -m; \
		  VITE_GLAMOR_API="http://127.0.0.1:$(PORT)" \
		  VITE_GLAMOR_TOKEN="$$(cat ~/.glamor/token 2>/dev/null)" \
		  nohup npm run dev --prefix web -- --port $(WEB_PORT) --strictPort > $(RUN_DIR)/web.log 2>&1 & \
		  echo $$! > $(RUN_DIR)/web.pid ); \
		echo "web запущен на http://127.0.0.1:$(WEB_PORT) (лог: $(RUN_DIR)/web.log)"; \
	fi

## stop: остановить glamord и web dev-сервер (вместе с дочерними процессами)
# Сервисы стартуют через `( set -m; nohup ... & )` — каждый становится
# лидером своей process group (pgid == pid), поэтому `kill -- -$PGID`
# сносит всё дерево (npm → sh → node/vite). Без этого дочерний vite
# переживал stop и держал порт $(WEB_PORT). setsid на macOS отсутствует,
# set -m — рабочая замена. Защита: групповой kill только когда процесс
# сам лидер группы (pgid == pid) — иначе группа может быть чужой.
stop:
	@for svc in glamord web; do \
		if [ -f $(RUN_DIR)/$$svc.pid ]; then \
			pid=$$(cat $(RUN_DIR)/$$svc.pid); \
			if kill -0 "$$pid" 2>/dev/null; then \
				pgid=$$(ps -o pgid= -p "$$pid" 2>/dev/null | tr -d ' '); \
				if [ -n "$$pgid" ] && [ "$$pgid" = "$$pid" ]; then \
					kill -- "-$$pgid" 2>/dev/null; \
				else \
					kill "$$pid" 2>/dev/null; \
				fi; \
				sleep 1; \
				if kill -0 "$$pid" 2>/dev/null; then \
					echo "$$svc не завершился по SIGTERM (pid $$pid), шлю SIGKILL"; \
					if [ -n "$$pgid" ] && [ "$$pgid" = "$$pid" ]; then \
						kill -9 -- "-$$pgid" 2>/dev/null; \
					else \
						kill -9 "$$pid" 2>/dev/null; \
					fi; \
				else \
					echo "$$svc остановлен (pid $$pid)"; \
				fi; \
			else \
				echo "$$svc не запущен"; \
			fi; \
			rm -f $(RUN_DIR)/$$svc.pid; \
		else \
			echo "$$svc не запущен (нет pid-файла)"; \
		fi; \
	done

## status: состояние сервисов + последние логи
status:
	@echo "=== glamord ==="
	@if [ -f $(RUN_DIR)/glamord.pid ] && kill -0 "$$(cat $(RUN_DIR)/glamord.pid)" 2>/dev/null; then \
		echo "running (pid $$(cat $(RUN_DIR)/glamord.pid))"; \
		./bin/glamor status 2>/dev/null || true; \
	else \
		echo "stopped"; \
	fi
	@echo "--- последние строки ~/.glamor/glamord.log ---"
	@tail -n 20 ~/.glamor/glamord.log 2>/dev/null || echo "(лога нет)"
	@echo ""
	@echo "=== web ==="
	@if [ -f $(RUN_DIR)/web.pid ] && kill -0 "$$(cat $(RUN_DIR)/web.pid)" 2>/dev/null; then \
		echo "running (pid $$(cat $(RUN_DIR)/web.pid))"; \
	else \
		echo "stopped"; \
	fi
	@echo "--- последние строки $(RUN_DIR)/web.log ---"
	@tail -n 20 $(RUN_DIR)/web.log 2>/dev/null || echo "(лога нет)"

## --- Tauri shell (T-18) ---

.PHONY: tauri-sidecar tauri-dev tauri-build

## tauri-sidecar: собрать glamord и положить в src-tauri/binaries с суффиксом target-triple
tauri-sidecar: build
	@triple=$$(rustc -vV 2>/dev/null | sed -n 's/host: //p'); \
	if [ -z "$$triple" ]; then echo "ошибка: нет Rust toolchain (brew install rustup && rustup-init)"; exit 1; fi; \
	mkdir -p src-tauri/binaries; \
	cp bin/glamord "src-tauri/binaries/glamord-$$triple"; \
	echo "sidecar: src-tauri/binaries/glamord-$$triple"

## tauri-dev: десктоп-окно + демон + Vite (нужен Rust toolchain)
tauri-dev: tauri-sidecar
	npx --prefix web tauri dev

## tauri-build: релизная .app со sidecar-демоном
tauri-build: tauri-sidecar
	npx --prefix web tauri build
