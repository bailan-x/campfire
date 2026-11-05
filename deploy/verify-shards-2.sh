#!/usr/bin/env bash
# Focused check: mod errors in current run, override files, load, entrances
set -u
CL=/home/dst/.klei/DoNotStarveTogether/MyDediServer
echo "== uptime/load =="
uptime
echo "== master error counts since unit start =="
SINCE=$(systemctl show -p ActiveEnterTimestamp --value dst-server)
echo "since: $SINCE"
sudo journalctl -u dst-server --since "$SINCE" --no-pager -o cat > /tmp/master.log
sudo journalctl -u dst-server-caves --since "$(systemctl show -p ActiveEnterTimestamp --value dst-server-caves)" --no-pager -o cat > /tmp/caves.log
echo "master Disabling: $(grep -c 'Disabling workshop' /tmp/master.log)  errors: $(grep -ci 'error' /tmp/master.log)  lines: $(wc -l < /tmp/master.log)"
echo "caves  Disabling: $(grep -c 'Disabling workshop' /tmp/caves.log)  errors: $(grep -ci 'error' /tmp/caves.log)  lines: $(wc -l < /tmp/caves.log)"
echo "== sample errors master =="
grep -i 'error' /tmp/master.log | sort | uniq -c | sort -rn | head -5
echo "== override files =="
sudo ls -l "$CL/Master/worldgenoverride.lua" "$CL/Caves/worldgenoverride.lua"
echo "--- master override content ---"
sudo cat "$CL/Master/worldgenoverride.lua"
echo "--- caves override content ---"
sudo cat "$CL/Caves/worldgenoverride.lua"
echo "== portals master (current run) =="
grep 'Validating portal' /tmp/master.log | sed 's/.*(//; s/)//' | sort | uniq -c
echo "== steam network =="
sudo ss -tunp | grep -c 'ESTAB' || true
