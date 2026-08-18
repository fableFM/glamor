# T-21 Версионирование пайплайнов + YAML import/export

Статус: done (2026-08-17) · M2 · зависимости: T-02

## Цель

Пайплайны как переносимый артефакт (D-62, «поделиться с коллегой»).

## Scope

- CRUD API пайплайнов: `POST /pipelines` (создать), `POST
  /pipelines/{id}/versions` (новая версия из spec_json), `GET
  /pipelines/{id}/versions`, `GET /pipeline-versions/{vid}`.
- Неизменяемость версий: `spec_json` версии immutable; правка = новая
  версия с `parent_version_id`.
- YAML-формат экспорта: человекочитаемый, одна версия = один файл:
  мета (имя, версия, parent), ноды (все поля), рёбра (тип, max_iters).
  Стабильный порядок ключей (для git-diff).
- Импорт: `POST /pipelines/import` (yaml) — валидация по правилам T-20,
  создание нового пайплайна (или новой версии существующего по имени —
  флаг `on_conflict: new|version|fail`, дефолт new).
- UI: кнопки Export (скачать yaml) / Import (загрузить файл) в списке
  пайплайнов проекта и глобальных.

## Acceptance

- Round-trip: экспорт → импорт даёт эквивалентный spec (тест на
  глубокое сравнение).
- Импорт битого/неизвестной версии формата — понятные ошибки.
- YAML дефолтного пайплайна M1 лежит в репо (`pipelines/default.yaml`) и
  используется как seed (заменяет seed из T-17, когда появится).

## Итог (2026-08-17)

Сделано:
- API: POST /pipelines, POST /pipelines/{id}/versions, GET
  /pipelines/{id}/versions, GET /pipeline-versions/{vid}, POST
  /pipelines/import, GET /pipeline-versions/{vid}/export (text/yaml).
- Неизменяемость версий: правка = CreatePipelineVersion (version+1,
  parent_version_id); ValidateSpec блокирует невалидные спеки.
- YAML (service/pipeline/yaml.go): стабильный порядок ключей (struct),
  meta (name/version/parent_version_id), stages, loop, final_gate.
- Валидация (validate.go): структура, ссылки loop, виды гейтов,
  обязательные артефакты, известные плейсхолдеры (список T-17).
- Импорт: on_conflict new (суффикс -2…)/version/fail; битый YAML и
  неизвестные плейсхолдеры — понятные 400 validation.
- Seed дефолтного пайплайна теперь из **embedded default.yaml**
  (backend/internal/service/pipeline/default.yaml — единый источник);
  в корне репо — симлинк `pipelines/default.yaml` → на него.
  Регенерация после правки промптов: `GEN_YAML=1 go test -run
  TestGenerateDefaultYAML ./internal/service/pipeline/` (golden-тест
  TestDefaultYAML_UpToDate следит за синхронизацией).
- UI-кнопки Export/Import — в брифе агента T-20 (редактор), приедут
  вместе с ним.

Проверка: round-trip экспорт→импорт (глубокое сравнение спек),
on_conflict new/version/fail, битые YAML, golden default.yaml,
неизменяемость v1 после создания v2. Полный прогон: 11 пакетов зелёные
с -race, lint 0 issues, make gen идемпотентен.
