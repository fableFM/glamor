#!/bin/bash
# fake harness: exit 0, но обязательного артефакта нет → failed
echo '{"kind":"assistant.text","text":"done but no artifact"}'
exit 0
