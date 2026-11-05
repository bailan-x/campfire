#!/usr/bin/env bash
# Verify sharded DST cluster state (master + caves)
set -u
CL=/home/dst/.klei/DoNotStarveTogether/MyDediServer
echo "== services =="
for u in dst-server dst-server-caves; do
  printf '%-20s %s\n' "$u" "$(systemctl is-active $u) $(systemctl show -p NRestarts --value $u | sed 's/^/restarts=/')"
done
echo "== ports =="
sudo ss -ulnp | grep -E ':1099[89]|:10889|:2701[67]|:876[67]' | awk '{print $1, $4, $6}'
echo "== master: online/connected/errors =="
sudo journalctl -u dst-server --no-pager -o cat | grep -E 'Online Server Started|World 2\(Caves\) is now|Registering Mod workshop' | tail -14
echo "  disabling-count: $(sudo journalctl -u dst-server --no-pager -o cat | grep -c 'Disabling workshop')"
echo "== caves: online/connected/errors =="
sudo journalctl -u dst-server-caves --no-pager -o cat | grep -E 'Online Server Started|World 1\(Master\) is now|Registering Mod workshop|secondary shard is now ready' | tail -14
echo "  disabling-count: $(sudo journalctl -u dst-server-caves --no-pager -o cat | grep -c 'Disabling workshop')"
echo "== world override files =="
sudo ls -l "$CL"/*/worldgenoverride.lua "$CL"/*/leveldataoverride.lua 2>/dev/null
echo "== caves shardindex overrides =="
sudo grep -E 'grassgekkos|twiggytrees_regrowth|wildfires|summerhounds|mutated_buzzard_gestalt|shard_enabled|id=|name=' "$CL/Caves/save/shardindex" 2>/dev/null | head -20
echo "== cluster.ini SHARD section =="
sudo sed -n '/\[SHARD\]/,/^\[/p' "$CL/cluster.ini"
echo "== per-shard server.ini =="
for s in Master Caves; do echo "--- $s"; sudo cat "$CL/$s/server.ini" | grep -E 'server_port|is_master|^name|^id|master_server_port|authentication_port'; done
echo "== memory/disk =="
free -m | head -3
df -h / | tail -1
echo "== ugc contents =="
for s in Master Caves; do printf '%-7s %s dirs, %s\n' "$s" "$(sudo ls /home/dst/dst-server/ugc_mods/MyDediServer/$s/content/322330 2>/dev/null | wc -l)" "$(sudo du -sh /home/dst/dst-server/ugc_mods/MyDediServer/$s/content/322330 2>/dev/null | cut -f1)"; done
echo "== dst-mods status =="
sudo dst-mods status
