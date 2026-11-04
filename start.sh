#!/bin/sh
# ARTEX daemon start script (Linux / macOS / Docker ENTRYPOINT)
#
# Usage:
#   ./start.sh                       Run in the foreground (Ctrl-C stops it)
#   nohup ./start.sh >artex.log 2>&1 &   Run in the background
#   ./start.sh -addr :9000           Extra arguments are passed to artex unchanged
#
# This script does one thing: it starts artex and, once the process exits, decides from the
# exit code whether to bring it back.
#
#   0      The user stopped it normally   -> end the loop
#   75     The program requested a restart -> start again immediately ("one-click update" or
#                                            "rollback" pressed in the UI)
#   other  Abnormal exit                 -> start again after a backoff (1 -> 2 -> 4 ... max 60s)
#
# Download, SHA256 checksum verification and version swapping deliberately do not happen
# here. That logic would have to exist twice (sh and bat), and it is precisely the part
# that must never be wrong: once the binary is swapped for one that does not run, this
# script faithfully keeps restarting it and the user has to log into the machine and repair
# it by hand. So verification and swapping live entirely in Go (the selfupdate package) and
# happen when artex starts up, which keeps this script simple.
set -u

cd "$(dirname "$0")" || exit 1

BIN=./artex
[ -x "$BIN" ] || { echo "[artex] executable not found: $BIN" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# Forward the stop signal to the artex process itself.
#
# This is mandatory in Docker: `docker stop` sends SIGTERM only to PID 1 (that is, this
# script) and not to the child process. Without the forward artex never sees the signal,
# cannot shut down gracefully, and is SIGKILLed 10 seconds later, cutting running work off
# mid-flight.
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

	# A signal interrupts `wait` and makes it return a value above 128. At that point the
	# child is still shutting down gracefully, so a second `wait` is needed to obtain the
	# real exit code.
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[artex] stopped"
		exit 0
	fi

	case "$code" in
		0)
			echo "[artex] exited cleanly"
			exit 0
			;;
		"$RESTART_CODE")
			# An update/rollback is staged. Starting again lets artex finish the version swap
			# on startup (see selfupdate.Bootstrap).
			echo "[artex] restart requested (applying the new version)..."
			delay=1
			;;
		*)
			echo "[artex] abnormal exit (code=$code), restarting in ${delay}s" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
