#!/bin/bash
# fake harness: успешный этап — NDJSON, артефакт, exit 0
WORK_DIR="$1"
PROMPT="$2"
echo '{"kind":"session.init","session_id":"fake-session-1"}'
echo "$PROMPT" > "$WORK_DIR/prompt-1.log"
echo '{"kind":"assistant.text","text":"doing work"}'
echo "artifact" > "$WORK_DIR/artifact.txt"
echo "artifact" > "$WORK_DIR/a1.txt"
echo "artifact" > "$WORK_DIR/a2.txt"
echo '{"kind":"usage","usage":{"input":100,"output":50}}'
echo '{"kind":"usage","usage":{"input":7,"output":3}}'
exit 0
