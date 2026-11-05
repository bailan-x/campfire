install -m 0755 /tmp/campfire-agent-new /usr/local/bin/campfire-agent
systemctl restart campfire-agent
sleep 2
echo "agent: $(systemctl is-active campfire-agent) md5=$(md5sum /usr/local/bin/campfire-agent | cut -d' ' -f1)"
systemctl show -p ExecMainStartTimestamp campfire-agent