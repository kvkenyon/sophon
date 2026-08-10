import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

import { calmSettingsPath, loadCalmSetting, resolveSophonDataHome, saveCalmSetting } from "./src/calm.ts";
import { ThreeBodySplash, presentationContext, workingFrames } from "./src/presentation.ts";
import {
	compactSophonLabel,
	isSophonCommand,
	shouldCompactSophonResult,
	toolResultText,
	transformCalmMarkdown,
} from "./src/rendering.ts";

const CALM_COMPONENT = Symbol("sophon-calm-component");
const STATUS_KEY = "sophon-calm";

type RuntimeModule = typeof import("@earendil-works/pi-coding-agent") & {
	createBashToolDefinition?: (cwd: string) => any;
};
type TuiModule = typeof import("@earendil-works/pi-tui");

function markedText(Text: TuiModule["Text"], content: string): InstanceType<TuiModule["Text"]> {
	const component = new Text(content, 0, 0) as InstanceType<TuiModule["Text"]> & { [CALM_COMPONENT]?: boolean };
	component[CALM_COMPONENT] = true;
	return component;
}

function withoutCalmLastComponent(context: any): any {
	return context.lastComponent?.[CALM_COMPONENT] ? { ...context, lastComponent: undefined } : context;
}

function installBashPresentation(
	pi: ExtensionAPI,
	runtime: RuntimeModule,
	tui: TuiModule,
	isCalm: () => boolean,
): boolean {
	const createDefinition = runtime.createBashToolDefinition;
	if (typeof createDefinition !== "function" || typeof tui.Text !== "function") return false;

	let initial: any;
	try {
		initial = createDefinition(process.cwd());
	} catch {
		return false;
	}
	if (
		!initial ||
		typeof initial.execute !== "function" ||
		typeof initial.renderCall !== "function" ||
		typeof initial.renderResult !== "function" ||
		!initial.parameters
	) {
		return false;
	}

	const definitions = new Map<string, any>([[process.cwd(), initial]]);
	const definitionFor = (cwd: string): any => {
		const existing = definitions.get(cwd);
		if (existing) return existing;
		const created = createDefinition(cwd);
		if (!created || typeof created.execute !== "function") throw new Error("Pi Bash definition became unavailable");
		definitions.set(cwd, created);
		return created;
	};

	pi.registerTool({
		...initial,
		async execute(toolCallId: string, params: any, signal: AbortSignal | undefined, onUpdate: any, ctx: ExtensionContext) {
			return definitionFor(ctx.cwd).execute(toolCallId, params, signal, onUpdate, ctx);
		},
		renderCall(args: { command?: string }, theme: any, context: any) {
			const stock = definitionFor(context.cwd).renderCall(args, theme, withoutCalmLastComponent(context));
			if (!isCalm() || !isSophonCommand(args.command) || context.isError) return stock;
			return markedText(tui.Text, theme.fg("dim", compactSophonLabel(args.command!)));
		},
		renderResult(result: any, options: any, theme: any, context: any) {
			// Always let Pi's renderer observe the terminal result first. Its Bash
			// renderer owns elapsed-time timers that must be stopped on completion.
			const stock = definitionFor(context.cwd).renderResult(
				result,
				options,
				theme,
				withoutCalmLastComponent(context),
			);
			if (
				!shouldCompactSophonResult({
					calm: isCalm(),
					command: context.args?.command,
					isError: context.isError,
					isPartial: options.isPartial,
					expanded: options.expanded,
					output: toolResultText(result),
				})
			) {
				return stock;
			}
			return markedText(tui.Text, "");
		},
	} as any);
	return true;
}

export interface PresentationDependencies {
	runtime: RuntimeModule;
	tui: TuiModule;
	env?: NodeJS.ProcessEnv;
	settingsFile?: string;
	loadSetting?: (path: string) => Promise<boolean>;
	saveSetting?: (path: string, calm: boolean) => Promise<void>;
}

export async function activateSophonPiPresentation(pi: ExtensionAPI, dependencies: PresentationDependencies): Promise<void> {
	const { runtime, tui } = dependencies;
	const env = dependencies.env ?? process.env;
	const settingsFile = dependencies.settingsFile ?? calmSettingsPath(resolveSophonDataHome({ env }));
	const loadSetting = dependencies.loadSetting ?? loadCalmSetting;
	const saveSetting = dependencies.saveSetting ?? saveCalmSetting;
	let calm = false;
	let persistenceWarning: string | undefined;
	try {
		calm = await loadSetting(settingsFile);
	} catch (error) {
		persistenceWarning = error instanceof Error ? error.message : String(error);
	}

	let logicalRunActive = false;
	let presentationApplied = false;
	let splashShown = false;
	let splash: ThreeBodySplash | undefined;
	let currentContext: ExtensionContext | undefined;
	const bashPresentationAvailable = installBashPresentation(pi, runtime, tui, () => calm);

	const applyCalmPresentation = (ctx: ExtensionContext): void => {
		if (ctx.mode !== "tui") return;
		currentContext = ctx;
		if (!calm) {
			if (!presentationApplied) return;
			ctx.ui.setStatus(STATUS_KEY, undefined);
			ctx.ui.setHiddenThinkingLabel();
			ctx.ui.setWorkingIndicator();
			ctx.ui.setWorkingVisible(true);
			presentationApplied = false;
			return;
		}

		ctx.ui.setStatus(STATUS_KEY, ctx.ui.theme.fg("dim", "Calm"));
		ctx.ui.setHiddenThinkingLabel("");
		ctx.ui.setWorkingVisible(true);
		if (logicalRunActive) {
			ctx.ui.setWorkingIndicator({ frames: workingFrames(ctx.ui.theme), intervalMs: 90 });
		} else {
			ctx.ui.setWorkingIndicator();
		}
		presentationApplied = true;
	};

	pi.registerMarkdownTransformer((markdown, context) => transformCalmMarkdown(markdown, context, calm));

	pi.registerCommand("calm", {
		description: "Toggle Sophon's persistent calm presentation",
		handler: async (_args, ctx) => {
			const next = !calm;
			try {
				await saveSetting(settingsFile, next);
			} catch (error) {
				if (ctx.hasUI) {
					ctx.ui.notify(`Calm setting was not changed: ${error instanceof Error ? error.message : String(error)}`, "error");
				}
				return;
			}
			calm = next;
			applyCalmPresentation(ctx);
			if (ctx.hasUI) ctx.ui.notify(`Calm ${calm ? "on" : "off"}.`, "info");
		},
	});

	pi.on("session_start", async (event, ctx) => {
		currentContext = ctx;
		applyCalmPresentation(ctx);
		if (persistenceWarning && ctx.hasUI) {
			ctx.ui.notify(`Calm preference could not be read; using ordinary presentation: ${persistenceWarning}`, "warning");
			persistenceWarning = undefined;
		}
		if (!bashPresentationAvailable && calm && ctx.mode === "tui") {
			ctx.ui.notify("Calm tool compaction is unavailable in this Pi build; stock tool rows remain visible.", "warning");
		}

		if (env.SOPHON_PI !== "1" || splashShown || event.reason !== "startup" || ctx.mode !== "tui") return;
		splashShown = true;
		if (typeof ctx.ui.custom !== "function") return;
		try {
			await ctx.ui.custom<void>(
				(tuiInstance, theme, _keybindings, done) => {
					splash = new ThreeBodySplash(
						tuiInstance,
						theme,
						presentationContext(env, ctx.cwd),
						() => done(undefined),
					);
					return splash;
				},
				{
					overlay: true,
					overlayOptions: { width: "100%", maxHeight: "100%", anchor: "center", margin: 0 },
				},
			);
		} catch (error) {
			ctx.ui.notify(
				`Opening presentation unavailable; continuing with Pi: ${error instanceof Error ? error.message : String(error)}`,
				"warning",
			);
		} finally {
			splash?.dispose();
			splash = undefined;
		}
	});

	pi.on("agent_start", async (_event, ctx) => {
		logicalRunActive = true;
		applyCalmPresentation(ctx);
	});

	pi.on("agent_settled", async (_event, ctx) => {
		logicalRunActive = false;
		applyCalmPresentation(ctx);
	});

	pi.on("session_shutdown", async () => {
		logicalRunActive = false;
		splash?.dispose();
		splash = undefined;
		if (presentationApplied && currentContext?.mode === "tui") {
			currentContext.ui.setStatus(STATUS_KEY, undefined);
			currentContext.ui.setHiddenThinkingLabel();
			currentContext.ui.setWorkingIndicator();
			currentContext.ui.setWorkingVisible(true);
		}
		presentationApplied = false;
		currentContext = undefined;
	});
}

export default async function sophonPiPresentation(pi: ExtensionAPI): Promise<void> {
	const [runtime, tui] = (await Promise.all([
		import("@earendil-works/pi-coding-agent"),
		import("@earendil-works/pi-tui"),
	])) as [RuntimeModule, TuiModule];
	await activateSophonPiPresentation(pi, { runtime, tui });
}
