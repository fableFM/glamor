# T-22 Janitor-этапы (build/lint/test без LLM)

Статус: done (2026-08-17) · M2 · зависимости: T-09 (supervisor), T-20 (тип ноды)

## Цель

Детерминированные шаги в пайплайне (мотив bernstein из BRAINSTORM):
прогон build/lint/test скриптом до ревьюера — дёшево и быстро.

## Модель

- Нода `janitor`: список команд (shell), workdir = чекаут проекта,
  таймаут на команду, политика при фейле: `fail_stage` (дефолт) |
  `warn` (событие, пайплайн идёт дальше).
- Выполнение — тем же supervisor (T-09), но без harness: события
  `stream.text` генерятся из stdout/stderr скрипта; артефакт — лог
  `janitor-<n>.log`; exit code != 0 → stage failed по политике.
- Verdict-совместимость: выхлоп janitor-этапа прикрепляется к контексту
  следующего LLM-этапа (например fixer получает лог упавших тестов через
  плейсхолдер `{{artifact.janitor_log}}`).
- Команды задаются в редакторе (T-20) многострочным полем; дефолт для
  Go-проектов: `go build ./...`, `go vet ./...`, `go test ./...`
  (пресет-подсказка в UI).

## Безопасность

- Команды — из конфигурации пайплайна (доверенная, её пишет пользователь),
  НЕ из вывода LLM. Ядро никогда не исполняет строки, сгенерированные
  моделью, как janitor-команды.

## Acceptance

- Пайплайн code → janitor(go build+test) → review работает; падение
  тестов → fixer получает лог → фикс → повтор.
- Время и exit codes видны в метриках этапа.

## Итог (2026-08-17)

Сделано:
- StageSpec: kind="janitor" + `commands` []string, `on_fail`
  (fail_stage|warn), `command_timeout_sec` (дефолт 10m).
- supervisor.runJanitor: команды через sh -c в чекауте проекта (process
  group + таймаут), вывод построчно → stream.text события (батчер) +
  лог `{run_dir}/janitor.log` (фикс. имя — плейсхолдер
  {{artifact.janitor.log}} для следующих этапов); лог регистрируется
  артефактом kind=janitor_log; exit!=0 → fail_stage (дефолт) или warn
  (stream.error + продолжение). Без harness — ветка в launchStage.
- Безопасность: команды только из конфигурации пайплайна (доверенная),
  зафиксировано в spec.go комментарием.
- UI-редактор: janitor-нода включается в палитре — в брифе UI-агента
  (T-20 делал её disabled; включение + поле commands — UI-батч).

Проверка: TestJanitorSuccess (лог+артефакт+exit 0), TestJanitorFailStage
(exit 3 → failed, ран failed), TestJanitorWarn (warn → succeeded) —
все с -race зелёные.
