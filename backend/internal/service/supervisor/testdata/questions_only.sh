#!/bin/bash
# fake harness: этап пишет ТОЛЬКО questions.md (без основного артефакта) —
# валидный исход по D-20 (реальный kimi так и делает)
WORK_DIR="$1"
PROMPT="$2"
RUN_DIR=$(echo "$PROMPT" | grep -oE '/[^ `"]+/[a-z._-]+\.(md|json|txt)' | head -1 | xargs dirname)
echo '{"kind":"session.init","session_id":"fake-1"}'
echo "1. Какой транспорт?" > "$RUN_DIR/questions.md"
exit 0
