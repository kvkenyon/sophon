import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";

import {
	CALM_SETTINGS_VERSION,
	calmSettingsPath,
	loadCalmSetting,
	resolveSophonDataHome,
	saveCalmSetting,
} from "../src/calm.ts";

test("resolved Sophon data home honors the override and otherwise uses ~/.sophon", () => {
	assert.equal(resolveSophonDataHome({ env: { SOPHON_DATA_HOME: " ./assigned " }, home: () => "/ignored" }), resolve("assigned"));
	assert.equal(resolveSophonDataHome({ env: {}, home: () => "/operator" }), "/operator/.sophon");
});

test("Calm persistence is versioned, reversible, and defaults off", async (t) => {
	const home = await mkdtemp(join(tmpdir(), "sophon-pi-calm-"));
	t.after(() => rm(home, { recursive: true, force: true }));
	const path = calmSettingsPath(home);

	assert.equal(await loadCalmSetting(path), false);
	await saveCalmSetting(path, true);
	assert.equal(await loadCalmSetting(path), true);
	assert.deepEqual(JSON.parse(await readFile(path, "utf8")), { version: CALM_SETTINGS_VERSION, calm: true });

	await saveCalmSetting(path, false);
	assert.equal(await loadCalmSetting(path), false);
	await writeFile(path, "not-json\n", "utf8");
	assert.equal(await loadCalmSetting(path), false);
});
