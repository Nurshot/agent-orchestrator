import {
	bindCloudStartupAttempt,
	type CloudStartupAttempt,
} from "./cloud-startup-timing";

export type CloudSessionPreparationCommit = {
	displayName: string;
	prompt: string;
};

export type CloudSessionPreparationRegistration = {
	attempt: CloudStartupAttempt;
	cancel: (sessionId: string) => Promise<void>;
	commit: (
		sessionId: string,
		input: CloudSessionPreparationCommit,
		idempotencyKey: string,
	) => Promise<void>;
	create: (idempotencyKey: string) => Promise<string>;
};

export type CloudSessionPreparation = {
	attempt: CloudStartupAttempt;
	cancel: () => void;
	commit: (input: CloudSessionPreparationCommit) => Promise<string>;
	retainForCommit: () => void;
};

type PreparationPhase = "preparing" | "committing" | "committed" | "cancelled";

export function startCloudSessionPreparation(
	registration: CloudSessionPreparationRegistration,
): CloudSessionPreparation {
	const createKey = globalThis.crypto.randomUUID();
	const commitKey = globalThis.crypto.randomUUID();
	let phase: PreparationPhase = "preparing";
	let durableSessionId: string | undefined;
	let cleanupStarted = false;
	let ready: Promise<string> | undefined;

	const cleanup = () => {
		if (!durableSessionId || cleanupStarted) return;
		cleanupStarted = true;
		void registration.cancel(durableSessionId).catch(() => undefined);
	};

	const ensureReady = (): Promise<string> => {
		if (ready) return ready;
		ready = registration.create(createKey).then((sessionId) => {
			if (!sessionId.trim()) throw new Error("The control plane returned no session identifier.");
			durableSessionId = sessionId;
			bindCloudStartupAttempt(sessionId, registration.attempt);
			if (phase === "cancelled") cleanup();
			return sessionId;
		}).catch((error) => {
			ready = undefined;
			throw error;
		});
		void ready.catch(() => undefined);
		return ready;
	};
	void ensureReady();

	return {
		attempt: registration.attempt,
		cancel: () => {
			if (phase === "committing" || phase === "committed") return;
			phase = "cancelled";
			cleanup();
		},
		commit: async (input) => {
			if (phase === "cancelled") throw new Error("The Cloud preparation was cancelled.");
			phase = "committing";
			let sessionId: string;
			try {
				sessionId = await ensureReady();
			} catch {
				sessionId = await ensureReady();
			}
			await registration.commit(sessionId, input, commitKey);
			phase = "committed";
			return sessionId;
		},
		retainForCommit: () => {
			if (phase === "preparing") phase = "committing";
		},
	};
}
