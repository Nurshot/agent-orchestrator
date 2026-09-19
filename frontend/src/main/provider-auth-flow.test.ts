import { chmod, mkdtemp, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { firstExecutable } from "./provider-auth-flow";

// firstExecutable is the core of the GUI-launched-app PATH fix: it must locate an
// agent CLI in an install directory that a Finder-launched app's minimal inherited
// PATH would never include, and must ignore a same-named non-executable file.
describe("firstExecutable", () => {
	const dirs: string[] = [];

	afterEach(() => {
		dirs.length = 0;
	});

	async function tempDir(): Promise<string> {
		const dir = await mkdtemp(path.join(os.tmpdir(), "ao-binresolve-"));
		dirs.push(dir);
		return dir;
	}

	it("finds an executable binary in a searched directory", async () => {
		const dir = await tempDir();
		const bin = path.join(dir, "claude");
		await writeFile(bin, "#!/bin/sh\n");
		await chmod(bin, 0o755);
		expect(await firstExecutable("claude", [dir])).toBe(bin);
	});

	it("returns null when no directory contains the binary", async () => {
		const dir = await tempDir();
		expect(await firstExecutable("claude", [dir, "/nonexistent-ao-dir"])).toBeNull();
	});

	it("skips a same-named file that is not executable", async () => {
		const dir = await tempDir();
		const notExec = path.join(dir, "claude");
		await writeFile(notExec, "not a program");
		await chmod(notExec, 0o644);
		expect(await firstExecutable("claude", [dir])).toBeNull();
	});

	it("returns the first match and tolerates empty/duplicate dirs", async () => {
		const first = await tempDir();
		const second = await tempDir();
		for (const dir of [first, second]) {
			const bin = path.join(dir, "codex");
			await writeFile(bin, "#!/bin/sh\n");
			await chmod(bin, 0o755);
		}
		expect(await firstExecutable("codex", ["", first, first, second])).toBe(path.join(first, "codex"));
	});
});
