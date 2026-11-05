param(
  [string]$Target = "ops@203.0.113.10",
  [string]$Key    = "<SSH_KEY_PATH>",
  [string]$Known  = "E:\gamedeepseek\secrets\known_hosts"
)

$ErrorActionPreference = "Stop"
$bin    = "E:\gamedeepseek\spike\bin\spike-linux"
$unit   = "E:\gamedeepseek\spike\campfire-spike-agent.service"
$sshOpt = @("-i", $Key, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=$Known", "-o", "ConnectTimeout=20")

Write-Host "==> 上传二进制与 unit"
scp -i $Key -o StrictHostKeyChecking=no -o UserKnownHostsFile=$Known -o ConnectTimeout=20 $bin "${Target}:/tmp/spike-linux"
scp -i $Key -o StrictHostKeyChecking=no -o UserKnownHostsFile=$Known -o ConnectTimeout=20 $unit "${Target}:/tmp/campfire-spike-agent.service"

Write-Host "==> 安装并启动"
$remote = @'
set -e
sudo install -m755 /tmp/spike-linux /usr/local/bin/campfire-spike-agent
sudo install -m644 /tmp/campfire-spike-agent.service /etc/systemd/system/campfire-spike-agent.service
sudo systemctl daemon-reload
sudo systemctl enable --now campfire-spike-agent
sleep 1
systemctl --no-pager --lines=0 status campfire-spike-agent
ls -l /run/campfire/agent.sock
'@
& ssh @sshOpt $Target $remote

Write-Host "==> 完成。接着运行："
Write-Host "  E:\gamedeepseek\spike\bin\spike.exe tunnel -addr 203.0.113.10:22 -user ops -key $Key -sock /run/campfire/agent.sock -op status -rounds 5"
