#!/bin/bash
# fake harness: долгий этап с периодическими событиями (для stop-теста)
echo '{"kind":"session.init","session_id":"fake-session-1"}'
for i in $(seq 1 600); do
  echo "{\"kind\":\"assistant.text\",\"text\":\"tick $i\"}"
  sleep 1
done
