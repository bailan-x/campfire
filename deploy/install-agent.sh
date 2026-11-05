#!/usr/bin/env bash
# 安装/升级 campfire-agent（root 执行；由本机 scp 上传后 bash 运行）
set -euo pipefail

BIN_SRC="/tmp/campfire-agent"
BIN_DST="/usr/local/bin/campfire-agent"

echo "== 1. 创建 campfire 组并让 ops 加入（面板通过 SSH 以 ops 身份访问 socket）"
getent group campfire >/dev/null || groupadd --system campfire
usermod -aG campfire ops

echo "== 2. 安装二进制"
install -m 0755 "$BIN_SRC" "$BIN_DST"
"$BIN_DST" -h 2>&1 | head -3 || true

echo "== 3. 写 systemd 单元"
cat > /etc/systemd/system/campfire-agent.service <<'EOF'
[Unit]
Description=Campfire Agent (DST control plane)
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/campfire-agent \
  -sock /run/campfire/agent.sock \
  -cluster-dir /home/dst/.klei/DoNotStarveTogether/MyDediServer \
  -install-dir /home/dst/dst-server \
  -home /home/dst \
  -game-user dst \
  -version 0.1.0
RuntimeDirectory=campfire
RuntimeDirectoryMode=0755
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

echo "== 4. 启动"
systemctl daemon-reload
systemctl enable campfire-agent >/dev/null 2>&1 || true
systemctl restart campfire-agent
sleep 2
systemctl --no-pager --lines=8 status campfire-agent || true
echo "== 5. socket 权限"
ls -l /run/campfire/agent.sock
echo "OK"
