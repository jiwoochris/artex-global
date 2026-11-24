#!/bin/sh
# Container entrypoint: start as root only long enough to make the bind-mounted data
# directory writable, then drop to the unprivileged "artex" user (uid/gid 10001) and
# hand over to start.sh. Everything the agents run (bash tool, MCP servers, custom
# tools) therefore runs without root.
#
# If the container was started as a non-root user already (docker run --user / compose
# `user:`), nothing to fix up or drop: just run start.sh.
set -eu

ARTEX_UID=10001
ARTEX_GID=10001

if [ "$(id -u)" -ne 0 ]; then
	exec /app/start.sh "$@"
fi

# ./data is bind-mounted from the host. Docker creates a missing host directory as root,
# and installs from before this image ran as root left root-owned files in it. Fix the
# ownership once: only recurse when the top level is not already ours, so a normal
# start does not walk a large data directory.
for dir in /app/data /app/state; do
	[ -d "$dir" ] || continue
	if [ "$(stat -c %u "$dir")" != "$ARTEX_UID" ]; then
		echo "[entrypoint] fixing ownership of $dir for uid $ARTEX_UID" >&2
		chown -R "$ARTEX_UID:$ARTEX_GID" "$dir" \
			|| echo "[entrypoint] warning: could not chown $dir; artex may be unable to write there" >&2
	fi
done

export HOME=/home/artex
# setpriv drops root for good: new uid/gid, no supplementary groups, and no way to gain
# privileges again through setuid binaries (no_new_privs).
exec setpriv --reuid="$ARTEX_UID" --regid="$ARTEX_GID" --clear-groups --no-new-privs \
	/app/start.sh "$@"
