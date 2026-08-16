#!/bin/bash
# fake harness: попытка 1 — долгая (interrupt&steer), попытка 2 — успех
WORK_DIR="$1"
PROMPT="$2"
MARKER="$WORK_DIR/.attempt-marker"
echo '{"kind":"session.init","session_id":"fake-session-1"}'
if [ -f "$MARKER" ]; then
  echo "$PROMPT" > "$WORK_DIR/prompt-2.log"
  echo "artifact" > "$WORK_DIR/artifact.txt"
  exit 0
fi
touch "$MARKER"
echo "$PROMPT" > "$WORK_DIR/prompt-1.log"
for i in $(seq 1 600); do
  echo "{\"kind\":\"assistant.text\",\"text\":\"tick $i\"}"
  sleep 1
done
