@echo off
rem 篝火面板：固定公网入口（SSH 反向隧道 -> 阿里云 Caddy）
rem 关掉这个窗口 = 公网入口断开（本机面板不受影响）
setlocal
set KEY=<SSH_KEY_PATH>
set KH=E:\gamedeepseek\secrets\known_hosts
set SRV=ops@203.0.113.10
title Campfire fixed public link (do not close)
:loop
echo [%date% %time%] connecting tunnel...
ssh -N -T -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -o ExitOnForwardFailure=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="%KH%" -i "%KEY%" -R 18717:127.0.0.1:8717 %SRV%
echo [%date% %time%] tunnel exited, retry in 5s...
timeout /t 5 /nobreak >nul
goto loop
