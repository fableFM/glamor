#!/bin/bash
# fake harness для janitor-тестов: пишет артефакт по имени файла в промпте
WORK_DIR="$1"
PROMPT="$2"
RUN_DIR=$(echo "$PROMPT" | grep -oE '/[^ `"]+/[a-z._-]+\.(md|json|txt)' | head -1 | xargs dirname)
echo '{"kind":"session.init","session_id":"fake-1"}'
case "$PROMPT" in
  *out.txt*) echo "out" > "$RUN_DIR/out.txt"; exit 0;;
  *review.md*) echo "ok" > "$RUN_DIR/review.md"; exit 0;;
esac
exit 0
