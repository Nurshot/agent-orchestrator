import { spawn } from "node:child_process";
import { constants as fsConstants } from "node:fs";
import { access, chmod, mkdtemp, mkdir, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

const MAX_AUTH_DOCUMENT_BYTES = 64 << 10;

// A macOS app launched from Finder/Dock inherits a minimal PATH
// (/usr/bin:/bin:/usr/sbin:/sbin), not the user's shell PATH, so an agent CLI
// installed by Homebrew, npm, or an install script is invisible to a bare
// spawn("claude"). Search these common install locations in addition to the
// inherited PATH before giving up. (A dev app started from a terminal already
// inherits the full PATH, which is why the login flow works there.)
function knownBinDirs(): string[] {
	const home = os.homedir();
	return [
		path.join(home, ".local", "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
		path.join(home, ".npm-global", "bin"),
		path.join(home, ".bun", "bin"),
		path.join(home, ".volta", "bin"),
		"/opt/local/bin",
		"/usr/bin",
		"/bin",
	];
}

export async function firstExecutable(name: string, dirs: readonly string[]): Promise<string | null> {
	const names = process.platform === "win32" ? [`${name}.cmd`, `${name}.exe`, name] : [name];
	const seen = new Set<string>();
	for (const dir of dirs) {
		if (!dir || seen.has(dir)) continue;
		seen.add(dir);
		for (const candidateName of names) {
			const candidate = path.join(dir, candidateName);
			try {
				await access(candidate, fsConstants.X_OK);
				return candidate;
			} catch {
				// keep looking
			}
		}
	}
	return null;
}

// Best-effort resolution through the user's login shell, covering PATHs set up
// by version managers (nvm, asdf) that neither the inherited PATH nor the static
// list above can know. Bounded by a hard timeout so a slow or prompting shell
// can never hang the login flow; any failure just falls through to "not found".
function resolveViaLoginShell(name: string): Promise<string | null> {
	if (process.platform === "win32") return Promise.resolve(null);
	const shell = process.env.SHELL || "/bin/zsh";
	return new Promise((resolve) => {
		let out = "";
		let settled = false;
		const child = spawn(shell, ["-lic", `command -v ${name} 2>/dev/null`], {
			stdio: ["ignore", "pipe", "ignore"],
		});
		const finish = (value: string | null) => {
			if (settled) return;
			settled = true;
			clearTimeout(timer);
			try {
				child.kill();
			} catch {
				// already gone
			}
			resolve(value);
		};
		const timer = setTimeout(() => finish(null), 4000);
		child.stdout.on("data", (chunk: Buffer) => {
			out += chunk.toString();
		});
		child.once("error", () => finish(null));
		child.once("exit", () => {
			const resolved = out
				.split("\n")
				.map((line) => line.trim())
				.filter(Boolean)
				.pop();
			finish(resolved && path.isAbsolute(resolved) ? resolved : null);
		});
	});
}

// Resolve an agent CLI to an absolute path and build a PATH the spawned CLI can
// use to find its own helpers (node, git). Returns null when the binary cannot
// be located anywhere, so the caller can surface an actionable error.
async function resolveProviderBinary(name: string): Promise<{ path: string; pathEnv: string } | null> {
	const inherited = (process.env.PATH ?? "").split(path.delimiter);
	let resolved = await firstExecutable(name, [...inherited, ...knownBinDirs()]);
	if (!resolved) resolved = await resolveViaLoginShell(name);
	if (!resolved) return null;
	const pathEnv = [path.dirname(resolved), ...knownBinDirs(), ...inherited]
		.filter(Boolean)
		.join(path.delimiter);
	return { path: resolved, pathEnv };
}

export interface ProviderAuthCredential {
	provider: string;
	credentialType: string;
	secret: string;
}

export interface ProviderAuthFlow {
	provider: string;
	authenticate(dataDir: string, signal?: AbortSignal): Promise<ProviderAuthCredential>;
}

const codexAuthFlow: ProviderAuthFlow = {
	provider: "codex",
	async authenticate(dataDir: string, signal?: AbortSignal): Promise<ProviderAuthCredential> {
		// mkdtemp does not create its parent. Keep this temporary, credential-bearing
		// directory within AO's data root and private even on a fresh install.
		await mkdir(dataDir, { recursive: true, mode: 0o700 });
		await chmod(dataDir, 0o700);
		const pending = await mkdtemp(path.join(dataDir, "codex-cloud-login-"));
		const codexHome = path.join(pending, "home");
		try {
			await mkdir(codexHome, { recursive: true, mode: 0o700 });
			await chmod(codexHome, 0o700);
			const binary = await resolveProviderBinary("codex");
			if (!binary) {
				throw new Error(
					'Codex is not installed or could not be found. Install the Codex CLI, or connect with the "API key" credential type instead.',
				);
			}
			await new Promise<void>((resolve, reject) => {
				const child = spawn(binary.path, ["-c", 'cli_auth_credentials_store="file"', "login"], {
					env: { ...process.env, PATH: binary.pathEnv, CODEX_HOME: codexHome },
					stdio: "ignore",
					shell: process.platform === "win32",
				});
				
				let timeout: NodeJS.Timeout;
				const cleanup = () => {
					clearTimeout(timeout);
					signal?.removeEventListener("abort", onAbort);
				};

				const onAbort = () => {
					child.kill();
					cleanup();
					reject(new Error("Login was cancelled."));
				};

				if (signal?.aborted) return onAbort();
				signal?.addEventListener("abort", onAbort);

				timeout = setTimeout(() => {
					child.kill();
					cleanup();
					reject(new Error("Login timed out after 5 minutes."));
				}, 5 * 60 * 1000);

				child.once("error", () => {
					cleanup();
					reject(new Error('Codex could not start. Connect with the "API key" credential type instead.'));
				});
				child.once("exit", (code) => {
					cleanup();
					code === 0 ? resolve() : reject(new Error("Codex sign-in did not complete."));
				});
			});
			const authFile = await readFile(path.join(codexHome, "auth.json"));
			if (authFile.byteLength === 0 || authFile.byteLength > MAX_AUTH_DOCUMENT_BYTES) {
				throw new Error("Codex did not create a valid authentication credential.");
			}
			const secret = authFile.toString("utf8");
			try {
				const document: unknown = JSON.parse(secret);
				if (typeof document !== "object" || document === null || Array.isArray(document)) throw new Error();
			} catch {
				throw new Error("Codex did not create a valid authentication credential.");
			}
			return { provider: "codex", credentialType: "auth_json", secret };
		} finally {
			await rm(pending, { recursive: true, force: true });
		}
	},
};

const claudeAuthFlow: ProviderAuthFlow = {
	provider: "claude-code",
	async authenticate(dataDir: string, signal?: AbortSignal): Promise<ProviderAuthCredential> {
		await mkdir(dataDir, { recursive: true, mode: 0o700 });
		await chmod(dataDir, 0o700);
		const pending = await mkdtemp(path.join(dataDir, "claude-cloud-login-"));
		try {
			const binary = await resolveProviderBinary("claude");
			if (!binary) {
				throw new Error(
					'Claude Code is not installed or could not be found. Install Claude Code, or connect with the "API key" credential type instead.',
				);
			}
			await new Promise<void>((resolve, reject) => {
				const child = spawn(binary.path, ["auth", "login"], {
					env: { ...process.env, PATH: binary.pathEnv, CLAUDE_CONFIG_DIR: pending },
					stdio: "ignore",
					shell: process.platform === "win32",
				});
				
				let timeout: NodeJS.Timeout;
				const cleanup = () => {
					clearTimeout(timeout);
					signal?.removeEventListener("abort", onAbort);
				};

				const onAbort = () => {
					child.kill();
					cleanup();
					reject(new Error("Login was cancelled."));
				};

				if (signal?.aborted) return onAbort();
				signal?.addEventListener("abort", onAbort);

				timeout = setTimeout(() => {
					child.kill();
					cleanup();
					reject(new Error("Login timed out after 5 minutes."));
				}, 5 * 60 * 1000);

				child.once("error", () => {
					cleanup();
					reject(new Error('Claude Code could not start. Connect with the "API key" credential type instead.'));
				});
				child.once("exit", (code) => {
					cleanup();
					code === 0 ? resolve() : reject(new Error("Claude sign-in did not complete."));
				});
			});
			
			const authFile = await readFile(path.join(pending, "settings.json"));
			if (authFile.byteLength === 0 || authFile.byteLength > MAX_AUTH_DOCUMENT_BYTES) {
				throw new Error("Claude did not create a valid authentication credential.");
			}
			const secretData = authFile.toString("utf8");
			let secret = "";
			try {
				const document = JSON.parse(secretData) as Record<string, string>;
				secret = document.primaryToken || document.oauthToken || document.token || "";
				if (!secret || typeof secret !== "string") throw new Error();
			} catch {
				throw new Error("Claude did not create a valid authentication credential or token was missing.");
			}
			return { provider: "claude-code", credentialType: "oauth_token", secret };
		} finally {
			await rm(pending, { recursive: true, force: true });
		}
	},
};

const flows = new Map<string, ProviderAuthFlow>([
	[codexAuthFlow.provider, codexAuthFlow],
	[claudeAuthFlow.provider, claudeAuthFlow],
]);

export function providerAuthFlow(provider: string): ProviderAuthFlow {
	const flow = flows.get(provider);
	if (!flow) throw new Error(`No browser authentication flow is available for ${provider}.`);
	return flow;
}
