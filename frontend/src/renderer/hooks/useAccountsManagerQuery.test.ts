import { describe, expect, it } from "vitest";
import type { AccountsManagerSnapshot } from "./useAccountsManagerQuery";
import { selectAccountsManagerSnapshot } from "./useAccountsManagerQuery";

function snapshot(revision: number, accountCount: number): AccountsManagerSnapshot {
  return {
    revision,
    availability: "ready",
    stale: false,
    accounts: Array.from({ length: accountCount }, (_, index) => ({
      id: `account-${index}`,
      provider: "codex",
      kind: "oauth",
      status: "active",
      disabled: false,
      unavailable: false,
      quotaSupported: false,
      cooldowns: [],
    })),
    oauthSessions: [],
  };
}

describe("selectAccountsManagerSnapshot", () => {
  it("accepts an authoritative snapshot when an unsafe int64 revision rounds to the same JavaScript number", () => {
    const current = snapshot(1_789_925_704_937_854_010, 0);
    const incoming = snapshot(1_789_925_704_937_854_011, 1);

    expect(selectAccountsManagerSnapshot(current, incoming)).toBe(incoming);
  });
});
