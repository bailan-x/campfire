#!/bin/bash
# Install Caddy on the Aliyun server and front the SSH reverse tunnel (127.0.0.1:18717)
set -euo pipefail

echo "== 1. install binary =="
install -m 0755 /tmp/caddy /usr/local/bin/caddy
/usr/local/bin/caddy version

echo "== 2. user & dirs =="
if ! id caddy >/dev/null 2>&1; then
  useradd --system --home /var/lib/caddy --shell /usr/sbin/nologin caddy
fi
mkdir -p /var/lib/caddy /etc/caddy /var/log/caddy
chown -R caddy:caddy /var/lib/caddy /var/log/caddy

echo "== 3. Caddyfile =="
cat > /etc/caddy/Caddyfile <<'CADDY'
{
	# never expose the admin API outside this machine
	admin localhost:2019
	storage file_system {
		root /var/lib/caddy
	}
}

# 固定公网地址：sslip.io 会把 203-0-113-10 解析回服务器 IP，证书由 Let's Encrypt 自动签发/续期
dst-demo.example.com {
	encode zstd gzip
	reverse_proxy 127.0.0.1:18717 {
		header_up X-Real-IP {remote_host}
	}
	log {
		output file /var/log/caddy/access.log
		format console
	}
}

# 直接访问 IP 的 http 请求转到 https 域名
http://203.0.113.10 {
	redir https://dst-demo.example.com{uri} permanent
}
CADDY
chown caddy:caddy /etc/caddy/Caddyfile

echo "== 4. systemd unit =="
cat > /etc/systemd/system/caddy.service <<'UNIT'
[Unit]
Description=Caddy web server (campfire panel front)
Documentation=https://caddyserver.com/docs/
After=network-online.target
Wants=network-online.target

[Service]
User=caddy
Group=caddy
ExecStart=/usr/local/bin/caddy run --environ --config /etc/caddy/Caddyfile
ExecReload=/usr/local/bin/caddy reload --config /etc/caddy/Caddyfile --force
TimeoutStopSec=10s
LimitNOFILE=1048576
PrivateTmp=true
ProtectSystem=full
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT

echo "== 5. validate & start =="
/usr/local/bin/caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile 2>&1 | tail -3
systemctl daemon-reload
systemctl enable caddy
systemctl restart caddy
sleep 3
systemctl is-active caddy
ss -ltn | grep -E ':(80|443) ' || echo "no-listen-yet"

echo "== 6. recent log =="
journalctl -u caddy -n 15 --no-pager | tail -15
