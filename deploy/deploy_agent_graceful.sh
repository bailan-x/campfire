set -eu
sudo install -m 0755 /tmp/campfire-agent-new /usr/local/bin/campfire-agent
sudo systemctl restart campfire-agent
sleep 2
systemctl is-active campfire-agent
md5sum /usr/local/bin/campfire-agent