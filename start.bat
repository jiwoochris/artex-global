@echo off
rem ============================================================================
rem  ScopeWeaver / ARTEX Windows supervisor
rem
rem  Usage:
rem    start.bat                    foreground supervisor (Ctrl+C to stop)
rem    start.bat background         detached, hidden window
rem    start.bat status             supervisor / service / health report
rem    start.bat stop               stop supervisor, the service and aux targets
rem    start.bat restart fg|bg      stop, then start (default foreground)
rem    start.bat logs               tail the service stderr log
rem    start.bat help               this text
rem
rem  Binary discovery: prefers scopeweaver.exe, falls back to artex.exe, so the
rem  same script works for a release package and for a local
rem  `go build -o artex.exe ./cmd/artex`.
rem
rem  Exit-code contract inherited from the upstream supervisor:
rem    0   clean exit          -> supervisor stops
rem    75  restart requested   -> relaunch immediately (self-update path)
rem    other / crash          -> exponential backoff, 1s .. 60s
rem
rem  Shutdown uses a stop-flag file, not a PID kill: killing the service out from
rem  under a restarting supervisor would simply be undone by the next backoff
rem  tick, so `stop` asks the loop to exit and only force-kills as a last resort.
rem
rem  Auxiliary local targets under data\fake_llm (the stub LLM on 8799 and the
rem  authorized practice target on 8801) are revived with the service and torn
rem  down with it. Both are optional: missing binaries are skipped silently.
rem
rem  NOTE: keep this file ASCII-only, CRLF-terminated and BOM-free. cmd.exe
rem  parses the script by byte offset and a BOM or bare LF breaks it.
rem ============================================================================

setlocal enabledelayedexpansion
chcp 65001 >nul 2>&1
cd /d "%~dp0"

set "PORT=8787"
set "HEALTH_URL=http://127.0.0.1:%PORT%/api/health"
set "SUPMARK=%~dp0.supervisor.running"
set "STOPFLAG=%~dp0.stop-requested"
set "SVCLOG=%~dp0artex.log"
set "ERRLOG=%~dp0artex.err.log"
set "RESTART_CODE=75"
set "MAX_DELAY=60"
set "MAX_LOG_BYTES=33554432"

rem Auxiliary local targets. AUXFAKE is the stub LLM the fake-model profile
rem points at; AUXTARGET is the local authorized practice target.
set "AUXDIR=%~dp0data\fake_llm"
set "AUXFAKE=%AUXDIR%\fake_llm.exe"
set "AUXTARGET=%AUXDIR%\target\target.exe"
set "AUXFAKE_PORT=8799"
set "AUXTARGET_PORT=8801"
set "AUXWATCHPID=%~dp0.aux-watchdog.pid"

rem %0 is never shifted: CALL :label + shift also moves %0, which silently turns
rem any later %~f0/%~dp0 into the first user argument. ARGS is built from %* minus
rem the leading action token instead.
set "ACTION=%~1"
set "ARGS=%*"
if /i "%ACTION%"=="_supervised" (
    set "ARGS="
    set "ACTION=foreground"
) else (
    for /f "tokens=1,* delims= " %%a in ("%*") do set "ARGS=%%b"
)

rem The watchdog is a separate loop: the main supervisor is blocked inside
rem the service child and only reaches its own loop boundary when that child
rem exits, so a target that dies mid-run would otherwise stay down.
if /i "%ACTION%"=="_aux_watchdog" goto :do_aux_watchdog

if /i "%ACTION%"=="help"    goto :usageif /i "%ACTION%"=="-h"      goto :usage
if /i "%ACTION%"=="--help"  goto :usage

set "BIN="
if exist "%~dp0scopeweaver.exe" set "BIN=scopeweaver.exe"
if not defined BIN if exist "%~dp0artex.exe" set "BIN=artex.exe"
if not defined BIN (
    echo [start] ERROR: neither scopeweaver.exe nor artex.exe exists in "%~dp0"
    echo [start]        build it with: go build -tags embedui -trimpath -o artex.exe ./cmd/artex
    exit /b 1
)
if exist "%~dp0scopeweaver.exe" if exist "%~dp0artex.exe" (
    echo [start] WARNING: both scopeweaver.exe and artex.exe are present; using %BIN%.
    echo [start]          Delete the stale one so the choice is unambiguous.
)

if /i "%ACTION%"=="background"  goto :do_background
if /i "%ACTION%"=="bg"         goto :do_background
if /i "%ACTION%"=="foreground" goto :do_foreground
if /i "%ACTION%"=="fg"         goto :do_foreground
if /i "%ACTION%"=="status"     goto :do_status
if /i "%ACTION%"=="stop"       goto :do_stop
if /i "%ACTION%"=="restart"    goto :do_restart
if /i "%ACTION%"=="logs"       goto :do_logs
echo [start] ERROR: unknown action "%ACTION%".
goto :usage_fail


rem ---------------------------------------------------------------------------
rem  foreground supervisor
rem ---------------------------------------------------------------------------
:do_foreground
call :preflight
if errorlevel 1 exit /b 1
> "%SUPMARK%" echo started
call :start_aux_watchdog
echo [start] supervising %BIN% on 127.0.0.1:%PORT% (foreground)
echo [start] press Ctrl+C or run "start.bat stop" to shut down
echo.
goto :supervise


:supervise
set /a delay=1

:supervise_iter
if exist "%STOPFLAG%" goto :stop_requested
call :ensure_aux
call :rotate_logs
echo [start] %DATE% %TIME% launching %BIN% ...
"%~dp0%BIN%"!ARGS! >> "%SVCLOG%" 2>> "%ERRLOG%"
set "code=!ERRORLEVEL!"
echo [start] %BIN% exited with code !code!
if "!code!"=="0" (
    echo [start] clean exit - supervisor stopping.
    goto :supervise_done
)
if "!code!"=="%RESTART_CODE%" (
    echo [start] restart requested by the service - relaunching immediately.
    goto :supervise_iter
)
echo [start] restarting in !delay!s
call :backoff !delay!
if exist "%STOPFLAG%" goto :stop_requested
goto :supervise_iter

:stop_requested
del /q "%STOPFLAG%" >nul 2>&1
echo [start] stop flag consumed - supervisor exiting.
goto :supervise_done

:supervise_done
del /q "%SUPMARK%" >nul 2>&1
call :release_port
call :stop_aux
endlocal
exit /b 0


:backoff
rem Sleep %1 seconds, but wake immediately if a stop is requested.
set "waited=0"
:backoff_one
if !waited! geq %~1 exit /b 0
if exist "%STOPFLAG%" exit /b 0
ping -n 2 127.0.0.1 >nul 2>&1
set /a waited+=1
goto :backoff_one


rem ---------------------------------------------------------------------------
rem  background: re-launch this script detached in a hidden window
rem ---------------------------------------------------------------------------
:do_background
call :preflight
if errorlevel 1 exit /b 1
if exist "%SUPMARK%" (
    echo [start] a supervisor is already running ^(marker present^).
    echo [start] run "start.bat stop" first, or "start.bat status" to inspect.
    exit /b 1
)
rem Start-Process reports failure through stderr, not a usable exit code, so the
rem marker poll below is the only reliable success signal. Capture stderr to a
rem temp file instead of discarding it, and fail fast if the spawn itself errors.
rem Start-Process cannot take a batch variable as -FilePath (it needs a literal
rem path), and passing it through %SW_BAT% into a PowerShell string is where the
rem quoting gets fragile. Write a one-line launcher script instead: no nested
rem quoting, no environment round-trip, and its stderr is captured verbatim.
set "SW_BAT=%~dp0start.bat"
set "SW_CWD=%~dp0"
set "SW_ERR=%~dp0.start-spawn.log"
set "SW_PS1=%~dp0.start-supervisor.ps1"
del /q "%SW_ERR%" >nul 2>&1
> "%SW_PS1%" echo $p = Start-Process -FilePath '%SW_BAT%' -ArgumentList '_supervised' -WorkingDirectory '%SW_CWD%' -WindowStyle Hidden
powershell -NoProfile -ExecutionPolicy Bypass -File "%SW_PS1%" >nul 2>"%SW_ERR%"
del /q "%SW_PS1%" >nul 2>&1
rem Poll for the marker: only the hidden supervisor writes it, so its presence
rem proves the detached process really reached the supervise loop.
set /a n=0
:wait_mark
if exist "%SUPMARK%" goto :bg_ok
set /a n+=1
if !n! gtr 20 (
    echo [start] ERROR: background supervisor did not claim the marker within 20s.
    if exist "%SW_ERR%" type "%SW_ERR%"
    exit /b 1
)
ping -n 2 127.0.0.1 >nul 2>&1
goto :wait_mark
:bg_ok
del /q "%SW_ERR%" >nul 2>&1
echo [start] background supervisor running (hidden window, logs in this directory).
echo [start] stop it with: start.bat stop
exit /b 0


rem ---------------------------------------------------------------------------
rem  restart
rem ---------------------------------------------------------------------------
:do_restart
rem !ARGS! is the argv minus the action token, so it may carry a leading space
rem or be empty; strip both before comparing. `restart` defaults to foreground,
rem which blocks until Ctrl+C, so the bg form must branch before the stop.
set "RMODE=!ARGS: =!"
if /i "!RMODE!"=="bg" (
    call :do_stop_quiet
    goto :do_background
)
if /i "!RMODE!"=="background" (
    call :do_stop_quiet
    goto :do_background
)
call :do_stop_quiet
goto :do_foreground


rem ---------------------------------------------------------------------------
rem  status
rem ---------------------------------------------------------------------------
:do_status
echo === ScopeWeaver status ===
if exist "%SUPMARK%" (echo   supervisor : running) else (echo   supervisor : not running)
if exist "%STOPFLAG%" (echo   stop flag : pending) else (echo   stop flag : none)
set "PORTPID="
call :port_pid
if defined PORTPID (echo   service   : listening on 127.0.0.1:%PORT% ^(PID !PORTPID!^)) else (echo   service   : stopped ^(nothing listening on %PORT%^))
set "HL="
call :health
if defined HL (echo   health    : !HL!) else (echo   health    : DOWN or unreachable)
call :aux_status
echo   binary    : %BIN%
echo   logs      : "%SVCLOG%" and "%ERRLOG%"
exit /b 0


rem ---------------------------------------------------------------------------
rem  stop
rem ---------------------------------------------------------------------------
:do_stop
rem Set the flag AND terminate the service in one pass. The supervisor only
rem tests the flag at loop boundaries (after the child returns and between
rem backoff ticks), so a long-running child would otherwise hold the loop for
rem the full timeout. Killing the child makes the supervisor observe the exit
rem at once and take the graceful path itself.
> "%STOPFLAG%" echo stop
if not exist "%SUPMARK%" (
    echo [stop] no supervisor marker; releasing the port directly.
    goto :stop_force
)
echo [stop] asking the supervisor to stop (terminating the service child) ...
set "PORTPID="
call :port_pid
if defined PORTPID taskkill /PID !PORTPID! /T /F >nul 2>&1
set /a n=0
:stop_wait
set /a n+=1
if !n! gtr 12 (
    echo [stop] supervisor did not release the marker in 12s - forcing.
    goto :stop_force
)
if not exist "%SUPMARK%" goto :stop_force
ping -n 2 127.0.0.1 >nul 2>&1
goto :stop_wait
:stop_force
call :release_port
call :stop_aux
if exist "%STOPFLAG%" del /q "%STOPFLAG%" >nul 2>&1
if exist "%SUPMARK%" del /q "%SUPMARK%" >nul 2>&1
echo [stop] done.
exit /b 0

:do_stop_quiet
> "%STOPFLAG%" echo stop
set "PORTPID="
call :port_pid
if defined PORTPID taskkill /PID !PORTPID! /T /F >nul 2>&1
set /a n=0
:stopq_wait
set /a n+=1
if !n! gtr 12 goto :stopq_force
if not exist "%SUPMARK%" goto :stopq_force
ping -n 2 127.0.0.1 >nul 2>&1
goto :stopq_wait
:stopq_force
call :release_port
call :stop_aux
if exist "%STOPFLAG%" del /q "%STOPFLAG%" >nul 2>&1
if exist "%SUPMARK%" del /q "%SUPMARK%" >nul 2>&1
exit /b 0


rem ---------------------------------------------------------------------------
rem  logs
rem ---------------------------------------------------------------------------
:do_logs
if not exist "%ERRLOG%" (
    echo [logs] no error log yet at "%ERRLOG%".
    exit /b 0
)
powershell -NoProfile -Command "Get-Content -Tail 40 -ErrorAction SilentlyContinue '%ERRLOG%'"
exit /b 0


rem ---------------------------------------------------------------------------
rem  preflight: refuse to fight an already-running service
rem ---------------------------------------------------------------------------
:preflight
set "PORTPID="
call :port_pid
if defined PORTPID (
    echo [start] port %PORT% is already bound by PID !PORTPID!.
    echo [start] only one ScopeWeaver server may own a database - run "start.bat stop" first.
    exit /b 1
)
exit /b 0


rem ---------------------------------------------------------------------------
rem  auxiliary local targets (stub LLM + practice target)
rem ---------------------------------------------------------------------------

:do_aux_watchdog
rem Revive the auxiliary targets while the service runs.
:aux_watch_loop
call :ensure_aux
if exist "%STOPFLAG%" exit /b 0
ping -n 11 127.0.0.1 >nul 2>&1
goto :aux_watch_loop


:start_aux_watchdog
rem Spawn a detached watchdog and record its PID. `stop` must terminate it
rem before killing the targets, otherwise it would immediately revive them.
if not exist "%AUXDIR%" exit /b 0
if exist "%AUXWATCHPID%" exit /b 0
set "SW_BAT=%~dp0start.bat"
set "SW_CWD=%~dp0"
set "SW_AUXPS1=%~dp0.start-watchdog.ps1"
del /q "%AUXWATCHPID%" >nul 2>&1
> "%SW_AUXPS1%" echo $p = Start-Process -FilePath '%SW_BAT%' -ArgumentList '_aux_watchdog' -WorkingDirectory '%SW_CWD%' -WindowStyle Hidden -PassThru; Set-Content -Path '%AUXWATCHPID%' -Value $p.Id -Encoding ascii
powershell -NoProfile -ExecutionPolicy Bypass -File "%SW_AUXPS1%" >nul 2>&1
del /q "%SW_AUXPS1%" >nul 2>&1
exit /b 0


:ensure_aux
rem Start each auxiliary target if its binary exists and nothing is listening
rem on its port yet. A target that is already up is left alone, so this is safe
rem to call on every supervisor iteration.
call :aux_start "%AUXFAKE%" "%AUXFAKE_PORT%" "fake_llm"
call :aux_start "%AUXTARGET%" "%AUXTARGET_PORT%" "target"
goto :eof


:aux_start
rem %1 = binary path, %2 = port, %3 = log label
if not exist "%~1" goto :eof
set "AUXPID="
call :port_pid_of %~2
if defined AUXPID goto :eof
if not exist "%AUXDIR%" goto :eof
start "" /b "%~1" >> "%AUXDIR%\%~3.out.log" 2>> "%AUXDIR%\%~3.err.log"
goto :eof


:stop_aux
rem Terminate the watchdog first: it revives the targets, so it must be
rem gone before they are killed or they would come straight back.
if exist "%AUXWATCHPID%" (
    set "WATCHPID="
    for /f "usebackq delims=" %%p in ("%AUXWATCHPID%") do set "WATCHPID=%%p"
    if defined WATCHPID taskkill /PID !WATCHPID! /T /F >nul 2>&1
    del /q "%AUXWATCHPID%" >nul 2>&1
)
rem Terminate the auxiliary targets by port so we never depend on remembering
rem a PID from a previous run.
call :kill_port %AUXFAKE_PORT%
call :kill_port %AUXTARGET_PORT%
goto :eof


:aux_status
set "AUXPID="
call :port_pid_of %AUXFAKE_PORT%
if defined AUXPID (echo   stub LLM  : listening on 127.0.0.1:%AUXFAKE_PORT% ^(PID !AUXPID!^)) else (echo   stub LLM  : not running)
set "AUXPID="
call :port_pid_of %AUXTARGET_PORT%
if defined AUXPID (echo   target    : listening on 127.0.0.1:%AUXTARGET_PORT% ^(PID !AUXPID!^)) else (echo   target    : not running)
goto :eof


rem ---------------------------------------------------------------------------
rem  helpers
rem ---------------------------------------------------------------------------

:collect_args
rem Accumulate the remaining argv into !ARGS!, quoted. Deliberately uses
rem :ARGS instead of shift: shift inside a CALLed subroutine also moves %0, which
rem would corrupt any later %~f0/%~dp0 reference (it silently resolves to the
rem first user argument instead of this script).
set "ARGS="
for %%a in (%*) do set "ARGS=!ARGS! "%%~a""
goto :eof


:port_pid
rem Echo the PID listening on %PORT%, or nothing.
set "PORTPID="
for /f "tokens=5" %%a in ('netstat -ano -p tcp ^| findstr /r /c:":%PORT% .*LISTENING"') do set "PORTPID=%%a"
goto :eof


:port_pid_of
rem Echo the PID listening on the port given as %1, or nothing.
set "AUXPID="
for /f "tokens=5" %%a in ('netstat -ano -p tcp ^| findstr /r /c:":%~1 .*LISTENING"') do set "AUXPID=%%a"
goto :eof


:kill_port
rem Terminate whatever listens on the port given as %1.
set "AUXPID="
call :port_pid_of %~1
if not defined AUXPID exit /b 0
taskkill /PID !AUXPID! /T /F >nul 2>&1
exit /b 0


:health
rem Echo the /api/health body when the API answers, or nothing.
set "HL="
for /f "usebackq delims=" %%h in (`powershell -NoProfile -Command "try{ (Invoke-WebRequest -UseBasicParsing -TimeoutSec 3 '%HEALTH_URL%').Content }catch{ 'DOWN' }"`) do set "HL=%%h"
if "!HL!"=="DOWN" set "HL="
goto :eof


:release_port
rem Terminate whatever still owns %PORT%, then wait for the bind to clear.
call :port_pid
if not defined PORTPID exit /b 0
echo [stop] terminating PID !PORTPID! on port %PORT% ...
taskkill /PID !PORTPID! /T /F >nul 2>&1
set /a n=0
:rel_one
call :port_pid
if not defined PORTPID exit /b 0
set /a n+=1
if !n! gtr 10 (
    echo [stop] WARNING: port %PORT% is still bound after a force kill.
    exit /b 0
)
ping -n 2 127.0.0.1 >nul 2>&1
goto :rel_one


:rotate_logs
rem Keep one previous generation; unbounded SSE debug output can fill a disk.
if exist "%SVCLOG%" for %%A in ("%SVCLOG%") do if %%~zA GTR %MAX_LOG_BYTES% move /y "%SVCLOG%" "%SVCLOG%.1" >nul 2>&1
if exist "%ERRLOG%" for %%A in ("%ERRLOG%") do if %%~zA GTR %MAX_LOG_BYTES% move /y "%ERRLOG%" "%ERRLOG%.1" >nul 2>&1
goto :eof


:usage
echo.
echo ScopeWeaver / ARTEX supervisor
echo.
echo   start.bat                    foreground supervisor
echo   start.bat background         detached, hidden window
echo   start.bat status             supervisor / service / health report
echo   start.bat stop               stop supervisor, the service and aux targets
echo   start.bat restart fg         stop, then start in the foreground
echo   start.bat restart bg         stop, then start in the background
echo   start.bat logs               tail the stderr log
echo   start.bat help               this text
echo.
echo Console: http://127.0.0.1:%PORT%/
exit /b 0

:usage_fail
exit /b 2
