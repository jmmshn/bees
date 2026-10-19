#!/usr/bin/env bash
# stage.sh <url>: plannotator's browser in demo 2 (setup.sh sets PLANNOTATOR_BROWSER to it).
#
# Opens <url> in a headless Chrome, so nothing shows on the keeper's screen. take.sh records
# that page over its DevTools port (9223) and clicks through the review there. Once the
# review server stops answering (the decision is in), it quits that Chrome.
set -euo pipefail
url=${1:?usage: stage.sh <url>}
# A profile that outlives the toy, so plannotator's first-visit dialogs (setup.sh's warm-up
# dismisses them) stay dismissed.
prof=$HOME/.cache/bees-demo2/chrome
mkdir -p "$prof"
# plannotator waits for this script, so Chrome and the watcher run detached with pipes closed.
nohup "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new \
	--user-data-dir="$prof" --remote-debugging-port=9223 --window-size=1200,780 --hide-scrollbars \
	--no-first-run --no-default-browser-check "$url" </dev/null >/dev/null 2>&1 &
nohup bash -c '
	sleep 5 # the server is up before it calls us; this is time for the page to load
	while curl -fsS -m 2 -o /dev/null "$1"; do sleep 1; done
	sleep 2 # let take.sh catch the page'"'"'s "done" state
	pkill -f -- "--user-data-dir=$2" || true
' stage "$url" "$prof" </dev/null >/dev/null 2>&1 &
