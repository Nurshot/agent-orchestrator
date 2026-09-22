import {
	bindCloudStartupAttempt,
	type CloudStartupAttempt,
} from "./cloud-startup-timing";

export type CloudSessionPreparationCommit = {
	displayName: string;
	prompt: string;
};

export type CloudSessionPreparationLease = {
	expiresAt: string;
	leaseSeconds: number;
};

export type CloudSessionPreparationRegistration = {
	attempt: CloudStartupAttempt;
	cancel: (sessionId: string) => Promise<void>;
	commit: (
		sessionId: string,
		input: CloudSessionPreparationCommit,
		idempotencyKey: string,
	) => Promise<void>;
	compatibilityKey: string;
	create: (idempotencyKey: string) => Promise<{
		lease: CloudSessionPreparationLease;
		sessionId: string;
	}>;
	renew: (sessionId: string) => Promise<CloudSessionPreparationLease>;
	scopeKey: string;
	onEvent?: (event: string, properties?: Record<string, unknown>) => void;
};

export type CloudSessionPreparation = {
	attempt: CloudStartupAttempt;
	commit: (input: CloudSessionPreparationCommit) => Promise<string>;
	invalidate: () => void;
	recordActivity: () => void;
	release: () => void;
	retainForCommit: () => void;
};

type PreparationPhase =
	| "preparing"
	| "attached"
	| "detached"
	| "committing"
	| "committed"
	| "expired"
	| "invalidated"
	| "failed";

type PreparationEntry = {
	activityDirty: boolean;
	activityTimer?: ReturnType<typeof setTimeout>;
	attachments: number;
	attempt: CloudStartupAttempt;
	cleanupStarted: boolean;
	commitKey: string;
	compatibilityKey: string;
	createKey: string;
	durableSessionId?: string;
	expiresAtMs?: number;
	expiryTimer?: ReturnType<typeof setTimeout>;
	key: string;
	lastRenewedAtMs: number;
	phase: PreparationPhase;
	ready?: Promise<string>;
	registration: CloudSessionPreparationRegistration;
	renewInFlight?: Promise<CloudSessionPreparationLease>;
	replacement?: PreparationEntry;
	scopeKey: string;
};

const ACTIVITY_RENEW_INTERVAL_MS = 45_000;
const ACTIVITY_RETRY_DELAY_MS = 5_000;
const entries = new Map<string, PreparationEntry>();

function registryKey(scopeKey: string, compatibilityKey: string): string {
	return `${scopeKey}\u0000${compatibilityKey}`;
}

function clearTimer(timer: ReturnType<typeof setTimeout> | undefined): void {
	if (timer !== undefined) clearTimeout(timer);
}

function removeEntry(entry: PreparationEntry): void {
	if (entries.get(entry.key) === entry) entries.delete(entry.key);
	clearTimer(entry.activityTimer);
	clearTimer(entry.expiryTimer);
	entry.activityTimer = undefined;
	entry.expiryTimer = undefined;
}

function emitEvent(entry: PreparationEntry, event: string, properties?: Record<string, unknown>): void {
	entry.registration.onEvent?.(event, {
		...(entry.durableSessionId ? { session_id: entry.durableSessionId } : {}),
		...properties,
	});
}

function expireEntry(entry: PreparationEntry): void {
	if (entry.phase === "committing" || entry.phase === "committed" || entry.phase === "expired" ||
		entry.phase === "invalidated") return;
	const previousPhase = entry.phase;
	entry.phase = "expired";
	removeEntry(entry);
	emitEvent(entry, "expired", { attachment_state: previousPhase });
}

function scheduleExpiry(entry: PreparationEntry): void {
	clearTimer(entry.expiryTimer);
	entry.expiryTimer = undefined;
	if (entry.expiresAtMs === undefined) return;
	entry.expiryTimer = setTimeout(
		() => expireEntry(entry),
		Math.max(0, entry.expiresAtMs - Date.now()),
	);
}

function setLease(entry: PreparationEntry, lease: CloudSessionPreparationLease): void {
	const serverExpiresAtMs = Date.parse(lease.expiresAt);
	const leaseDurationMs = lease.leaseSeconds * 1_000;
	if (!Number.isFinite(serverExpiresAtMs) || !Number.isFinite(leaseDurationMs) || leaseDurationMs <= 0) {
		throw new Error("The control plane returned an invalid preparation expiry.");
	}
	const expiresAtMs = Date.now() + leaseDurationMs;
	entry.expiresAtMs = expiresAtMs;
	entry.lastRenewedAtMs = Date.now();
	scheduleExpiry(entry);
}

function cleanupInvalidated(entry: PreparationEntry): void {
	if (!entry.durableSessionId || entry.cleanupStarted) return;
	entry.cleanupStarted = true;
	void entry.registration.cancel(entry.durableSessionId).catch(() => undefined);
}

function ensureReady(entry: PreparationEntry): Promise<string> {
	if (entry.ready) return entry.ready;
	entry.ready = entry.registration.create(entry.createKey).then(({ lease, sessionId }) => {
		if (!sessionId.trim()) throw new Error("The control plane returned no session identifier.");
		entry.durableSessionId = sessionId;
		setLease(entry, lease);
		bindCloudStartupAttempt(sessionId, entry.attempt);
		if (entry.phase === "invalidated") cleanupInvalidated(entry);
		return sessionId;
	}).catch((error) => {
		entry.ready = undefined;
		if (entry.phase !== "invalidated") entry.phase = "failed";
		throw error;
	});
	void entry.ready.catch(() => undefined);
	return entry.ready;
}

function createEntry(registration: CloudSessionPreparationRegistration): PreparationEntry {
	const key = registryKey(registration.scopeKey, registration.compatibilityKey);
	const entry: PreparationEntry = {
		activityDirty: false,
		attachments: 0,
		attempt: registration.attempt,
		cleanupStarted: false,
		commitKey: globalThis.crypto.randomUUID(),
		compatibilityKey: registration.compatibilityKey,
		createKey: globalThis.crypto.randomUUID(),
		key,
		lastRenewedAtMs: Date.now(),
		phase: "preparing",
		registration,
		scopeKey: registration.scopeKey,
	};
	entries.set(key, entry);
	emitEvent(entry, "acquired", { acquisition: "new" });
	void ensureReady(entry);
	return entry;
}

function invalidateEntry(entry: PreparationEntry): void {
	if (entry.phase === "committing" || entry.phase === "committed" || entry.phase === "invalidated") return;
	entry.phase = "invalidated";
	removeEntry(entry);
	cleanupInvalidated(entry);
}

function invalidateIncompatible(registration: CloudSessionPreparationRegistration): void {
	for (const entry of entries.values()) {
		if (entry.scopeKey === registration.scopeKey && entry.compatibilityKey !== registration.compatibilityKey) {
			invalidateEntry(entry);
		}
	}
}

function resolveEntry(entry: PreparationEntry): PreparationEntry {
	let current = entry;
	while (current.replacement) current = current.replacement;
	return current;
}

function replaceExpiredEntry(entry: PreparationEntry): PreparationEntry {
	const current = resolveEntry(entry);
	expireEntry(current);
	const existing = entries.get(current.key);
	if (existing && existing !== current && !isExpired(existing)) {
		existing.attachments += current.attachments;
		current.attachments = 0;
		current.replacement = existing;
		return existing;
	}
	const replacement = createEntry(current.registration);
	replacement.attachments = current.attachments;
	replacement.phase = replacement.attachments > 0 ? "attached" : "detached";
	current.attachments = 0;
	current.replacement = replacement;
	return replacement;
}

function isExpired(entry: PreparationEntry): boolean {
	return entry.phase === "expired" || (entry.expiresAtMs !== undefined && entry.expiresAtMs <= Date.now());
}

export function isCloudSessionPreparationExpired(error: unknown): boolean {
	return typeof error === "object" && error !== null && "code" in error &&
		(error as { code?: unknown }).code === "PREPARATION_EXPIRED";
}

async function renewEntry(entry: PreparationEntry): Promise<CloudSessionPreparationLease> {
	if (entry.renewInFlight) return entry.renewInFlight;
	emitEvent(entry, "renewal_attempted");
	entry.renewInFlight = ensureReady(entry).then((sessionId) => {
		clearTimer(entry.expiryTimer);
		entry.expiryTimer = undefined;
		return entry.registration.renew(sessionId);
	}).then((lease) => {
		setLease(entry, lease);
		emitEvent(entry, "renewal_succeeded");
		return lease;
	}).catch((error) => {
		emitEvent(entry, "renewal_failed", {
			failure_category: isCloudSessionPreparationExpired(error) ? "expired" : "transport_or_server",
		});
		if (isCloudSessionPreparationExpired(error) ||
			(entry.expiresAtMs !== undefined && entry.expiresAtMs <= Date.now())) {
			expireEntry(entry);
		} else {
			scheduleExpiry(entry);
		}
		throw error;
	}).finally(() => {
		entry.renewInFlight = undefined;
	});
	return entry.renewInFlight;
}

function scheduleActivityRenewal(entry: PreparationEntry, delay?: number): void {
	if (entry.activityTimer !== undefined || entry.attachments < 1 || !entry.activityDirty) return;
	const dueIn = delay ?? Math.max(0, entry.lastRenewedAtMs + ACTIVITY_RENEW_INTERVAL_MS - Date.now());
	entry.activityTimer = setTimeout(() => {
		entry.activityTimer = undefined;
		if (entry.attachments < 1 || !entry.activityDirty) return;
		entry.activityDirty = false;
		void renewEntry(entry).then(() => {
			if (entry.activityDirty) scheduleActivityRenewal(entry);
		}).catch((error) => {
			if (isCloudSessionPreparationExpired(error)) {
				if (entry.attachments > 0) replaceExpiredEntry(entry);
				return;
			}
			entry.activityDirty = true;
			if (!isExpired(entry)) scheduleActivityRenewal(entry, ACTIVITY_RETRY_DELAY_MS);
		});
	}, dueIn);
}

function acquireEntry(registration: CloudSessionPreparationRegistration): PreparationEntry {
	invalidateIncompatible(registration);
	const key = registryKey(registration.scopeKey, registration.compatibilityKey);
	let entry = entries.get(key);
	if (entry && isExpired(entry)) {
		expireEntry(entry);
		entry = undefined;
	}
	if (!entry) return createEntry(registration);
	entry.registration = registration;
	emitEvent(entry, "acquired", { acquisition: "reused" });
	return entry;
}

export function startCloudSessionPreparation(
	registration: CloudSessionPreparationRegistration,
): CloudSessionPreparation {
	let entry = acquireEntry(registration);
	const reused = entry.attachments === 0 && entry.durableSessionId !== undefined;
	entry.attachments += 1;
	if (entry.phase !== "preparing") entry.phase = "attached";
	let released = false;

	if (reused) {
		emitEvent(entry, "reattached");
		void renewEntry(entry).catch((error) => {
			if (isCloudSessionPreparationExpired(error)) entry = replaceExpiredEntry(entry);
		});
	}

	const currentEntry = (replaceExpired: boolean): PreparationEntry => {
		entry = resolveEntry(entry);
		if (replaceExpired && isExpired(entry)) entry = replaceExpiredEntry(entry);
		return entry;
	};

	return {
		attempt: entry.attempt,
		commit: async (input) => {
			const active = currentEntry(true);
			active.phase = "committing";
			clearTimer(active.activityTimer);
			clearTimer(active.expiryTimer);
			active.activityTimer = undefined;
			active.expiryTimer = undefined;
			let sessionId: string;
			try {
				sessionId = await ensureReady(active);
			} catch {
				sessionId = await ensureReady(active);
			}
			try {
				await active.registration.commit(sessionId, input, active.commitKey);
			} catch (error) {
				if (isCloudSessionPreparationExpired(error)) {
					active.phase = "expired";
					removeEntry(active);
					emitEvent(active, "expired", { attachment_state: "committing" });
					throw error;
				}
				await active.registration.commit(sessionId, input, active.commitKey);
			}
			active.phase = "committed";
			removeEntry(active);
			return sessionId;
		},
		invalidate: () => invalidateEntry(currentEntry(false)),
		recordActivity: () => {
			if (released) return;
			const active = currentEntry(true);
			if (active.phase === "committing" || active.phase === "committed" || active.phase === "invalidated") return;
			active.activityDirty = true;
			scheduleActivityRenewal(active);
		},
		release: () => {
			if (released) return;
			released = true;
			const active = currentEntry(false);
			active.attachments = Math.max(0, active.attachments - 1);
			if (active.attachments > 0 || active.phase === "committing" || active.phase === "committed" ||
				active.phase === "expired" || active.phase === "invalidated") return;
			active.phase = "detached";
			emitEvent(active, "detached");
			active.activityDirty = false;
			clearTimer(active.activityTimer);
			active.activityTimer = undefined;
			void renewEntry(active).catch(() => undefined);
		},
		retainForCommit: () => {
			const active = currentEntry(true);
			if (active.phase !== "committed" && active.phase !== "invalidated") active.phase = "committing";
		},
	};
}

export function resetCloudSessionPreparationRegistryForTests(): void {
	for (const entry of entries.values()) removeEntry(entry);
	entries.clear();
}
