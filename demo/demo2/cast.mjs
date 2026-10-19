// cast.mjs <port> <dir>: record the first page of the Chrome on <port> with CDP's
// Page.startScreencast, until SIGTERM or the browser goes away. Writes <dir>/NNNNN.jpg and
// <dir>/frames.tsv: frame file, then CDP's wall-clock timestamp in seconds.
import { appendFileSync, mkdirSync, writeFileSync } from "node:fs";

const [port, dir] = process.argv.slice(2);
if (!port || !dir) {
	console.error("usage: node cast.mjs <port> <dir>");
	process.exit(2);
}
mkdirSync(dir, { recursive: true });
writeFileSync(`${dir}/frames.tsv`, "");
const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
const page = targets.find((t) => t.type === "page");
if (!page) {
	console.error(`no page on port ${port}`);
	process.exit(1);
}
const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((ok, fail) => {
	ws.onopen = ok;
	ws.onerror = fail;
});
let id = 0, n = 0;
const send = (method, params = {}) => ws.send(JSON.stringify({ id: ++id, method, params }));
ws.onmessage = (m) => {
	const msg = JSON.parse(m.data);
	if (msg.method !== "Page.screencastFrame") return;
	const { data, metadata, sessionId } = msg.params;
	const name = `${String(++n).padStart(5, "0")}.jpg`;
	writeFileSync(`${dir}/${name}`, Buffer.from(data, "base64"));
	appendFileSync(`${dir}/frames.tsv`, `${name}\t${metadata.timestamp}\n`);
	send("Page.screencastFrameAck", { sessionId });
};
ws.onclose = () => process.exit(0);
process.on("SIGTERM", () => {
	send("Page.stopScreencast");
	setTimeout(() => process.exit(0), 300);
});
send("Page.startScreencast", { format: "jpeg", quality: 85, everyNthFrame: 1 });
