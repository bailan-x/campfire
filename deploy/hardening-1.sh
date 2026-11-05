#!/usr/bin/env bash
# 阶段一：创建非 root 管理用户 ops（密钥登录 + sudo），暂不改 sshd
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

if ! id ops >/dev/null 2>&1; then
  adduser --disabled-password --gecos "" ops
  echo "[ok] created user ops"
else
  echo "[skip] user ops exists"
fi

usermod -aG sudo ops

install -d -m 700 -o ops -g ops /home/ops/.ssh
# 复用 root 的 authorized_keys（即本机 dst_rsa 对应的公钥）
install -m 600 -o ops -g ops /root/.ssh/authorized_keys /home/ops/.ssh/authorized_keys

echo 'ops ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/90-ops
chmod 440 /etc/sudoers.d/90-ops
visudo -cf /etc/sudoers.d/90-ops

echo "=== 结果 ==="
id ops
ls -l /home/ops/.ssh/
echo "--- sudo 权限 ---"
sudo -l -U ops | tail -3
echo "=== 阶段一完成 ==="
