#!/usr/bin/env bash
# stage.sh <url>: plannotator's browser in demo 2 (setup.sh sets PLANNOTATOR_BROWSER to it).
#
# Opens <url> in a Chrome instance of its own (a separate profile), started full screen, so
# it gets a macOS Space of its own and the screen slides from the terminal's Space to it.
# Once the review server stops answering (the keeper approved or sent feedback), it quits
# that instance: its Space closes and the screen slides back to the terminal.
set -euo pipefail
url=${1:?usage: stage.sh <url>}
prof=$HOME/demo/.stage/chrome # setup.sh made $HOME/demo/.stage; teardown.sh removes it
mkdir -p "$prof"
open -na "Google Chrome" --args --user-data-dir="$prof" --kiosk "$url" \
	--no-first-run --no-default-browser-check --hide-crash-restore-bubble # --kiosk: full screen with no tabs, toolbar or sign-in chip
# plannotator waits for this script, so the watcher runs detached with its pipes closed.
nohup bash -c '
	sleep 3 # the server is up before it calls us; this is time for the page to load
	while curl -fsS -m 2 -o /dev/null "$1"; do sleep 1; done
	sleep 1 # let the page show its "done" state
	pkill -f -- "--user-data-dir=$2" || true
' stage "$url" "$prof" </dev/null >/dev/null 2>&1 &
