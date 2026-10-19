#!/usr/bin/env bash
# review.sh <out dir> <pick>: play the keeper in plannotator's review page, which stage.sh
# opened in a headless Chrome on DevTools port 9223, while cast.mjs records it to
# <out>/browser/. Shows each study, then the one whose name contains <pick> (or, failing
# that, "zigzag": the queen may copy the studies under new names), adds the note
# "zigzag, end with a buzz" and approves with it. Appends its marks to <out>/marks.tsv.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:?usage: review.sh <out dir> <pick>} pick=${2:?usage: review.sh <out dir> <pick>}
port=9223 note="zigzag, end with a buzz"
now() { perl -MTime::HiRes=time -e 'printf "%.2f\n", time'; }
mark() { printf '%s\t%s\n' "$(now)" "$1" >>"$out/marks.tsv"; }
js() { node "$here/cdp.mjs" "$port" "$@"; }
until_() { # until_ <secs> <what> <cmd...>
	local end=$(($(date +%s) + $1)) what=$2; shift 2
	until "$@" >/dev/null 2>&1; do
		[ "$(date +%s)" -lt "$end" ] || { echo "review: timed out waiting for $what" >&2; exit 1; }
		sleep 0.5
	done
}
page() { curl -fsS "http://127.0.0.1:$port/json/list" | jq -e 'any(.[]; .type == "page" and (.url | startswith("http")))'; }
until_ 300 "plannotator's page" page
# A pointer for the video: headless Chrome draws none. glide(el) moves it onto el, then clicks.
lib='window.B=(t)=>[...document.querySelectorAll("button,[role=menuitem]")].find(b=>b.innerText.trim().startsWith(t)||b.title===t||b.ariaLabel===t);
window.studies=()=>[...document.querySelectorAll("button")].filter(b=>b.title.endsWith("STUDY.md"));
window.glide=async(el)=>{let c=document.getElementById("demo-cursor");
 if(!c){c=document.createElement("div");c.id="demo-cursor";
  c.style.cssText="position:fixed;z-index:2147483647;width:22px;height:22px;margin:-11px 0 0 -11px;border-radius:50%;background:rgba(255,214,10,.85);box-shadow:0 0 0 3px rgba(0,0,0,.5);pointer-events:none;left:600px;top:390px;transition:left .6s ease,top .6s ease";
  document.body.appendChild(c);await new Promise(r=>setTimeout(r,50));}
 const r=el.getBoundingClientRect();c.style.left=(r.left+r.width/2)+"px";c.style.top=(r.top+r.height/2)+"px";
 await new Promise(r=>setTimeout(r,750));el.click();return true};true'
ready() { [ "$(js "$lib; !!B('Approve') && studies().length > 0")" = true ]; }
until_ 60 "the Approve button" ready
node "$here/cast.mjs" "$port" "$out/browser" &
cast=$!
trap 'kill "$cast" 2>/dev/null || true' EXIT
mark browser
sleep 1.5
js "$lib; B('Continue')?.click(); B('Got it')?.click(); 0" >/dev/null # in case the warm-up missed them
n=$(js "$lib; studies().length")
for ((i = 0; i < n; i++)); do
	js "$lib; glide(studies()[$i])" >/dev/null
	sleep 2
done
js "$lib; glide(studies().find(b=>b.title.includes('$pick')) ?? studies().find(b=>b.title.includes('zigzag')) ?? studies()[0])" >/dev/null
sleep 1.5
js "$lib; glide(B('Add global comment'))" >/dev/null
sleep 0.5
js 'document.querySelector("textarea").focus(), 0' >/dev/null
js --type "$note" >/dev/null
sleep 1
js "$lib; glide(B('Add'))" >/dev/null
sleep 1
js "$lib; glide(B('More decisions'))" >/dev/null
sleep 1
js "$lib; glide(B('Approve with notes'))" >/dev/null
mark approved
gone() { ! curl -fsS "http://127.0.0.1:$port/json/list"; }
until_ 30 "the browser to close" gone
kill "$cast" 2>/dev/null || true
wait "$cast" 2>/dev/null || true
mark terminal
