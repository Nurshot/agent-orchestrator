import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage, getApiBaseUrl } from "../lib/api-client";

export type AccountsManagerSnapshot =
  components["schemas"]["AccountsManagerAccountsResponse"];
export type AccountsManagerAccount =
  components["schemas"]["AccountsManagerAccountResponse"];
export const accountsManagerQueryKey = [
  "accounts-manager",
  "accounts",
] as const;

export function selectAccountsManagerSnapshot(
  current: AccountsManagerSnapshot | undefined,
  incoming: AccountsManagerSnapshot,
): AccountsManagerSnapshot {
  if (
    !current ||
    !Number.isSafeInteger(current.revision) ||
    !Number.isSafeInteger(incoming.revision)
  ) {
    return incoming;
  }
  return incoming.revision > current.revision ? incoming : current;
}

export async function fetchAccountsManager(): Promise<AccountsManagerSnapshot> {
  const { data, error } = await apiClient.GET(
    "/api/v1/accounts-manager/accounts",
  );
  if (error) throw new Error(apiErrorMessage(error));
  return data as AccountsManagerSnapshot;
}

export function useAccountsManagerQuery() {
  return useQuery({
    queryKey: accountsManagerQueryKey,
    queryFn: fetchAccountsManager,
    staleTime: Number.POSITIVE_INFINITY,
    retry: 1,
  });
}

export function useAccountsManagerEvents(): void {
  const client = useQueryClient();
  useEffect(() => {
    let closed = false;
    let stream: EventSource | null = null;
    let reconnect: number | undefined;
    const apply = (snapshot: AccountsManagerSnapshot) =>
      client.setQueryData<AccountsManagerSnapshot>(
        accountsManagerQueryKey,
        (current) => selectAccountsManagerSnapshot(current, snapshot),
      );
    const connect = async (refreshFirst: boolean) => {
      if (refreshFirst)
        try {
          apply(await fetchAccountsManager());
        } catch {
          /* keep the last safe snapshot */
        }
      if (closed) return;
      const base = getApiBaseUrl();
      if (!base) return;
      stream = new EventSource(
        `${base}/api/v1/accounts-manager/accounts/events`,
      );
      stream.addEventListener("accounts_manager", (event) => {
        try {
          apply(
            JSON.parse(
              (event as MessageEvent<string>).data,
            ) as AccountsManagerSnapshot,
          );
        } catch {
          /* ignore malformed events */
        }
      });
      stream.onerror = () => {
        stream?.close();
        stream = null;
        if (!closed)
          reconnect = window.setTimeout(() => void connect(true), 1_000);
      };
    };
    void connect(false);
    const focus = () =>
      void fetchAccountsManager()
        .then(apply)
        .catch(() => undefined);
    window.addEventListener("focus", focus);
    return () => {
      closed = true;
      stream?.close();
      if (reconnect) window.clearTimeout(reconnect);
      window.removeEventListener("focus", focus);
    };
  }, [client]);
}

export async function startAccountsManagerOAuth(
  provider: "codex" | "claude",
  mode: "callback" | "device",
) {
  const { data, error } = await apiClient.POST(
    "/api/v1/accounts-manager/oauth-sessions",
    { body: { provider, mode } },
  );
  if (error) throw new Error(apiErrorMessage(error));
  return data;
}
export async function cancelAccountsManagerOAuth(operationId: string) {
  const { error } = await apiClient.DELETE(
    "/api/v1/accounts-manager/oauth-sessions/{operationId}",
    { params: { path: { operationId } } },
  );
  if (error) throw new Error(apiErrorMessage(error));
}
export async function addAccountsManagerAPIKey(
  provider: "codex" | "claude",
  key: string,
  baseUrl?: string,
) {
  const { data, error } = await apiClient.POST(
    "/api/v1/accounts-manager/accounts/api-key",
    { body: { provider, key, ...(baseUrl ? { baseUrl } : {}) } },
  );
  if (error) throw new Error(apiErrorMessage(error));
  return data as AccountsManagerSnapshot;
}
export async function importAccountsManagerCredential(
  provider: "codex" | "claude",
  filename: string,
  credential: Record<string, unknown>,
) {
  const { data, error } = await apiClient.POST(
    "/api/v1/accounts-manager/accounts/import",
    { body: { provider, filename, credential } },
  );
  if (error) throw new Error(apiErrorMessage(error));
  return data as AccountsManagerSnapshot;
}
export async function setAccountsManagerDisabled(
  accountId: string,
  disabled: boolean,
) {
  const { data, error } = await apiClient.PATCH(
    "/api/v1/accounts-manager/accounts/{accountId}",
    { params: { path: { accountId } }, body: { disabled } },
  );
  if (error) throw new Error(apiErrorMessage(error));
  return data as AccountsManagerSnapshot;
}
export async function refreshAccountsManagerAccount(accountId: string) {
  const { data, error } = await apiClient.POST(
    "/api/v1/accounts-manager/accounts/{accountId}/refresh",
    { params: { path: { accountId } } },
  );
  if (error) throw new Error(apiErrorMessage(error));
  return data as AccountsManagerSnapshot;
}
export async function removeAccountsManagerAccount(accountId: string) {
  const { data, error } = await apiClient.DELETE(
    "/api/v1/accounts-manager/accounts/{accountId}",
    { params: { path: { accountId } } },
  );
  if (error) throw new Error(apiErrorMessage(error));
  return data as AccountsManagerSnapshot;
}
export async function fetchAccountsManagerModels(accountId: string) {
  const { data, error } = await apiClient.GET(
    "/api/v1/accounts-manager/accounts/{accountId}/models",
    { params: { path: { accountId } } },
  );
  if (error) throw new Error(apiErrorMessage(error));
  return data;
}
export async function fetchAccountsManagerQuota(accountId: string) {
  const { data, error } = await apiClient.GET(
    "/api/v1/accounts-manager/accounts/{accountId}/quota",
    { params: { path: { accountId } } },
  );
  if (error) throw new Error(apiErrorMessage(error));
  return data;
}
export async function resetAccountsManagerQuota(accountId: string) {
  const { error } = await apiClient.POST(
    "/api/v1/accounts-manager/accounts/{accountId}/quota/reset",
    { params: { path: { accountId } } },
  );
  if (error) throw new Error(apiErrorMessage(error));
}
