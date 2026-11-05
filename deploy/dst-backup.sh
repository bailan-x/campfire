#!/usr/bin/env bash
# DST 存档自动备份：打包 MyDediServer，保留最近 7 份
set -euo pipefail
SRC="/home/dst/.klei/DoNotStarveTogether"
DEST="/home/dst/backups"
KEEP=7
mkdir -p "$DEST"
ts=$(date +%Y%m%d-%H%M%S)
tar -czf "$DEST/MyDediServer-$ts.tar.gz" -C "$SRC" MyDediServer
ls -1t "$DEST"/MyDediServer-*.tar.gz 2>/dev/null | tail -n +$((KEEP + 1)) | xargs -r rm -f
echo "backup done: $DEST/MyDediServer-$ts.tar.gz ($(du -h "$DEST/MyDediServer-$ts.tar.gz" | cut -f1))"
