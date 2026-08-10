import assert from "node:assert/strict";
import test from "node:test";

import { ThreeBodySplash, presentationContext, renderSplashFrame, workingFrames } from "../src/presentation.ts";

const theme = {
	fg: (color: string, text: string) => `\x1b[3${color.length % 8}m${text}\x1b[39m`,
	bold: (text: string) => `\x1b[1m${text}\x1b[22m`,
};
const plain = (text: string) => text.replace(/\x1b\[[0-9;]*m/g, "");

test("opening animation is responsive, animated, branded, and width safe", () => {
	const first = renderSplashFrame(96, 28, 0, { workspace: "treehouse", project: "sophon" }, theme);
	const next = renderSplashFrame(96, 28, 13, { workspace: "treehouse", project: "sophon" }, theme);
	assert.equal(first.length, 28);
	assert.notDeepEqual(first, next);
	assert.match(first.map(plain).join("\n"), /S O P H O N/);
	assert.match(first.map(plain).join("\n"), /treehouse \/ sophon/);
	for (const line of [...first, ...next]) assert.ok(plain(line).length <= 96);

	const narrowA = renderSplashFrame(31, 10, 0, { project: "a-project-with-a-long-name" }, theme);
	const narrowB = renderSplashFrame(31, 10, 999, { project: "a-project-with-a-long-name" }, theme);
	assert.deepEqual(narrowA, narrowB);
	for (const line of narrowA) assert.ok(plain(line).length <= 31);
});

test("splash handles Enter, resize, animation, and idempotent timer disposal", () => {
	let callback: (() => void) | undefined;
	let clearCount = 0;
	let unrefCount = 0;
	let renderRequests = 0;
	let doneCount = 0;
	const handle = { unref: () => unrefCount++ };
	const clock = {
		setInterval: (next: () => void, milliseconds: number) => {
			assert.equal(milliseconds, 90);
			callback = next;
			return handle;
		},
		clearInterval: (actual: unknown) => {
			assert.equal(actual, handle);
			clearCount++;
		},
	};
	const tui = { terminal: { rows: 24 }, requestRender: () => renderRequests++ };
	const splash = new ThreeBodySplash(tui, theme, { workspace: "w", project: "p" }, () => doneCount++, clock);
	assert.equal(unrefCount, 1);
	const wide = splash.render(80);
	callback?.();
	assert.equal(renderRequests, 1);
	assert.notDeepEqual(splash.render(80), wide);
	tui.terminal.rows = 12;
	const resized = splash.render(37);
	assert.equal(resized.length, 12);
	for (const line of resized) assert.ok(plain(line).length <= 37);

	splash.handleInput("x");
	assert.equal(doneCount, 0);
	splash.handleInput("\r");
	splash.handleInput("\r");
	assert.equal(doneCount, 1);
	assert.equal(clearCount, 1);
	splash.dispose();
	assert.equal(clearCount, 1);
});

test("working animation is compact, changing, and terminal-width safe", () => {
	const frames = workingFrames(theme);
	assert.equal(frames.length, 18);
	assert.ok(new Set(frames).size > 5);
	for (const frame of frames) assert.equal(plain(frame).length, 7);
});

test("workspace and project context is sanitized and safely derived", () => {
	assert.deepEqual(presentationContext({}, "/tmp/workspace/project"), { workspace: "workspace", project: "project" });
	assert.deepEqual(presentationContext({ SOPHON_WORKSPACE: "bad\nname", SOPHON_PROJECT: " safe " }, "/ignored"), {
		workspace: "bad name",
		project: "safe",
	});
});
