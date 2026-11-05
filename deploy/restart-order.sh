#!/bin/bash
echo "== 停掉两个分片 =="
sudo systemctl stop dst-server-caves
sudo systemctl stop dst-server
sleep 3
echo "== 先起洞穴（会在连不上 master 时自动重试），2 秒后再起地上 =="
sudo systemctl start dst-server-caves
sleep 2
sudo systemctl start dst-server
sleep 50
echo "== MASTER =="
sudo journalctl -u dst-server --since '-3min' --no-pager | grep -E 'Skipping portal|connected: \[LAN\]|Online Server Started|Sim paused|Registering Mod' | tail -15
echo "== CAVES =="
sudo journalctl -u dst-server-caves --since '-3min' --no-pager | grep -E 'Online Server Started|secondary shard|Sim paused|Registering Mod' | tail -8
echo "== PORTS =="
sudo ss -ulnp | grep -E '1099[89]|10889' || true
echo "== MEM =="
free -m | head -3
echo "== DST-MODS STATUS =="
sudo dst-mods status