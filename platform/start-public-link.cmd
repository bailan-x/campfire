@echo off
title Campfire Public Link
cd /d "%~dp0"
echo.
echo  == Campfire public link (localhost.run) ==
echo  1) Panel must be running (start-panel.cmd / desktop shortcut).
echo  2) This prints a public URL like https://xxxx.lhr.life
echo  3) KEEP THIS WINDOW OPEN. Closing it closes the public link.
echo  4) The URL changes every time you restart this window.
echo.
ssh -T -o StrictHostKeyChecking=accept-new -o ServerAliveInterval=30 -R 80:127.0.0.1:8717 nokey@localhost.run
echo.
echo Link closed. Press any key to exit.
pause >nul
