@echo off
chcp 65001 >nul
echo ============================================
echo  Nanfang v1.1.0 - aero_v2 Proxy Client
echo ============================================
echo.

set DIR=%~dp0
set NODES=%DIR%nodes.json

if not exist "%NODES%" (
    echo [ERROR] nodes.json not found in %DIR%
    echo Please export nodes.json first.
    pause
    exit /b 1
)

echo Starting SOCKS5/HTTP proxy on 127.0.0.1:7890 ...
echo Press Ctrl+C to stop.
echo.

"%DIR%nanfang-core.exe" serve --nodes-file "%NODES%" --listen 127.0.0.1:7890

pause