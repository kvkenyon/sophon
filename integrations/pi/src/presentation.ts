export interface PresentationTheme {
	fg(color: string, text: string): string;
	bold(text: string): string;
}

export interface PresentationTui {
	terminal: { rows: number };
	requestRender(force?: boolean): void;
}

export interface PresentationClock {
	setInterval(handler: () => void, milliseconds: number): unknown;
	clearInterval(handle: unknown): void;
}

export interface SplashContext {
	workspace?: string;
	project?: string;
}

type Ink = "accent" | "dim" | "muted" | "text" | "warning";
type Cell = { character: string; ink?: Ink; bold?: boolean };

const DEFAULT_CLOCK: PresentationClock = {
	setInterval: (handler, milliseconds) => setInterval(handler, milliseconds),
	clearInterval: (handle) => clearInterval(handle as ReturnType<typeof setInterval>),
};

function safeContextValue(value: string | undefined): string | undefined {
	const safe = value?.replace(/[\x00-\x1f\x7f]/g, " ").replace(/[^\x20-\x7e]/g, "?").replace(/\s+/g, " ").trim();
	return safe ? safe.slice(0, 80) : undefined;
}

export function presentationContext(env: NodeJS.ProcessEnv, cwd: string): SplashContext {
	const parts = cwd.split(/[\\/]+/).filter(Boolean);
	return {
		workspace: safeContextValue(env.SOPHON_WORKSPACE_NAME ?? env.SOPHON_WORKSPACE ?? parts.at(-2)),
		project: safeContextValue(env.SOPHON_PROJECT_NAME ?? env.SOPHON_PROJECT ?? parts.at(-1)),
	};
}

function centered(text: string, width: number): number {
	return Math.max(0, Math.floor((width - text.length) / 2));
}

function frameGrid(width: number, height: number): Cell[][] {
	return Array.from({ length: height }, () => Array.from({ length: width }, () => ({ character: " " })));
}

function put(grid: Cell[][], x: number, y: number, text: string, ink?: Ink, bold = false): void {
	if (y < 0 || y >= grid.length) return;
	for (let offset = 0; offset < text.length; offset++) {
		const column = x + offset;
		if (column >= 0 && column < (grid[y]?.length ?? 0)) grid[y]![column] = { character: text[offset]!, ink, bold };
	}
}

function renderGrid(grid: Cell[][], theme: PresentationTheme): string[] {
	return grid.map((row) => {
		let rendered = "";
		let index = 0;
		while (index < row.length) {
			const start = index;
			const style = row[index]!;
			while (index < row.length && row[index]!.ink === style.ink && row[index]!.bold === style.bold) index++;
			let text = row.slice(start, index).map((cell) => cell.character).join("");
			if (style.bold) text = theme.bold(text);
			if (style.ink) text = theme.fg(style.ink, text);
			rendered += text;
		}
		return rendered;
	});
}

function narrowSplash(width: number, height: number, context: SplashContext, theme: PresentationTheme): string[] {
	const grid = frameGrid(width, height);
	const contextText = [context.workspace, context.project].filter(Boolean).join(" / ").slice(0, Math.max(0, width - 2));
	const middle = Math.floor(height / 2);
	const lines: Array<[string, Ink, boolean]> = [
		["SOPHON", "accent", true],
		["*   *   *", "warning", false],
		[contextText, "muted", false],
		["Enter to continue", "dim", false],
	];
	for (let i = 0; i < lines.length; i++) {
		const [text, ink, bold] = lines[i]!;
		put(grid, centered(text, width), middle - 3 + i * 2, text, ink, bold);
	}
	return renderGrid(grid, theme);
}

function orbit(tick: number, period: number, phase: number): number {
	return (Math.sin((tick / period) * Math.PI * 2 + phase) + 1) / 2;
}

function traceSophon(grid: Cell[][], tick: number, left: number, top: number, width: number, height: number): void {
	for (let age = 18; age >= 0; age--) {
		const time = tick - age;
		const nx = 0.5 + 0.27 * Math.sin(time * 0.29) + 0.18 * Math.sin(time * 0.071 + 1.4);
		const ny = 0.5 + 0.25 * Math.cos(time * 0.23 + 0.8) + 0.17 * Math.sin(time * 0.113);
		const x = left + Math.round(Math.max(0, Math.min(1, nx)) * Math.max(0, width - 1));
		const y = top + Math.round(Math.max(0, Math.min(1, ny)) * Math.max(0, height - 1));
		put(grid, x, y, age === 0 ? "o" : age < 6 ? ":" : ".", age === 0 ? "accent" : "dim", age === 0);
	}
}

export function renderSplashFrame(
	width: number,
	height: number,
	tick: number,
	context: SplashContext,
	theme: PresentationTheme,
): string[] {
	width = Math.max(1, Math.floor(width));
	height = Math.max(1, Math.floor(height));
	if (width < 48 || height < 16) return narrowSplash(width, height, context, theme);

	const grid = frameGrid(width, height);
	const fieldTop = 2;
	const fieldBottom = Math.max(fieldTop + 5, height - 7);
	const fieldHeight = fieldBottom - fieldTop;
	const left = 3;
	const fieldWidth = width - 7;

	for (let x = 2; x < width - 2; x += 5) {
		const y = fieldTop + ((x * 7 + tick) % Math.max(1, fieldHeight));
		put(grid, x, y, ".", "dim");
	}
	for (let x = 4; x < width - 4; x += 11) {
		const y = fieldTop + Math.round(fieldHeight / 2 + Math.sin((x + tick) * 0.13) * Math.max(1, fieldHeight / 4));
		put(grid, x, y, "-", "dim");
	}

	const suns = [
		{ periodX: 47, periodY: 61, phase: 0.2, glyph: "*" },
		{ periodX: 71, periodY: 43, phase: 2.1, glyph: "+" },
		{ periodX: 89, periodY: 79, phase: 4.3, glyph: "*" },
	];
	for (const sun of suns) {
		const x = left + Math.round(orbit(tick, sun.periodX, sun.phase) * fieldWidth);
		const y = fieldTop + Math.round(orbit(tick, sun.periodY, sun.phase + 0.9) * fieldHeight);
		put(grid, x, y, sun.glyph, "warning", true);
	}

	traceSophon(grid, tick, left, fieldTop, fieldWidth, fieldHeight);
	const brand = "S O P H O N";
	put(grid, centered(brand, width), height - 5, brand, "accent", true);
	const contextText = [context.workspace, context.project].filter(Boolean).join(" / ").slice(0, width - 4);
	if (contextText) put(grid, centered(contextText, width), height - 3, contextText, "muted");
	const prompt = "Enter to continue";
	put(grid, centered(prompt, width), height - 1, prompt, "dim");
	return renderGrid(grid, theme);
}

export class ThreeBodySplash {
	private tick = 0;
	private timer: unknown;
	private disposed = false;
	private readonly tui: PresentationTui;
	private readonly theme: PresentationTheme;
	private readonly context: SplashContext;
	private readonly done: () => void;
	private readonly clock: PresentationClock;

	constructor(
		tui: PresentationTui,
		theme: PresentationTheme,
		context: SplashContext,
		done: () => void,
		clock: PresentationClock = DEFAULT_CLOCK,
	) {
		this.tui = tui;
		this.theme = theme;
		this.context = context;
		this.done = done;
		this.clock = clock;
		this.timer = this.clock.setInterval(() => {
			if (this.disposed) return;
			this.tick++;
			this.tui.requestRender();
		}, 90);
		const maybeTimer = this.timer as { unref?: () => void };
		maybeTimer.unref?.();
	}

	handleInput(data: string): void {
		if (this.disposed) return;
		if (data === "\r" || data === "\n" || data === "\x1bOM") {
			this.dispose();
			this.done();
		}
	}

	render(width: number): string[] {
		return renderSplashFrame(width, this.tui.terminal.rows, this.tick, this.context, this.theme);
	}

	invalidate(): void {
		// Frames are derived from the live theme and terminal size; no cache.
	}

	dispose(): void {
		if (this.disposed) return;
		this.disposed = true;
		this.clock.clearInterval(this.timer);
	}
}

export function workingFrames(theme: PresentationTheme): string[] {
	const frames: string[] = [];
	for (let tick = 0; tick < 18; tick++) {
		const cells: Cell[] = Array.from({ length: 7 }, () => ({ character: " " }));
		const positions = [
			Math.round(orbit(tick, 17, 0.2) * 6),
			Math.round(orbit(tick, 23, 2.0) * 6),
			Math.round(orbit(tick, 29, 4.1) * 6),
		];
		for (const position of positions) cells[position] = { character: "*", ink: "warning" };
		const sophon = Math.max(0, Math.min(6, Math.round(3 + 2.4 * Math.sin(tick * 0.83) + Math.sin(tick * 0.21))));
		cells[sophon] = { character: "o", ink: "accent", bold: true };
		frames.push(renderGrid([cells], theme)[0]!);
	}
	return frames;
}
