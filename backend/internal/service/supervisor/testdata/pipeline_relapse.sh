#!/bin/bash
# fake harness: reviewer дважды находит ОДИН И ТОТ ЖЕ blocking-finding
# (REV-001, итерации 1-2), на третьей — approved (T-30, M4: relapse-дедуп).
# distill пишет NO_LESSONS (гейт lesson_review не открывается).
WORK_DIR="$1"
PROMPT="$2"
RUN_DIR=$(echo "$PROMPT" | grep -oE '/[^ `"]+/[a-z-]+\.(md|json)' | head -1 | xargs dirname)
echo '{"kind":"session.init","session_id":"fake-1"}'
case "$PROMPT" in
  *"Роль: DISTILL"*)
    echo "NO_LESSONS" > "$RUN_DIR/lessons.md"; exit 0;;
  *"Роль: FIXER"*)
    echo "# handoff fix" > "$RUN_DIR/handoff.md"; exit 0;;
  *"Роль: REVIEWER"*)
    N=$(cat "$RUN_DIR/.review-count" 2>/dev/null || echo 0); N=$((N+1)); echo "$N" > "$RUN_DIR/.review-count"
    if [ "$N" -ge 3 ]; then
      echo '{"verdict":"approved","findings":[]}' > "$RUN_DIR/verdict.json"
    else
      echo '{"verdict":"changes_required","findings":[{"id":"REV-001","severity":"blocking","file":"queue.go","line":10,"observed":"очередь на файлах в sqlite демоне","expected":"очередь в sqlite","required_fix":"перенеси очередь в sqlite, файлы не атомарны","forbidden_fix":null}]}' > "$RUN_DIR/verdict.json"
    fi
    exit 0;;
  *"Роль: CODER"*)
    echo "# handoff" > "$RUN_DIR/handoff.md"; exit 0;;
  *"Роль: PLANNER"*)
    echo "# spec" > "$RUN_DIR/spec.md"; exit 0;;
esac
exit 1
