import assert from "node:assert/strict";
import test from "node:test";

import { activateSophonPiPresentation } from "../index.ts";

class FakeText {
	text: string;
	paddingX: number;
	paddingY: number;
	constructor(text: string, paddingX = 0, paddingY = 0) {
		this.text = text;
		this.paddingX = paddingX;
		this.paddingY = paddingY;
	}
	setText(text: string): void {
		this.text = text;
	}
	render(): string[] {
		return [this.text];
	}
	invalidate(): void {}
}

function harness() {
	const handlers = new Map<string, Array<(event: any, ctx: any) => unknown>>();
	const commands = new Map<string, any>();
	const transformers: any[] = [];
	let tool: any;
	const pi = {
		on: (event: string, handler: (event: any, ctx: any) => unknown) => {
			handlers.set(event, [...(handlers.get(event) ?? []), handler]);
		},
		registerCommand: (name: string, command: any) => commands.set(name, command),
		registerMarkdownTransformer: (transformer: any) => transformers.push(transformer),
		registerTool: (definition: any) => {
			tool = definition;
		},
	};
	const runtime = {
		createBashToolDefinition: (_cwd: string) => ({
			name: "bash",
			label: "bash",
			description: "stock bash",
			promptSnippet: "stock snippet",
			promptGuidelines: ["stock guideline"],
			parameters: { type: "object" },
			async execute() {
				return { content: [{ type: "text", text: "executed" }] };
			},
			renderCall(args: any) {
				return new FakeText(`stock-call:${args.command}`);
			},
			renderResult(result: any) {
				return new FakeText(`stock-result:${result.content?.[0]?.text ?? ""}`);
			},
		}),
	};
	return { pi, runtime, tui: { Text: FakeText }, handlers, commands, transformers, get tool() { return tool; } };
}

function tuiContext() {
	const calls: Array<[string, unknown]> = [];
	const theme = {
		fg: (_color: string, text: string) => text,
		bold: (text: string) => text,
	};
	const ui = {
		theme,
		setStatus: (_key: string, value: unknown) => calls.push(["status", value]),
		setHiddenThinkingLabel: (value?: string) => calls.push(["thinking", value]),
		setWorkingVisible: (value: boolean) => calls.push(["visible", value]),
		setWorkingIndicator: (value?: unknown) => calls.push(["indicator", value]),
		notify: (value: string) => calls.push(["notify", value]),
	};
	return { mode: "tui", hasUI: true, cwd: "/tmp/workspace/project", ui, calls, isIdle: () => true };
}

function renderContext(command: string, isError = false) {
	return {
		cwd: "/tmp/workspace/project",
		args: { command },
		state: {},
		lastComponent: undefined,
		executionStarted: true,
		isError,
	};
}

test("/calm toggles persistent display-only rendering on and off in the current session", async () => {
	const app = harness();
	const saved: boolean[] = [];
	await activateSophonPiPresentation(app.pi as any, {
		runtime: app.runtime as any,
		tui: app.tui as any,
		env: {},
		settingsFile: "/ignored/presentation.json",
		loadSetting: async () => false,
		saveSetting: async (_path, calm) => saved.push(calm),
	});
	const ctx = tuiContext();
	await app.handlers.get("session_start")![0]!({ reason: "startup" }, ctx as any);

	const command = "SOPHON_DATA_HOME=/tmp/home sophon status mission";
	assert.match(app.tool.renderCall({ command }, {}, renderContext(command)).text, /^stock-call:/);
	assert.equal(
		app.tool.renderResult(
			{ content: [{ type: "text", text: "mission complete" }] },
			{ isPartial: false, expanded: false },
			{},
			renderContext(command),
		).text,
		"stock-result:mission complete",
	);

	await app.commands.get("calm").handler("", ctx);
	assert.deepEqual(saved, [true]);
	assert.equal(app.tool.renderCall({ command }, { fg: (_c: string, text: string) => text }, renderContext(command)).text, "sophon status mission");
	assert.equal(
		app.tool.renderResult(
			{ content: [{ type: "text", text: "mission complete" }] },
			{ isPartial: false, expanded: false },
			{},
			renderContext(command),
		).text,
		"",
	);
	assert.equal(
		app.tool.renderResult(
			{ content: [{ type: "text", text: "Command exited with code 1" }] },
			{ isPartial: false, expanded: false },
			{},
			renderContext(command, true),
		).text,
		"stock-result:Command exited with code 1",
	);
	assert.equal(
		app.tool.renderResult(
			{ content: [{ type: "text", text: "conflicting evidence refused" }] },
			{ isPartial: false, expanded: false },
			{},
			renderContext(command),
		).text,
		"stock-result:conflicting evidence refused",
	);

	assert.equal(app.transformers[0]("reasoning", { messageType: "assistant-thinking", isStreaming: false, availableWidth: 80 }), "");
	assert.equal(app.transformers[0]("final answer", { messageType: "assistant", isStreaming: false, availableWidth: 80 }), "final answer");

	await app.handlers.get("agent_start")![0]!({}, ctx as any);
	const runningIndicator = ctx.calls.filter(([name]) => name === "indicator").at(-1)?.[1] as { frames?: string[] };
	assert.equal(runningIndicator.frames?.length, 18);
	await app.handlers.get("agent_settled")![0]!({}, ctx as any);
	assert.equal(ctx.calls.filter(([name]) => name === "indicator").at(-1)?.[1], undefined);

	await app.commands.get("calm").handler("", ctx);
	assert.deepEqual(saved, [true, false]);
	assert.match(app.tool.renderCall({ command }, {}, renderContext(command)).text, /^stock-call:/);
	assert.equal(app.transformers[0]("reasoning", { messageType: "assistant-thinking", isStreaming: false, availableWidth: 80 }), "reasoning");
});

test("non-TUI startup degrades without attempting terminal presentation", async () => {
	const app = harness();
	await activateSophonPiPresentation(app.pi as any, {
		runtime: app.runtime as any,
		tui: app.tui as any,
		env: { SOPHON_PI: "1" },
		settingsFile: "/ignored/presentation.json",
		loadSetting: async () => true,
		saveSetting: async () => undefined,
	});
	const ui = new Proxy(
		{},
		{
			get: (_target, property) => {
				throw new Error(`non-TUI UI access: ${String(property)}`);
			},
		},
	);
	const ctx = { mode: "print", hasUI: false, cwd: "/tmp/workspace/project", ui };
	await app.handlers.get("session_start")![0]!({ reason: "startup" }, ctx as any);
	await app.handlers.get("agent_start")![0]!({}, ctx as any);
	await app.handlers.get("agent_settled")![0]!({}, ctx as any);
});
