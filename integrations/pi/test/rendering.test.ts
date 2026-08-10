import assert from "node:assert/strict";
import test from "node:test";

import {
	compactSophonLabel,
	findSophonInvocation,
	hasImportantSophonOutput,
	isSophonCommand,
	shouldCompactSophonResult,
	toolResultText,
	transformCalmMarkdown,
} from "../src/rendering.ts";

const thinking = { messageType: "assistant-thinking", isStreaming: false, availableWidth: 80 } as const;
const assistant = { messageType: "assistant", isStreaming: false, availableWidth: 80 } as const;
const user = { messageType: "user", isStreaming: false, availableWidth: 80 } as const;

test("Calm changes only thinking presentation and leaves prompts and final answers intact", () => {
	assert.equal(transformCalmMarkdown("routine reasoning summary", thinking, true), "");
	assert.equal(transformCalmMarkdown("final commander answer", assistant, true), "final commander answer");
	assert.equal(transformCalmMarkdown("operator prompt", user, true), "operator prompt");
	assert.equal(transformCalmMarkdown("ordinary reasoning", thinking, false), "ordinary reasoning");
});

test("display transformation does not mutate content retained by export or share", () => {
	const message = { role: "assistant", content: [{ type: "thinking", thinking: "stock export content" }] };
	const before = structuredClone(message);
	assert.equal(transformCalmMarkdown(message.content[0]!.thinking, thinking, true), "");
	assert.deepEqual(message, before);
	assert.equal(message.content[0]!.thinking, "stock export content");
});

test("Sophon invocation detection accepts pinned data homes but not mentions in arguments", () => {
	assert.deepEqual(findSophonInvocation("SOPHON_DATA_HOME='/tmp/a b' sophon status --all"), ["sophon", "status", "--all"]);
	assert.deepEqual(findSophonInvocation("echo before && env X=1 /usr/local/bin/sophon verify task"), [
		"/usr/local/bin/sophon",
		"verify",
		"task",
	]);
	assert.equal(isSophonCommand("printf 'run sophon status'"), false);
	assert.equal(compactSophonLabel("SOPHON_DATA_HOME=/tmp/home sophon review apply task --confirmed"), "sophon review apply");
});

test("routine successful Sophon results compact only in collapsed Calm rendering", () => {
	const routine = {
		calm: true,
		command: "SOPHON_DATA_HOME=/tmp/home sophon status mission-1",
		output: "mission-1 complete",
	};
	assert.equal(shouldCompactSophonResult(routine), true);
	assert.equal(shouldCompactSophonResult({ ...routine, calm: false }), false);
	assert.equal(shouldCompactSophonResult({ ...routine, expanded: true }), false);
	assert.equal(shouldCompactSophonResult({ ...routine, isPartial: true }), false);
	assert.equal(shouldCompactSophonResult({ ...routine, command: "go test ./..." }), false);
});

test("failures, conflicting evidence, warnings, decisions, and delivery confirmations stay visible", () => {
	const base = { calm: true, command: "sophon status task", output: "ok" };
	assert.equal(shouldCompactSophonResult({ ...base, isError: true }), false);
	for (const output of [
		"status: invalid-evidence",
		"conflicting evidence refused",
		"preserved work warning",
		"decision required",
		"delivery confirmed",
		'{"status":"attention"}',
	]) {
		assert.equal(hasImportantSophonOutput(output), true, output);
		assert.equal(shouldCompactSophonResult({ ...base, output }), false, output);
	}
	assert.equal(toolResultText({ content: [{ type: "text", text: "one" }, { type: "image" }, { type: "text", text: "two" }] }), "one\ntwo");
});
