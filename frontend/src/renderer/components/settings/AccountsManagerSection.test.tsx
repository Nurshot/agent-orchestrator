import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AccountsManagerSection } from "./AccountsManagerSection";

const mocks = vi.hoisted(() => ({
  openExternal: vi.fn(),
  startOAuth: vi.fn(),
  cancelOAuth: vi.fn(),
  addKey: vi.fn(),
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
      data: {
        revision: 1,
        availability: "ready",
        stale: false,
        accounts: [],
        oauthSessions: [],
      },
      isLoading: false,
    }),
    startAccountsManagerOAuth: mocks.startOAuth,
    cancelAccountsManagerOAuth: mocks.cancelOAuth,
    addAccountsManagerAPIKey: mocks.addKey,
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
    fireEvent.click(
      screen.getByRole("button", { name: "Continue in browser" }),
    );
    await waitFor(() => expect(mocks.cancelOAuth).toHaveBeenCalledTimes(1));
    expect(mocks.cancelOAuth).toHaveBeenCalledWith("safe-operation");
    expect(
      await screen.findByText("Could not start browser sign-in. Try again."),
    ).toBeInTheDocument();
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
});
