import { beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({ bind: vi.fn() }));

vi.mock("./cloud-startup-timing", () => ({
	bindCloudStartupAttempt: h.bind,
}));

import { startCloudSessionPreparation } from "./cloud-session-preparation";

const attempt = { attemptId: "attempt-1", startedAtMs: 100 };

beforeEach(() => {
	h.bind.mockReset();
});

describe("cloud session preparation", () => {
	it("starts immediately and commits the same durable session", async () => {
		const create = vi.fn().mockResolvedValue("session-1");
		const commit = vi.fn().mockResolvedValue(undefined);
		const cancel = vi.fn().mockResolvedValue(undefined);
		const preparation = startCloudSessionPreparation({ attempt, create, commit, cancel });

		expect(create).toHaveBeenCalledOnce();
		const sessionId = await preparation.commit({ displayName: "Fix startup", prompt: "Do the work" });

		expect(sessionId).toBe("session-1");
		expect(h.bind).toHaveBeenCalledWith("session-1", attempt);
		expect(commit).toHaveBeenCalledWith(
			"session-1",
			{ displayName: "Fix startup", prompt: "Do the work" },
			expect.any(String),
		);
		preparation.cancel();
		expect(cancel).not.toHaveBeenCalled();
	});

	it("reclaims a preparation cancelled before create resolves", async () => {
		let resolveCreate!: (sessionId: string) => void;
		const create = vi.fn(() => new Promise<string>((resolve) => {
			resolveCreate = resolve;
		}));
		const cancel = vi.fn().mockResolvedValue(undefined);
		const preparation = startCloudSessionPreparation({
			attempt,
			create,
			commit: vi.fn(),
			cancel,
		});

		preparation.cancel();
		resolveCreate("session-2");

		await vi.waitFor(() => expect(cancel).toHaveBeenCalledWith("session-2"));
	});

	it("retries a failed create with the same idempotency key", async () => {
		const create = vi.fn()
			.mockRejectedValueOnce(new Error("offline"))
			.mockResolvedValueOnce("session-3");
		const commit = vi.fn().mockResolvedValue(undefined);
		const preparation = startCloudSessionPreparation({
			attempt,
			create,
			commit,
			cancel: vi.fn(),
		});

		await vi.waitFor(() => expect(create).toHaveBeenCalledOnce());
		await expect(preparation.commit({ displayName: "Retry", prompt: "Continue" })).resolves.toBe("session-3");

		expect(create).toHaveBeenCalledTimes(2);
		expect(create.mock.calls[0]?.[0]).toBe(create.mock.calls[1]?.[0]);
	});
});
