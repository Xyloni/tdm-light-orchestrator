@echo off
setlocal
cd /d "%~dp0"

title tdm light

where go >nul 2>nul
if errorlevel 1 (
    echo [ERROR] Go is not installed or is missing from PATH.
    echo Install Go 1.22 or newer, then run this file again.
    pause
    exit /b 1
)

if not exist "config.json" (
    if exist "config.json.example" (
        copy /Y "config.json.example" "config.json" >nul
        echo Created config.json from the example.
    ) else (
        echo [ERROR] config.json and config.json.example are both missing.
        pause
        exit /b 1
    )
)

if not exist "config.env" (
    if exist "config.env.example" (
        copy /Y "config.env.example" "config.env" >nul
        echo Created config.env from the example.
    )
)

if not exist "account.txt" if exist "accounts.txt" (
    move "accounts.txt" "account.txt" >nul
    if errorlevel 1 exit /b 1
)

if not exist "account.txt" (
    if exist "account.txt.example" (
        copy /Y "account.txt.example" "account.txt" >nul
        echo Created account.txt from the example.
    ) else (
        echo [ERROR] account.txt and account.txt.example are both missing.
        pause
        exit /b 1
    )
    set "NEEDS_ACCOUNTS_SETUP=1"
)

if not exist ".gocache" mkdir ".gocache"
set "GOCACHE=%CD%\.gocache"

echo Building tdm light...
go build -o tdm-light.exe .
if errorlevel 1 (
    echo.
    echo [ERROR] Build failed. Check the Go error messages above.
    pause
    exit /b 1
)

where powershell.exe >nul 2>nul
if errorlevel 1 (
    echo.
    echo [ERROR] Windows PowerShell is required for the startup setup prompts.
    pause
    exit /b 1
)

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0setup.ps1"
if errorlevel 1 (
    echo.
    echo [ERROR] Setup was not completed.
    pause
    exit /b 1
)

if defined NEEDS_ACCOUNTS_SETUP (
    echo.
    echo Replace the sample rows in account.txt with your real account data.
    echo Then run start.bat again.
    pause
    exit /b 0
)

echo Starting tdm light. Press Ctrl+C to stop.
echo.
tdm-light.exe
set "APP_EXIT=%ERRORLEVEL%"
echo.
echo tdm light exited with code %APP_EXIT%.
pause
exit /b %APP_EXIT%
