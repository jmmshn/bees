// cdp.mjs: drive the first page of the Chrome listening on <port> for the DevTools protocol.
//   node cdp.mjs <port> <js>               evaluate <js> (awaiting a promise), print the result as JSON
//   node cdp.mjs <port> --type <text>      type <text> into the focused element, a key at a time
//   node cdp.mjs <port> --shot <file.png>  save a screenshot of the page
// No dependencies: Node's built-in fetch and WebSocket.
import { writeFileSync } from "node:fs";

const [port, mode, arg] = process.argv.slice(2);
if (!port || !mode || (mode.startsWith("--") && arg === undefined)) {
	console.error("usage: node cdp.mjs <port> <js> | --type <text> | --shot <file.png>");
	process.exit(2);
}
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
let next = 0;
const pending = new Map();
ws.onmessage = (m) => {
	const msg = JSON.parse(m.data);
	pending.get(msg.id)?.(msg);
};
function call(method, params = {}) {
	const id = ++next;
	ws.send(JSON.stringify({ id, method, params }));
	return new Promise((ok) => pending.set(id, ok)).then((msg) => {
		const r = msg.result ?? {};
		if (msg.error || r.exceptionDetails) {
			console.error(JSON.stringify(msg.error ?? r.exceptionDetails));
			process.exit(1);
		}
		return r;
	});
}

if (mode === "--type") {
	for (const ch of arg) {
		await call("Input.insertText", { text: ch });
		await new Promise((ok) => setTimeout(ok, 60)); // a typist's pace, so it reads on screen
	}
} else if (mode === "--shot") {
	writeFileSync(arg, Buffer.from((await call("Page.captureScreenshot", { format: "png" })).data, "base64"));
} else {
	const r = await call("Runtime.evaluate", { expression: mode, awaitPromise: true, returnByValue: true });
	console.log(JSON.stringify(r.result?.value ?? null));
}
process.exit(0);
