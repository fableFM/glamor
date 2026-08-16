# T-13 Web UI scaffold + API/WS инфраструктура

Статус: done (2026-08-16) · M1 · зависимости: T-01, T-05 (спека), T-04 (WS-протокол)

## Цель

Базовый фронтенд-каркас, на котором строятся все экраны (D-06).

## Scope

- Vite + React + TS + zustand; роутер (react-router или tanstack-router —
  выбрать, tanstack-router предпочтителен за type-safety).
- Дизайн-система: тёмная тема по умолчанию, светлая — позже; CSS —
  Tailwind (v4) + минимум кастомных компонентов; иконки — lucide.
- `web/src/api/`: сгенерированный TS-клиент из openapi.yaml (orval или
  openapi-typescript — выбрать, зафиксировать в T-05) + токен из
  конфига/локального файла, который Tauri подкладывает (в браузерном
  dev-режиме — из env/localStorage).
- **WS-клиент** (`web/src/lib/events.ts`):
  - reconnect с экспоненциальным backoff;
  - хранение `last_event_id` в памяти стора (per run);
  - после реконнекта — догон через `/ws?last_event_id=` (T-04);
  - dispatch событий в zustand-сторы по kind.
- **Сторы**: `projects`, `runs`, `runDetails` (stages/gates/artifacts),
  `streams` (буферы событий стрима per stage, с cap по размеру — например
  последние 2000 событий, чтобы не раздувать память).
- Обработка `synced`-границы replay/live (T-04): события до границы — bulk
  apply, после — инкрементально.
- Глобальный индикатор соединения с демоном (плашка «демон недоступен,
  переподключение...»).

## Scope (out)

- Конкретные экраны (T-14/15/16) — здесь только инфраструктура и layout-
  каркас (роуты-заглушки).

## Acceptance

- `npm run build` чист; type-check без `any` в api-слое.
- Тест (vitest) WS-клиента: эмуляция разрыва → реконнект → догон без
  дублей/пропусков (мок-сервер).
- Индикатор соединения реагирует на убитый демон.

## Итог (2026-08-16, субагент; проверено мной: npm run build + vitest зелёные)

Сделано (web/src/):
- TanStack Router (code-based, типизированные параметры), zustand,
  Tailwind v4 (тёмная тема), lucide-react.
- api/client.ts — typed fetch поверх schema.d.ts (без any), ApiError с
  {code,message,details}, токен из localStorage/VITE_GLAMOR_TOKEN,
  базовый URL VITE_GLAMOR_API.
- lib/events.ts — WS-клиент: reconnect backoff 500ms×2 cap 8s + джиттер,
  last_event_id per run, атомарный replay (bulk на synced, обрыв до
  synced → откат курсора), дедуп по id; lib/dispatch.ts — роутинг
  событий в сторы по kind.
- Сторы: projects, runs, runDetails, streams (cap 2000/стадия),
  connection (online/reconnecting/offline) + ConnectionBanner.
- Layout + роуты-заглушки /, /projects/$id, /runs/$id (экраны — T-14/15/16).
- vitest: 3 теста WS-клиента на мок-сервере (synced-граница, реконнект
  без дублей/пропусков, откат при обрыве посреди replay).

Проверка: npm run build чист, npx vitest run 3/3, oxlint 0 warnings,
any в api-слое отсутствует (grep).
