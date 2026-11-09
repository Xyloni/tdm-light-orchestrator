@echo off
setlocal
cd /d "%~dp0" || exit /b 1
title TDM Light Orchestrator

where go >nul 2>nul
if errorlevel 1 (
    echo [ERROR] Go is not installed or is missing from PATH.
    echo Install Go 1.22 or newer, then run this file again.
    pause
    exit /b 1
)

set "GOCACHE=%CD%\.gocache"
echo Building TDM Light Orchestrator...
go build -o tdm-light-orchestrator.exe .
if errorlevel 1 (
    echo.
    echo [ERROR] Build failed. Check the Go error messages above.
    pause
    exit /b 1
)

if not defined ORCHESTRATOR_WEB_ADDR set "ORCHESTRATOR_WEB_ADDR=127.0.0.1:8787"
echo Starting TDM Light Orchestrator. Press Ctrl+C to stop.
echo Local web panel: http://%ORCHESTRATOR_WEB_ADDR%
echo.
tdm-light-orchestrator.exe --orchestrator
set "APP_EXIT=%ERRORLEVEL%"
echo.
echo TDM Light Orchestrator exited with code %APP_EXIT%.
pause
exit /b %APP_EXIT%
