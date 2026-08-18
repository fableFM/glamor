#!/bin/bash
# fake harness: verdict ВСЕГДА changes_required → эскалация петли
WORK_DIR="$1"
PROMPT="$2"
RUN_DIR=$(echo "$PROMPT" | grep -oE '/[^ `"]+/[a-z-]+\.(md|json)' | head -1 | xargs dirname)
echo '{"kind":"session.init","session_id":"fake-1"}'
case "$PROMPT" in
  *"Роль: FIXER"*)
    echo "# handoff fix" > "$RUN_DIR/handoff.md"; exit 0;;
  *"Роль: REVIEWER"*)
    echo '{"verdict":"changes_required","findings":[{"id":"REV-001","severity":"blocking","file":"x.go","observed":"x","expected":"y","required_fix":"fix x","forbidden_fix":null}]}' > "$RUN_DIR/verdict.json"
    exit 0;;
  *"Роль: CODER"*)
    echo "# handoff" > "$RUN_DIR/handoff.md"; exit 0;;
  *"Роль: PLANNER"*)
    echo "# spec" > "$RUN_DIR/spec.md"; exit 0;;
esac
exit 1
