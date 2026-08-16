#!/bin/bash
# fake harness: первая попытка падает (exit 1, нет артефакта), вторая — успех
WORK_DIR="$1"
MARKER="$WORK_DIR/.attempt-marker"
echo '{"kind":"session.init","session_id":"fake-session-1"}'
if [ -f "$MARKER" ]; then
  echo "artifact" > "$WORK_DIR/artifact.txt"
  echo "artifact" > "$WORK_DIR/a1.txt"
  echo "artifact" > "$WORK_DIR/a2.txt"
  exit 0
fi
touch "$MARKER"
exit 1
