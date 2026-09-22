import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { CloudCpProviderConnection } from "./cloud-cp";

const h = vi.hoisted(() => ({
	me: vi.fn(),
	listProviderConnections: vi.fn(),
	listUserProviderConnections: vi.fn(),
	createSession: vi.fn(),
	beginCloudStartupAttempt: vi.fn(() => ({ attemptId: "attempt-orchestrator", startedAtMs: 10 })),
	bindCloudStartupAttempt: vi.fn(),
	captureRendererEvent: vi.fn(),
}));

vi.mock("../hooks/useCloudCp", () => ({
	createRendererCloudCpClient: () => ({
		me: h.me,
		listProviderConnections: h.listProviderConnections,
		listUserProviderConnections: h.listUserProviderConnections,
		createSession: h.createSession,
	}),
}));

vi.mock("../stores/sandbox-provider-store", () => ({ readSelectedSandboxProvider: () => null }));
vi.mock("./telemetry", () => ({ captureRendererEvent: h.captureRendererEvent }));
vi.mock("./cloud-startup-timing", () => ({
	beginCloudStartupAttempt: h.beginCloudStartupAttempt,
	bindCloudStartupAttempt: h.bindCloudStartupAttempt,
}));

import { selectCloudOrchestratorHarness, spawnCloudOrchestrator } from "./cloud-orchestrator";

beforeEach(() => {
	for (const mock of Object.values(h)) mock.mockReset();
	h.beginCloudStartupAttempt.mockReturnValue({ attemptId: "attempt-orchestrator", startedAtMs: 10 });
});

function connection(provider: string, validationState = "valid"): CloudCpProviderConnection {
	return {
		id: provider,
		provider,
		label: "default",
		config: {},
		validationState,
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
	};
}

describe("selectCloudOrchestratorHarness", () => {
	it("uses the connected harness when it is the only Cloud option", () => {
		expect(selectCloudOrchestratorHarness([connection("claude-code")])).toBe("claude-code");
	});

	it("preserves Codex as the preference when several supported agents are connected", () => {
		expect(selectCloudOrchestratorHarness([connection("cursor"), connection("codex")])).toBe("codex");
	});

	it("ignores invalid, non-default, and non-agent provider connections", () => {
		const invalid = connection("codex", "invalid");
		const nonDefault = { ...connection("claude-code"), label: "secondary" };
		expect(selectCloudOrchestratorHarness([invalid, nonDefault, connection("github")])).toBeUndefined();
	});
});

describe("spawnCloudOrchestrator", () => {
	it("binds the startup attempt to the created session", async () => {
		const queryClient = new QueryClient();
		queryClient.setQueryData(["settings"], { cloudControlPlaneUrl: "https://cloud.example.test" });
		h.me.mockResolvedValue({ organizations: [{ id: "org-1" }] });
		h.listProviderConnections.mockResolvedValue({ providerConnections: [connection("codex")] });
		h.listUserProviderConnections.mockResolvedValue({ providerConnections: [] });
		h.createSession.mockResolvedValue({ session: { id: "orchestrator-1" } });

		await expect(spawnCloudOrchestrator(queryClient, "project-1")).resolves.toBe("orchestrator-1");
		expect(h.beginCloudStartupAttempt).toHaveBeenCalledOnce();
		expect(h.bindCloudStartupAttempt).toHaveBeenCalledWith("orchestrator-1", {
			attemptId: "attempt-orchestrator",
			startedAtMs: 10,
		});
	});
});
