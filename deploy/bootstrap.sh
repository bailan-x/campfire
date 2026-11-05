#!/usr/bin/env bash
# campfire: DST 专服引导脚本（第一步：依赖 + swap + SteamCMD + 游戏本体）
set -euo pipefail
LOG=/root/dst-bootstrap.log
exec >>"$LOG" 2>&1
echo "=== start $(date -Is) ==="
export DEBIAN_FRONTEND=noninteractive

echo "--- [1/5] i386 arch + apt update ---"
dpkg --add-architecture i386
apt-get update -y

echo "--- [2/5] 32-bit runtime + tools ---"
apt-get install -y --no-install-recommends \
  lib32gcc-s1 lib32stdc++6 libstdc++6:i386 libcurl4:i386 libcurl4-gnutls-dev:i386 \
  ca-certificates curl unzip tar rsync tmux file

echo "--- [3/5] swap 2G ---"
if ! swapon --show | grep -q '/swapfile'; then
  if [ ! -f /swapfile ]; then
    fallocate -l 2G /swapfile || dd if=/dev/zero of=/swapfile bs=1M count=2048
    chmod 600 /swapfile
    mkswap /swapfile
  fi
  swapon /swapfile
  grep -q '^/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
  sysctl -w vm.swappiness=10
  echo 'vm.swappiness=10' > /etc/sysctl.d/99-dst.conf
fi
swapon --show || true

echo "--- [4/5] dst user + steamcmd ---"
id dst >/dev/null 2>&1 || useradd -m -s /bin/bash dst
mkdir -p /home/dst/steamcmd
cd /home/dst/steamcmd
if [ ! -x ./steamcmd.sh ]; then
  curl -fsSL -o steamcmd_linux.tar.gz https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz
  tar -xzf steamcmd_linux.tar.gz
  rm -f steamcmd_linux.tar.gz
fi
chown -R dst:dst /home/dst

echo "--- [5/5] steamcmd app_update 343050 (DST dedicated server) ---"
su - dst -c "/home/dst/steamcmd/steamcmd.sh +force_install_dir /home/dst/dst-server +login anonymous +app_update 343050 validate +quit"

echo "--- verify ---"
ls -la /home/dst/dst-server | head -20
du -sh /home/dst/dst-server 2>/dev/null || true
test -x /home/dst/dst-server/bin64/dontstarve_dedicated_server_nullrenderer_x64 && echo "BIN64_OK" || echo "BIN64_MISSING"
echo "=== done $(date -Is) ==="
