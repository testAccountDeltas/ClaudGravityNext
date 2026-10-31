@echo off
setlocal
cd /d "%~dp0"

set "BIN="
for %%F in (gateway.exe claudgravitynext-windows-amd64.exe) do if exist "%%F" if not defined BIN set "BIN=%%F"
if not defined BIN (
    echo No gateway binary found. Build one with: go build -o gateway.exe ./cmd/gateway
    exit /b 1
)
if not exist config.json (
    echo config.json is missing. Copy config.example.json to config.json first.
    exit /b 1
)

tasklist /FI "IMAGENAME eq %BIN%" 2>NUL | find /I "%BIN%" >NUL
if %ERRORLEVEL%==0 (
    echo Stopping the running %BIN%...
    taskkill /IM %BIN% /F >NUL 2>&1
    timeout /t 1 /nobreak >NUL
)

echo Starting ClaudGravityNext...
start "ClaudGravityNext" "%BIN%" -config config.json -auths auths -ca-dir ca
endlocal
