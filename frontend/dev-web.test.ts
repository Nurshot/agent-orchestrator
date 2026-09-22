import { readFile } from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("dev:web", () => {
	it("uses Vite's cross-platform web mode instead of a shell environment prefix", async () => {
		const manifestPath = path.join(import.meta.dirname, "package.json");
		const manifest = JSON.parse(await readFile(manifestPath, "utf8"));

		expect(manifest.scripts["dev:web"]).toBe(
			"vite --config vite.renderer.config.ts --mode web",
		);
	});
});
