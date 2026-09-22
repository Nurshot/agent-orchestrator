import { describe, expect, it, vi } from "vitest";
import { createCloudCpClient } from "./client";

describe("cloud control-plane session lifecycle", () => {
	it("prepares, renews, and commits one hidden session with stable mutation keys", async () => {
		const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
			new Response(
				JSON.stringify({ session: { id: "session/1" } }),
				{ status: 200, headers: { "Content-Type": "application/json" } },
			),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.test/",
			getToken: async () => "token",
			fetchImpl: fetchMock as typeof fetch,
		});

		await client.prepareSession(
			"org/1",
			{ projectId: "project-1", harness: "codex", provider: "nodeops" },
			{ idempotencyKey: "prepare-key" },
		);
		await client.renewSessionPreparation("org/1", "session/1");
		await client.commitSessionPreparation(
			"org/1",
			"session/1",
			{ displayName: "Fix startup", prompt: "Run the checks" },
			{ idempotencyKey: "commit-key" },
		);

		expect(fetchMock.mock.calls[0]?.[0]).toBe(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/session-preparations",
		);
		expect(new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("Idempotency-Key")).toBe("prepare-key");
		expect(fetchMock.mock.calls[1]?.[0]).toBe(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/renew-preparation",
		);
		expect(fetchMock.mock.calls[2]?.[0]).toBe(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/commit-preparation",
		);
		expect(new Headers(fetchMock.mock.calls[2]?.[1]?.headers).get("Idempotency-Key")).toBe("commit-key");
	});

	it("posts explicit resume intent for one encoded session", async () => {
		const fetchMock = vi.fn(async () =>
			new Response(
				JSON.stringify({
					session: {
						id: "session/1",
						sandboxProvider: "coder",
						desiredState: "running",
						observedState: "stopped",
					},
				}),
				{ status: 202, headers: { "Content-Type": "application/json" } },
			),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.test/",
			getToken: async () => "token",
			fetchImpl: fetchMock as typeof fetch,
		});

		const response = await client.resumeSession("org/1", "session/1");

		expect(response.session.desiredState).toBe("running");
		expect(fetchMock).toHaveBeenCalledWith(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/resume",
			expect.objectContaining({ method: "POST" }),
		);
	});

	it("posts restore intent for one deleted, encoded session", async () => {
		const fetchMock = vi.fn(async () =>
			new Response(
				JSON.stringify({
					session: {
						id: "session/1",
						desiredState: "running",
					},
				}),
				{ status: 202, headers: { "Content-Type": "application/json" } },
			),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.test/",
			getToken: async () => "token",
			fetchImpl: fetchMock as typeof fetch,
		});

		const response = await client.restoreSession("org/1", "session/1");

		expect(response.session.desiredState).toBe("running");
		expect(fetchMock).toHaveBeenCalledWith(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/restore",
			expect.objectContaining({ method: "POST" }),
		);
	});
});
