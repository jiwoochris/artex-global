@echo off
rem Switch the console to UTF-8; otherwise the Korean text in this file is garbled on GBK terminals.
chcp 65001 >nul 2>&1
rem ARTEX daemon start script (Windows)
rem
rem Usage:
rem   start.bat                  run in the foreground (stop with Ctrl-C)
rem   start.bat -addr :9000      extra arguments are passed through to artex as is
rem
rem This script does exactly one thing: it runs artex.exe, and when the process exits it looks at the exit code to decide whether to relaunch it.
rem
rem   0      user stopped it normally  -> exit the loop
rem   75     program requested restart -> relaunch immediately (the user clicked "one-click update" or "rollback" in the UI)
rem   other  abnormal exit             -> relaunch after backoff (1->2->4... up to 60s)
rem
rem Downloading, SHA256 checksum verification and version swapping are not done here; artex handles all of it itself at startup
rem (the selfupdate package). The script stays simple. For details, see the explanation at the top of start.sh.

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=artex.exe"
if not exist "%BIN%" (
	echo [artex] 실행 파일을 찾을 수 없습니다: %BIN% 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [artex] 정상 종료
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
rem An update/rollback is ready. On relaunch, artex completes the version swap at startup.
	echo [artex] 재시작 요청: 새 버전 적용…
	set /a delay=1
	goto loop
)

echo [artex] 비정상 종료 ^(code=!code!^), !delay!s 후 재시작 1>&2
rem timeout fails on a redirected console, so ping is used instead (an N-second delay needs N+1 pings).
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
