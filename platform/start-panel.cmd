@echo off
rem Campfire panel launcher - starts the panel in its own window, then opens the browser.
rem Connection settings are read from %APPDATA%\campfire\panel.json
cd /d "%~dp0"
start "Campfire Panel" "%~dp0bin\campfire-panel.exe"
timeout /t 3 >nul
start "" "http://127.0.0.1:8717"
exit
