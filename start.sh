#!/bin/sh
# ARTEX daemon start script (Linux / macOS / Docker ENTRYPOINT)
#
# Usage:
#   ./start.sh                       run in the foreground (stop with Ctrl-C)
#   nohup ./start.sh >artex.log 2>&1 &   keep running in the background
#   ./start.sh -addr :9000           extra arguments are passed through to artex as is
#
# This script does exactly one thing: it runs artex, and when the process exits it looks at the exit code to decide whether to relaunch it.
#
#   0      user stopped it normally   → exit the loop
#   75     program requested restart  → relaunch immediately (the user clicked "one-click update" or "rollback" in the UI)
#   other  abnormal exit              → relaunch after backoff (1→2→4… up to 60s)
#
# Downloading, SHA256 checksum verification and version swapping are deliberately not done here. That logic would have to be written
# twice, in sh and in bat, and it is precisely the part that must never be wrong. Once the binary is swapped for one that does not run,
# this script would faithfully relaunch it over and over, and the user's only option would be to log into the machine and recover by
# hand. So all verification and swapping lives in Go (the selfupdate package), artex handles it itself at startup, and the script stays simple.
set -u

cd "$(dirname "$0")" || exit 1

BIN=./artex
[ -x "$BIN" ] || { echo "[artex] 실행 파일을 찾을 수 없습니다: $BIN" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# Forward stop signals to the artex process.
#
# This is essential under Docker. docker stop sends SIGTERM only to PID 1 (i.e. this script), not to
# child processes. Without forwarding, artex never receives the signal and cannot shut down gracefully;
# after 10 seconds it is SIGKILLed and any running task is cut off midway.
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while :; do
	"$BIN" "$@" &
	child=$!

	# The signal interrupts wait, making it return a value greater than 128. The child is still in the middle of shutting down
	# gracefully at that point, so we must wait once more to get the real exit code.
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[artex] 중지되었습니다"
		exit 0
	fi

	case "$code" in
		0)
			echo "[artex] 정상 종료"
			exit 0
			;;
		"$RESTART_CODE")
			# An update/rollback is ready. On relaunch, artex completes the version swap at startup (see selfupdate.Bootstrap).
			echo "[artex] 재시작 요청(새 버전 적용)…"
			delay=1
			;;
		*)
			echo "[artex] 비정상 종료 (code=$code), ${delay}s 후 재시작" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
