#!/bin/bash
# fake harness: coder падает (exit 0 без обязательного артефакта → этап
# failed → ран failed); distill отрабатывает (T-30: терминальный distill
# на провальном исходе).
WORK_DIR="$1"
PROMPT="$2"
RUN_DIR=$(echo "$PROMPT" | grep -oE '/[^ `"]+/[a-z-]+\.(md|json)' | head -1 | xargs dirname)
echo '{"kind":"session.init","session_id":"fake-1"}'
case "$PROMPT" in
  *"Роль: DISTILL"*)
    printf -- '---\ntitle: урок из провала\ntriggers: [провал]\n---\n\n## Причина (почему)\nКодер не записал handoff.\n\n## Правило\nВсегда пиши артефакт.\n' > "$RUN_DIR/lessons.md"; exit 0;;
  *"Роль: REVIEWER"*)
    echo '{"verdict":"approved","findings":[]}' > "$RUN_DIR/verdict.json"; exit 0;;
  *"Роль: CODER"*)
    exit 0;; # артефакт НЕ записан — classify → failed
  *"Роль: PLANNER"*)
    echo "# spec" > "$RUN_DIR/spec.md"; exit 0;;
esac
exit 1
