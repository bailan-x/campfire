#!/usr/bin/env bash
# 阶段二：fail2ban + 自动安全更新 + sshd 加固（禁 root 登录）
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

echo "=== 1) 安装 fail2ban 与 unattended-upgrades ==="
apt-get update -qq
apt-get install -y -qq fail2ban unattended-upgrades >/dev/null

echo "=== 2) fail2ban sshd 策略 ==="
cat > /etc/fail2ban/jail.local <<'EOF'
[DEFAULT]
ignoreip = 127.0.0.1/8 ::1
bantime  = 1h
findtime = 10m
maxretry = 5

[sshd]
enabled = true
port    = ssh
backend = systemd
EOF
systemctl enable fail2ban >/dev/null 2>&1 || true
systemctl restart fail2ban

echo "=== 3) 自动安全更新 ==="
cat > /etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
EOF
# 确保不自动重启（重启会杀掉游戏服）
sed -i 's|^//\?\s*Unattended-Upgrade::Automatic-Reboot .*|Unattended-Upgrade::Automatic-Reboot "false";|' /etc/apt/apt.conf.d/50unattended-upgrades || true
grep -q 'Unattended-Upgrade::Automatic-Reboot' /etc/apt/apt.conf.d/50unattended-upgrades || echo 'Unattended-Upgrade::Automatic-Reboot "false";' >> /etc/apt/apt.conf.d/50unattended-upgrades
systemctl enable --now unattended-upgrades >/dev/null 2>&1 || true

echo "=== 4) sshd 加固（先校验再生效）==="
if grep -qE '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/\*\.conf' /etc/ssh/sshd_config; then
  cat > /etc/ssh/sshd_config.d/00-campfire.conf <<'EOF'
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
MaxAuthTries 4
EOF
  echo "[ok] wrote /etc/ssh/sshd_config.d/00-campfire.conf"
else
  echo "[warn] sshd_config 没有 Include 子目录，改用直接追加"
  printf '\nPermitRootLogin no\nPasswordAuthentication no\nPubkeyAuthentication yes\n' >> /etc/ssh/sshd_config
fi

if sshd -t; then
  systemctl reload ssh
  echo "[ok] sshd 配置校验通过并已 reload"
else
  echo "[FAIL] sshd 配置有误，未 reload（服务保持原状）"
  exit 1
fi

echo "=== 结果 ==="
sshd -T 2>/dev/null | grep -Ei '^(permitrootlogin|passwordauthentication|pubkeyauthentication|maxauthtries) '
echo "--- fail2ban ---"
systemctl is-active fail2ban
fail2ban-client status sshd 2>/dev/null | tail -5 || true
echo "=== 阶段二完成 ==="
