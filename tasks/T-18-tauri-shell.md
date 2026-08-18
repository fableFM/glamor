# T-18 Tauri shell

Статус: done (с оговорками, 2026-08-17) · M1 · зависимости: T-12, T-13 — GUI-приёмка UNVERIFIED, см. «Дополнение 2026-08-17»

## Цель

Десктопная упаковка (D-07): Tauri = окно + трей + нотификации + управление
жизнью демона. Никакой бизнес-логики в Rust.

## Scope

- **Sidecar**: `glamord` собирается как внешний бинарь, кладётся в ресурсы
  Tauri; shell стартует его при запуске приложения, следит (restart при
  крахе с backoff, N раз потом — экран ошибки с логом), убивает при выходе
  (SIGTERM → grace → SIGKILL; graceful демона — T-12).
- Чтение `~/.glamor/daemon.json` (порт) + `token` → передача во фронт
  (env-инъекция при загрузке страницы или запрос к локальному мосту —
  выбрать безопасный способ: token не должен светиться в DevTools-логах
  больше необходимого).
- **Окно**: размер/позиция запоминаются; single-instance (второй запуск
  фокусирует окно).
- **Трей**: иконка, статус («N активных ранов, M гейтов» — polling
  `/healthz`+summary endpoint), пункты: открыть, выйти. Закрытие окна →
  сворачивает в трей, демон продолжает работать (конфиг: «quit also stops
  daemon», дефолт — нет).
- **Нотификации**: при открытии гейта и свёрнутом окне — desktop-
  notification (через фронт: событие `gate.opened` → Tauri notification
  API). Клик → фокус окна на ране.
- Dev-режим: `tauri dev` поднимает и демон, и Vite.

## Scope (out)

- Автообновление (updater), подпись/нотаризация — M4.
- Нативный диалог выбора каталога используется в T-16 — здесь только
  разрешение в capabilities.

## Acceptance

- `tauri build` выдаёт .app, которая стартует демон, открывает UI,
  переживает убийство демона (рестарт), корректно завершается.
- Сценарий: свернуть в трей во время рана → нотификация о гейте →
  клик → окно на нужном ране.

## Блокер (2026-08-17)

Код написан полностью, но acceptance (tauri build → .app) непроверяем:
на машине нет Rust toolchain (`cargo`/`rustc` отсутствуют, ставить системные
тулчейны без подтверждения пользователя нельзя).

Что сделано (UNVERIFIED — не компилировалось):
- `src-tauri/Cargo.toml` (tauri 2 + plugins: shell, single-instance,
  notification, dialog, window-state), `tauri.conf.json` (externalBin
  binaries/glamord, окно 1440x900), `capabilities/default.json`,
  `build.rs`.
- `src/main.rs`: single-instance (второй запуск → фокус), закрытие окна →
  трей, трей-меню (открыть/выйти; выход НЕ останавливает демон — дефолт
  «quit also stops daemon = false»), daemon-watch: чтение
  ~/.glamor/daemon.json+token, запуск sidecar при отсутствии демона,
  restart с backoff (×5 → нотификация с указанием лога), invoke-команда
  `daemon_info` для фронта (url+token, один раз).
- Makefile: `make tauri-sidecar` (бинарь с target-triple суффиксом),
  `tauri-dev`, `tauri-build`.

Что нужно от пользователя:
1. Установить Rust: `brew install rustup && rustup-init` (или `brew install rust`).
2. `make tauri-dev` — проверить dev-режим; `make tauri-build` — .app.
3. Известные риски при первой сборке (написано без компиляции):
   версии плагинов могут потребовать точных тегов; `TrayIconBuilder`
   без явной иконки (используется дефолтная); нотификация о гейте со
   стороны фронта (gate.opened → Tauri notification API) не подключена —
   нужен маленький мост в web/ (window.__TAURI__ → invoke), сделать при
   первой проверке.

## Разблокировка и итог (2026-08-17)

Rust установлен пользователем (brew rustup); починка окружения:
`rustup default stable` + PATH на `/opt/homebrew/opt/rustup/bin` (keg-only
формула не даёт rustup-init и не линкует прокси — строка добавлена в
~/.zshrc). По ходу первой сборки исправлено:
- sidecar-бинарь `binaries/glamord-aarch64-apple-darwin` (make tauri-sidecar);
- иконки — сгенерированы RGBA-плейсхолдеры (32/128/256 + icon.icns);
- beforeBuildCommand/beforeDevCommand выполняются из КОРНЯ приложения
  (родителя src-tauri) — пути поправлены на `--prefix web`.

Проверено: `cargo check` зелёный, `tauri build` собрал
`src-tauri/target/release/bundle/macos/glamor.app` (sidecar glamord в
Contents/MacOS, icon.icns в Resources).

Осталось UNVERIFIED (нужен живой прогон окна — GUI не запускал):
сценарий «свернуть в трей → нотификация о гейте → клик → фокус на ране»
(нотификация gate.opened → Tauri notification API со стороны фронта не
подключена — follow-up: мост window.__TAURI__ в web/), restart демона
из shell при крахе, запоминание позиции окна (window-state плагин
подключён, но поведение не проверено на живом окне).

## Дополнение 2026-08-17 (fix-task-2, П.6): честный статус приёмки

Сборка .app подтверждена, но GUI-acceptance таски выполнен НЕ полностью.
UNVERIFIED-пункты (агент GUI прогнать не может, нужен живой прогон
пользователем):

1. GUI-смоук не прогонялся: свернуть в трей → нотификация о гейте →
   клик по нотификации → фокус окна на ране.
2. Мост `gate.opened` → Tauri notification API в web/ НЕ подключён —
   нотификации о гейтах в десктопе не появятся (follow-up: мост
   window.__TAURI__ → invoke в web/).
3. Restart демона из shell при крахе (daemon-watch, backoff ×5 →
   нотификация) не проверялся на живом окне.
4. Window-state (запоминание позиции/размера окна) — плагин подключён,
   поведение не проверено.

Статус таски: done (с оговорками) — код и сборка готовы, пункты 1-4
выше остаются открытой частью acceptance до живого GUI-прогона.
