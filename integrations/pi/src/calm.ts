import { homedir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";

export const CALM_SETTINGS_VERSION = 1;
export const CALM_SETTINGS_RELATIVE_PATH = join("pi", "presentation.json");

export interface CalmSettings {
	version: typeof CALM_SETTINGS_VERSION;
	calm: boolean;
}

export interface DataHomeResolutionOptions {
	env?: NodeJS.ProcessEnv;
	home?: () => string;
}

export function resolveSophonDataHome(options: DataHomeResolutionOptions = {}): string {
	const env = options.env ?? process.env;
	const override = env.SOPHON_DATA_HOME?.trim();
	const selected = override || join((options.home ?? homedir)(), ".sophon");
	return resolve(selected);
}

export function calmSettingsPath(dataHome: string): string {
	return join(resolve(dataHome), CALM_SETTINGS_RELATIVE_PATH);
}

export async function loadCalmSetting(path: string): Promise<boolean> {
	let raw: string;
	try {
		raw = await readFile(path, "utf8");
	} catch (error) {
		if ((error as NodeJS.ErrnoException).code === "ENOENT") return false;
		throw error;
	}

	try {
		const decoded = JSON.parse(raw) as Partial<CalmSettings> | null;
		return decoded?.version === CALM_SETTINGS_VERSION && typeof decoded.calm === "boolean" ? decoded.calm : false;
	} catch {
		return false;
	}
}

let temporarySequence = 0;

export async function saveCalmSetting(path: string, calm: boolean): Promise<void> {
	await mkdir(dirname(path), { recursive: true, mode: 0o700 });
	const temporary = `${path}.${process.pid}.${temporarySequence++}.tmp`;
	const payload = `${JSON.stringify({ version: CALM_SETTINGS_VERSION, calm } satisfies CalmSettings)}\n`;

	try {
		await writeFile(temporary, payload, { encoding: "utf8", flag: "wx", mode: 0o600 });
		await rename(temporary, path);
	} catch (error) {
		await rm(temporary, { force: true }).catch(() => undefined);
		throw error;
	}
}
