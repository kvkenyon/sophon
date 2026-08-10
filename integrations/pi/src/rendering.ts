export interface MarkdownRenderContext {
	messageType: "user" | "assistant" | "assistant-thinking";
	isStreaming: boolean;
	availableWidth: number;
}

export interface ToolResultLike {
	content?: Array<{ type: string; text?: string }>;
}

const IMPORTANT_SOPHON_OUTPUT =
	/\b(?:attention|blocked|cannot|conflict(?:ing)?|decision|deliver(?:ed|y)|diverg(?:e|ed|ence)|error|fail(?:ed|ure)?|invalid(?:-evidence)?|mismatch|needs[- ]decision|preserv(?:e|ed|ing)|refus(?:e|ed|al)|stale|unable|warn(?:ing)?|confirmation|confirmed)\b|\"status\"\s*:\s*\"(?:attention|invalid-evidence)\"/i;

const SHELL_BOUNDARY = /(?:^|[\n;&|()]\s*)([^\n;&|()]*)/g;
const ASSIGNMENT = /^[A-Za-z_][A-Za-z0-9_]*=/;

function shellWords(segment: string): string[] {
	const words: string[] = [];
	let word = "";
	let quote: "'" | '"' | undefined;
	let escaping = false;

	for (const character of segment.trim()) {
		if (escaping) {
			word += character;
			escaping = false;
			continue;
		}
		if (character === "\\" && quote !== "'") {
			escaping = true;
			continue;
		}
		if (quote) {
			if (character === quote) quote = undefined;
			else word += character;
			continue;
		}
		if (character === "'" || character === '"') {
			quote = character;
			continue;
		}
		if (/\s/.test(character)) {
			if (word) words.push(word);
			word = "";
			continue;
		}
		word += character;
	}
	if (word) words.push(word);
	return words;
}

function executableIndex(words: string[]): number {
	let index = 0;
	if (words[index] === "command") index++;
	if (words[index] === "env") {
		index++;
		while (words[index]?.startsWith("-")) index++;
	}
	while (ASSIGNMENT.test(words[index] ?? "")) index++;
	return index;
}

export function findSophonInvocation(command: string): string[] | undefined {
	SHELL_BOUNDARY.lastIndex = 0;
	for (const match of command.matchAll(SHELL_BOUNDARY)) {
		const words = shellWords(match[1] ?? "");
		const index = executableIndex(words);
		const executable = words[index]?.replace(/^.*\//, "");
		if (executable === "sophon") return words.slice(index);
	}
	return undefined;
}

export function isSophonCommand(command: unknown): command is string {
	return typeof command === "string" && findSophonInvocation(command) !== undefined;
}

export function compactSophonLabel(command: string): string {
	const invocation = findSophonInvocation(command) ?? ["sophon"];
	const first = invocation[1]?.startsWith("-") ? undefined : invocation[1];
	const second = invocation[2]?.startsWith("-") ? undefined : invocation[2];
	return ["sophon", first, second].filter(Boolean).join(" ");
}

export function toolResultText(result: ToolResultLike): string {
	return (result.content ?? [])
		.filter((part) => part.type === "text" && typeof part.text === "string")
		.map((part) => part.text)
		.join("\n");
}

export function hasImportantSophonOutput(output: string): boolean {
	return IMPORTANT_SOPHON_OUTPUT.test(output);
}

export interface SophonRenderDecision {
	calm: boolean;
	command: unknown;
	isError?: boolean;
	isPartial?: boolean;
	expanded?: boolean;
	output?: string;
}

export function shouldCompactSophonResult(decision: SophonRenderDecision): boolean {
	return (
		decision.calm &&
		isSophonCommand(decision.command) &&
		!decision.isError &&
		!decision.isPartial &&
		!decision.expanded &&
		!hasImportantSophonOutput(decision.output ?? "")
	);
}

export function transformCalmMarkdown(markdown: string, context: MarkdownRenderContext, calm: boolean): string {
	return calm && context.messageType === "assistant-thinking" ? "" : markdown;
}
