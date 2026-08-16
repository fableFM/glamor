#!/bin/bash
# fake harness: диалог D-20 — попытка 1: questions.md; попытка 2: чистый spec
WORK_DIR="$1"
PROMPT="$2"
MARKER="$WORK_DIR/.attempt-marker"
echo '{"kind":"session.init","session_id":"fake-session-1"}'
if [ -f "$MARKER" ]; then
  echo "$PROMPT" > "$WORK_DIR/prompt-2.log"
  rm -f "$WORK_DIR/questions.md"
  echo "# spec v2" > "$WORK_DIR/spec.md"
  exit 0
fi
touch "$MARKER"
echo "$PROMPT" > "$WORK_DIR/prompt-1.log"
echo "1. Какую БД использовать?" > "$WORK_DIR/questions.md"
echo "# spec v1" > "$WORK_DIR/spec.md"
exit 0
