@echo off
rem Switch the console to UTF-8, otherwise the program's non-ASCII output is mangled in a
rem non-UTF-8 terminal (e.g. GBK).
chcp 65001 >nul 2>&1
rem ARTEX daemon start script (Windows)
rem
rem Usage:
rem   start.bat                  Run in the foreground (Ctrl-C stops it)
rem   start.bat -addr :9000      Extra arguments are passed to artex unchanged
rem
rem This script does one thing: it starts artex.exe and, once the process exits, decides
rem from the exit code whether to bring it back.
rem
rem   0      The user stopped it normally -> end the loop
rem   75     The program requested a restart -> start again immediately ("one-click update"
rem          or "rollback" pressed in the UI)
rem   other  Abnormal exit -> start again after a backoff (1->2->4 ... max 60s)
rem
rem Downloading, SHA256 checksum verification and version swapping do not happen here; artex
rem handles all of that itself on startup (the selfupdate package). The script stays simple.
rem See the header of start.sh for the details.

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=artex.exe"
if not exist "%BIN%" (
	echo [artex] executable not found: %BIN% 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [artex] exited cleanly
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem An update/rollback is staged; starting again lets artex finish the version swap on startup.
	echo [artex] restart requested: applying the new version...
	set /a delay=1
	goto loop
)

echo [artex] abnormal exit ^(code=!code!^), restarting in !delay!s 1>&2
rem timeout fails on a redirected console, so ping stands in for it (an N-second delay needs N+1 pings).
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
