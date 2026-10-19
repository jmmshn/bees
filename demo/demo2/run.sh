#!/usr/bin/env bash
# run.sh <out dir>: one whole take of demo 2, off screen: setup.sh, take.sh, teardown.sh, then
# edit.py. Leaves <out>/demo.gif and <out>/demo.mp4. Teardown runs even if the take fails.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:?usage: run.sh <out dir>}
mkdir -p "$out" && out=$(cd "$out" && pwd)
"$here/setup.sh" || exit 1
"$here/take.sh" "$out"
took=$?
"$here/teardown.sh" || { echo "run: teardown didn't end clean; see above" >&2; exit 1; }
[ $took = 0 ] || { echo "run: the take failed (exit $took); see $out/take.log" >&2; exit $took; }
python3 "$here/edit.py" "$out"
