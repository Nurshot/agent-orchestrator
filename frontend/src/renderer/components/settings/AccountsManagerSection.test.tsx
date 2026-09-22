import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AccountsManagerSection } from "./AccountsManagerSection";

const mocks = vi.hoisted(() => ({
  openExternal: vi.fn(),
  startOAuth: vi.fn(),
  cancelOAuth: vi.fn(),
  addKey: vi.fn(),
  updateRouting: vi.fn(),
  snapshot: {
    revision: 1,
    availability: "ready",
    stale: false,
    accounts: [],
    oauthSessions: [],
    routing: [],
  } as Record<string, unknown>,
}));

vi.mock("../../lib/bridge", () => ({
  aoBridge: { app: { openExternal: mocks.openExternal } },
}));
vi.mock("../../hooks/useAccountsManagerQuery", async () => {
  const actual = await vi.importActual<
    typeof import("../../hooks/useAccountsManagerQuery")
  >("../../hooks/useAccountsManagerQuery");
  return {
    ...actual,
    useAccountsManagerEvents: () => undefined,
    useAccountsManagerQuery: () => ({
      data: mocks.snapshot,
      isLoading: false,
    }),
    startAccountsManagerOAuth: mocks.startOAuth,
    cancelAccountsManagerOAuth: mocks.cancelOAuth,
    addAccountsManagerAPIKey: mocks.addKey,
    updateAccountsManagerRouting: mocks.updateRouting,
  };
});

function renderSection() {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <AccountsManagerSection />
    </QueryClientProvider>,
  );
}

describe("AccountsManagerSection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.cancelOAuth.mockResolvedValue(undefined);
    mocks.snapshot = {
      revision: 1,
      availability: "ready",
      stale: false,
      accounts: [],
      oauthSessions: [],
      routing: [],
    };
  });

  it("cancels the OAuth operation if the browser cannot be opened", async () => {
    mocks.startOAuth.mockResolvedValue({
      id: "safe-operation",
      authorizationUrl: "https://auth.example.test",
      provider: "codex",
      status: "pending",
    });
    mocks.openExternal.mockRejectedValue(new Error("no browser"));
    renderSection();
    fireEvent.click(screen.getByRole("button", { name: "Add codex account" }));
    fireEvent.click(screen.getByRole("button", { name: "Browser sign-in" }));
    fireEvent.click(
      screen.getByRole("button", { name: "Continue in browser" }),
    );
    await waitFor(() => expect(mocks.cancelOAuth).toHaveBeenCalledTimes(1));
    expect(mocks.cancelOAuth).toHaveBeenCalledWith("safe-operation");
    expect(
      await screen.findByText("Could not start sign-in. Try again."),
    ).toBeInTheDocument();
    expect(mocks.startOAuth).toHaveBeenCalledWith("codex", "callback");
  });

  it("starts Codex device sign-in by default", async () => {
    mocks.startOAuth.mockResolvedValue({
      id: "safe-operation",
      authorizationUrl: "https://auth.openai.com/codex/device",
      userCode: "ABCD-EFGH",
      provider: "codex",
      mode: "device",
      status: "pending",
    });
    mocks.openExternal.mockResolvedValue(undefined);
    renderSection();
    fireEvent.click(screen.getByRole("button", { name: "Add codex account" }));
    fireEvent.click(
      screen.getByRole("button", { name: "Continue with device code" }),
    );
    await waitFor(() =>
      expect(mocks.startOAuth).toHaveBeenCalledWith("codex", "device"),
    );
    expect(mocks.openExternal).toHaveBeenCalledWith(
      "https://auth.openai.com/codex/device",
    );
  });

  it("shows the pending device code inline", () => {
    mocks.snapshot = {
      revision: 2,
      availability: "ready",
      stale: false,
      accounts: [],
      oauthSessions: [
        {
          id: "safe-operation",
          provider: "codex",
          mode: "device",
          status: "pending",
          authorizationUrl: "https://auth.openai.com/codex/device",
          userCode: "ABCD-EFGH",
          expiresAt: "2026-09-20T12:00:00Z",
        },
      ],
    };
    renderSection();
    expect(screen.getByText("ABCD-EFGH")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy" })).toBeInTheDocument();
  });

  it("clears API key input after a failed submission", async () => {
    mocks.addKey.mockRejectedValue(new Error("rejected"));
    renderSection();
    fireEvent.click(screen.getByRole("button", { name: "Add codex account" }));
    fireEvent.click(screen.getByRole("button", { name: "API key" }));
    const input = screen.getByLabelText("API key") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "secret-value" } });
    fireEvent.click(screen.getByRole("button", { name: "Add account" }));
    await waitFor(() => expect(mocks.addKey).toHaveBeenCalled());
    expect(input.value).toBe("");
  });

  it("enables routing with eligible accounts in displayed order", async () => {
    mocks.snapshot = {
      revision: 3,
      availability: "ready",
      stale: false,
      accounts: [
        {
          id: "account-a",
          provider: "codex",
          kind: "oauth",
          email: "a@example.com",
          status: "active",
          disabled: false,
          unavailable: false,
          quotaSupported: false,
          cooldowns: [],
        },
        {
          id: "account-b",
          provider: "codex",
          kind: "oauth",
          email: "b@example.com",
          status: "active",
          disabled: false,
          unavailable: false,
          quotaSupported: false,
          cooldowns: [],
        },
      ],
      oauthSessions: [],
      routing: [{ provider: "codex", enabled: false, accountIds: [] }],
    };
    mocks.updateRouting.mockResolvedValue({
      ...mocks.snapshot,
      revision: 4,
      routing: [
        {
          provider: "codex",
          enabled: true,
          accountIds: ["account-a", "account-b"],
        },
      ],
    });
    renderSection();
    fireEvent.click(
      screen.getByRole("switch", {
        name: "Route new codex sessions through Accounts Manager",
      }),
    );
    await waitFor(() =>
      expect(mocks.updateRouting).toHaveBeenCalledWith("codex", true, [
        "account-a",
        "account-b",
      ]),
    );
    expect(
      screen.getAllByText(
        "Changes apply to new sessions. Existing sessions keep their selected account.",
      ),
    ).toHaveLength(2);
    expect(
      screen.getByText("Codex Chat continues to use the native device account."),
    ).toBeInTheDocument();
  });

  it("replaces an unusable saved selection when routing is enabled", async () => {
    mocks.snapshot = {
      revision: 5,
      availability: "ready",
      stale: false,
      accounts: [
        {
          id: "disabled-account",
          provider: "claude",
          kind: "oauth",
          status: "disabled",
          disabled: true,
          unavailable: false,
          quotaSupported: false,
          cooldowns: [],
        },
        {
          id: "ready-account",
          provider: "claude",
          kind: "oauth",
          status: "active",
          disabled: false,
          unavailable: false,
          quotaSupported: false,
          cooldowns: [],
        },
      ],
      oauthSessions: [],
      routing: [
        {
          provider: "claude",
          enabled: false,
          accountIds: ["disabled-account"],
        },
      ],
    };
    mocks.updateRouting.mockResolvedValue({ ...mocks.snapshot, revision: 6 });

    renderSection();
    fireEvent.click(
      screen.getByRole("switch", {
        name: "Route new claude sessions through Accounts Manager",
      }),
    );

    await waitFor(() =>
      expect(mocks.updateRouting).toHaveBeenCalledWith("claude", true, [
        "ready-account",
      ]),
    );
  });
});
