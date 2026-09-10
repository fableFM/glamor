#!/bin/bash
# fake harness: пайплайн с distill — planner/coder/reviewer(approved)/distill
# (T-30: гарантия встроенного distill, мастер-выключатель).
# Роль определяется маркером в промпте ($2), run_dir — из путей артефактов.
WORK_DIR="$1"
PROMPT="$2"
RUN_DIR=$(echo "$PROMPT" | grep -oE '/[^ `"]+/[a-z-]+\.(md|json)' | head -1 | xargs dirname)
echo '{"kind":"session.init","session_id":"fake-1"}'
case "$PROMPT" in
  *"Роль: DISTILL"*)
    printf -- '---\ntitle: тестовый урок\ntriggers: [тест]\n---\n\n## Причина (почему)\nПотому что тест.\n\n## Правило\nКогда тест — делай тест.\n' > "$RUN_DIR/lessons.md"; exit 0;;
  *"Роль: REVIEWER"*)
    echo '{"verdict":"approved","findings":[]}' > "$RUN_DIR/verdict.json"; exit 0;;
  *"Роль: CODER"*)
    echo "# handoff" > "$RUN_DIR/handoff.md"; exit 0;;
  *"Роль: PLANNER"*)
    echo "# spec" > "$RUN_DIR/spec.md"; exit 0;;
esac
exit 1
