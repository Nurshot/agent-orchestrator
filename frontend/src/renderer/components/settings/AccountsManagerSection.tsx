import {
  ChevronDown,
  ChevronRight,
  LoaderCircle,
  Plus,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { useEffect, useRef, useState, type RefObject } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { aoBridge } from "../../lib/bridge";
import {
  accountsManagerQueryKey,
  addAccountsManagerAPIKey,
  cancelAccountsManagerOAuth,
  fetchAccountsManagerModels,
  fetchAccountsManagerQuota,
  importAccountsManagerCredential,
  refreshAccountsManagerAccount,
  removeAccountsManagerAccount,
  resetAccountsManagerQuota,
  setAccountsManagerDisabled,
  startAccountsManagerOAuth,
  useAccountsManagerEvents,
  useAccountsManagerQuery,
  type AccountsManagerAccount,
  type AccountsManagerSnapshot,
} from "../../hooks/useAccountsManagerQuery";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { AgentProviderGroup } from "./AgentProviderGroup";
import { SettingsSection } from "./SettingsSection";

type Provider = "codex" | "claude";
type AddMethod = "device" | "browser" | "api-key" | "json";

export function AccountsManagerSection({
  titleHidden,
}: {
  titleHidden?: boolean;
}) {
  const query = useAccountsManagerQuery();
  useAccountsManagerEvents();
  const client = useQueryClient();
  const [expanded, setExpanded] = useState<Record<Provider, boolean>>({
    codex: true,
    claude: true,
  });
  const [adding, setAdding] = useState<Provider | null>(null);
  const [method, setMethod] = useState<AddMethod>("device");
  const [secret, setSecret] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const cancelledOAuth = useRef(new Set<string>());
  const activeOAuthID = useRef<string | null>(null);
  const data = query.data;
  const update = (next: AccountsManagerSnapshot) =>
    client.setQueryData(accountsManagerQueryKey, next);
  const clearSensitive = () => {
    setSecret("");
    if (fileRef.current) fileRef.current.value = "";
  };
  const cancelOAuthOnce = async (id: string) => {
    if (cancelledOAuth.current.has(id)) return;
    cancelledOAuth.current.add(id);
    await cancelAccountsManagerOAuth(id);
  };

  const startOAuth = async (
    provider: Provider,
    mode: "device" | "callback",
  ) => {
    setBusy(true);
    setError(null);
    try {
      const session = await startAccountsManagerOAuth(provider, mode);
      activeOAuthID.current = session.id;
      if (!session.authorizationUrl) {
        throw new Error("Missing authorization URL");
      }
      try {
        await aoBridge.app.openExternal(session.authorizationUrl);
      } catch (openError) {
        await cancelOAuthOnce(session.id).catch(() => undefined);
        throw openError;
      }
    } catch {
      setError("Could not start sign-in. Try again.");
    } finally {
      setBusy(false);
    }
  };
  const submitKey = async (provider: Provider) => {
    setBusy(true);
    setError(null);
    try {
      update(
        await addAccountsManagerAPIKey(provider, secret, baseURL || undefined),
      );
      setAdding(null);
    } catch {
      setError("Could not add this API key.");
    } finally {
      clearSensitive();
      setBusy(false);
    }
  };
  const submitFile = async (provider: Provider, file?: File) => {
    if (!file) return;
    setBusy(true);
    setError(null);
    try {
      if (file.size > 1_048_576 || !file.name.toLowerCase().endsWith(".json"))
        throw new Error("invalid");
      const parsed: unknown = JSON.parse(await file.text());
      if (!parsed || Array.isArray(parsed) || typeof parsed !== "object")
        throw new Error("invalid");
      update(
        await importAccountsManagerCredential(
          provider,
          file.name,
          parsed as Record<string, unknown>,
        ),
      );
      setAdding(null);
    } catch {
      setError("Choose a valid credential JSON file up to 1 MiB.");
    } finally {
      clearSensitive();
      setBusy(false);
    }
  };

  const waiting = data?.oauthSessions.find(
    (session) => session.status === "pending",
  );
  useEffect(() => {
    if (waiting) {
      activeOAuthID.current ??= waiting.id;
      setAdding(waiting.provider as Provider);
    }
  }, [waiting?.id]);
  useEffect(() => {
    const active = data?.oauthSessions.find(
      (session) => session.id === activeOAuthID.current,
    );
    if (active?.status === "completed") {
      activeOAuthID.current = null;
      setAdding(null);
      setError(null);
    }
    if (active?.status === "failed" || active?.status === "expired") {
      activeOAuthID.current = null;
      setError(
        active.status === "expired"
          ? "Sign-in expired. Try again."
          : "Sign-in failed. Try again.",
      );
    }
  }, [data?.revision]);
  const closeAdd = async (provider: Provider) => {
    const pending = data?.oauthSessions.find(
      (session) =>
        session.provider === provider && session.status === "pending",
    );
    if (pending) await cancelOAuthOnce(pending.id).catch(() => undefined);
    setAdding(null);
    setError(null);
    clearSensitive();
  };

  return (
    <SettingsSection title="Accounts" titleHidden={titleHidden}>
      <div className="space-y-4">
        {data?.stale ? (
          <p className="text-xs text-muted-foreground">
            Accounts Manager is temporarily unavailable. Showing the last
            update.
          </p>
        ) : null}
        {(["codex", "claude"] as const).map((provider) => {
          const accounts =
            data?.accounts.filter((account) => account.provider === provider) ??
            [];
          const unavailable = !data || data.availability !== "ready";
          return (
            <AgentProviderGroup
              key={provider}
              provider={provider}
              name={provider === "codex" ? "Codex" : "Claude"}
              summary={`${accounts.length} saved account${accounts.length === 1 ? "" : "s"}`}
              expanded={expanded[provider]}
              onExpandedChange={(value) =>
                setExpanded((current) => ({ ...current, [provider]: value }))
              }
              action={
                <Button
                  size="icon"
                  variant="ghost"
                  disabled={unavailable}
                  aria-label={`Add ${provider} account`}
                  onClick={() => {
                    setAdding(provider);
                    setMethod(provider === "codex" ? "device" : "browser");
                    setError(null);
                  }}
                >
                  <Plus className="size-4" />
                </Button>
              }
            >
              {adding === provider ? (
                <AddAccountPanel
                  provider={provider}
                  method={method}
                  setMethod={(next) => {
                    if (next !== "api-key") clearSensitive();
                    setMethod(next);
                  }}
                  secret={secret}
                  setSecret={setSecret}
                  baseURL={baseURL}
                  setBaseURL={setBaseURL}
                  busy={busy}
                  waiting={waiting?.provider === provider ? waiting : undefined}
                  error={error}
                  dismissError={() => {
                    setError(null);
                    clearSensitive();
                  }}
                  close={() => void closeAdd(provider)}
                  startOAuth={(mode) => void startOAuth(provider, mode)}
                  submitKey={() => void submitKey(provider)}
                  fileRef={fileRef}
                  submitFile={(file) => void submitFile(provider, file)}
                />
              ) : null}
              {accounts.length ? (
                accounts.map((account) => (
                  <AccountRow
                    key={account.id}
                    account={account}
                    disabled={unavailable}
                    update={update}
                  />
                ))
              ) : (
                <p className="px-4 py-5 text-sm text-muted-foreground">
                  No {provider === "codex" ? "Codex" : "Claude"} accounts yet.
                </p>
              )}
            </AgentProviderGroup>
          );
        })}
      </div>
    </SettingsSection>
  );
}

function AddAccountPanel(props: {
  provider: Provider;
  method: AddMethod;
  setMethod: (v: AddMethod) => void;
  secret: string;
  setSecret: (v: string) => void;
  baseURL: string;
  setBaseURL: (v: string) => void;
  busy: boolean;
  waiting?: AccountsManagerSnapshot["oauthSessions"][number];
  error: string | null;
  dismissError: () => void;
  close: () => void;
  startOAuth: (mode: "device" | "callback") => void;
  submitKey: () => void;
  fileRef: RefObject<HTMLInputElement | null>;
  submitFile: (file?: File) => void;
}) {
  return (
    <div className="border-b border-border bg-muted/20 p-4">
      <div className="mb-3 flex flex-wrap gap-2">
        {([
          ...(props.provider === "codex" ? (["device"] as const) : []),
          "browser",
          "api-key",
          "json",
        ] as const).map((method) => (
          <Button
            key={method}
            size="sm"
            variant={props.method === method ? "secondary" : "ghost"}
            onClick={() => props.setMethod(method)}
          >
            {method === "device"
              ? "Device sign-in"
              : method === "browser"
                ? "Browser sign-in"
                : method === "api-key"
                  ? "API key"
                  : "Credential JSON"}
          </Button>
        ))}
      </div>
      {props.waiting ? (
        <DeviceOrBrowserWaiting session={props.waiting} />
      ) : props.method === "device" ? (
        <div className="space-y-2">
          <p className="text-sm text-muted-foreground">
            Recommended. AO will show a short code to enter on OpenAI’s secure
            sign-in page.
          </p>
          <Button
            disabled={props.busy}
            onClick={() => props.startOAuth("device")}
          >
            {props.busy ? (
              <LoaderCircle className="mr-2 size-4 animate-spin" />
            ) : null}
            Continue with device code
          </Button>
        </div>
      ) : props.method === "browser" ? (
        <Button
          disabled={props.busy}
          onClick={() => props.startOAuth("callback")}
        >
          {props.busy ? (
            <LoaderCircle className="mr-2 size-4 animate-spin" />
          ) : null}
          Continue in browser
        </Button>
      ) : props.method === "api-key" ? (
        <div className="space-y-2">
          <Input
            aria-label="API key"
            autoComplete="off"
            placeholder="API key"
            type="password"
            value={props.secret}
            onChange={(e) => props.setSecret(e.target.value)}
          />
          <Input
            aria-label="Base URL"
            placeholder="Base URL (optional)"
            value={props.baseURL}
            onChange={(e) => props.setBaseURL(e.target.value)}
          />
          <Button
            disabled={props.busy || !props.secret.trim()}
            onClick={props.submitKey}
          >
            Add account
          </Button>
        </div>
      ) : (
        <input
          ref={props.fileRef}
          type="file"
          accept="application/json,.json"
          disabled={props.busy}
          onChange={(e) => props.submitFile(e.currentTarget.files?.[0])}
        />
      )}
      {props.error ? (
        <div className="mt-3 flex items-center gap-2 text-sm text-destructive">
          <span>{props.error}</span>
          <Button size="sm" variant="ghost" onClick={props.dismissError}>
            Dismiss
          </Button>
        </div>
      ) : null}
      <Button className="mt-2" size="sm" variant="ghost" onClick={props.close}>
        Cancel
      </Button>
    </div>
  );
}

function DeviceOrBrowserWaiting({
  session,
}: {
  session: AccountsManagerSnapshot["oauthSessions"][number];
}) {
  const userCode = session?.userCode;
  if (!userCode) return <p className="text-sm">Waiting for sign-in…</p>;
  return (
    <div className="space-y-2">
      <p className="text-sm text-muted-foreground">
        Enter this code on the OpenAI sign-in page:
      </p>
      <div className="flex items-center gap-2">
        <code className="rounded-md bg-background px-3 py-2 text-base font-semibold tracking-wider">
          {userCode}
        </code>
        <Button
          size="sm"
          variant="secondary"
          onClick={() =>
            void navigator.clipboard.writeText(userCode).catch(() => undefined)
          }
        >
          Copy
        </Button>
      </div>
      <p className="text-sm">Waiting for sign-in…</p>
    </div>
  );
}

function AccountRow({
  account,
  disabled,
  update,
}: {
  account: AccountsManagerAccount;
  disabled: boolean;
  update: (next: AccountsManagerSnapshot) => void;
}) {
  const [open, setOpen] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [details, setDetails] = useState<{
    models?: string[];
    quota?: string;
  } | null>(null);
  const label =
    account.email ||
    `${account.provider === "codex" ? "Codex" : "Claude"} ${account.kind === "api_key" ? "API key" : "account"}`;
  const status = account.disabled
    ? "Disabled"
    : account.unavailable
      ? "Unavailable"
      : account.status === "refreshing"
        ? "Refreshing"
        : account.status === "error"
          ? "Needs attention"
          : "Ready";
  const action = async (task: () => Promise<AccountsManagerSnapshot>) => {
    setBusy(true);
    setError(null);
    try {
      update(await task());
    } catch {
      setError("Account action failed. Try again.");
    } finally {
      setBusy(false);
    }
  };
  const expand = async () => {
    const next = !open;
    setOpen(next);
    if (next && !details) {
      const [models, quota] = await Promise.allSettled([
        fetchAccountsManagerModels(account.id),
        account.quotaSupported
          ? fetchAccountsManagerQuota(account.id)
          : Promise.resolve(null),
      ]);
      setDetails({
        models:
          models.status === "fulfilled"
            ? models.value.models.map((model) => model.displayName || model.id)
            : [],
        quota:
          quota.status === "fulfilled" && quota.value
            ? `${quota.value.groups.length} quota group${quota.value.groups.length === 1 ? "" : "s"}`
            : undefined,
      });
    }
  };
  return (
    <div className="border-b border-border last:border-b-0">
      <div className="flex min-h-16 items-center gap-3 px-4 py-3">
        <button
          className="flex min-w-0 flex-1 items-center gap-2 text-left"
          onClick={() => void expand()}
        >
          {open ? (
            <ChevronDown className="size-4" />
          ) : (
            <ChevronRight className="size-4" />
          )}
          <div className="min-w-0">
            <p className="truncate text-sm font-medium">{label}</p>
            <p className="text-xs text-muted-foreground">{status}</p>
            {error ? <p className="text-xs text-destructive">{error}</p> : null}
          </div>
        </button>
        <Button
          size="icon"
          variant="ghost"
          disabled={disabled || busy}
          aria-label="Refresh account"
          onClick={() =>
            void action(() => refreshAccountsManagerAccount(account.id))
          }
        >
          {busy ? (
            <LoaderCircle className="size-4 animate-spin" />
          ) : (
            <RefreshCw className="size-4" />
          )}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          disabled={disabled || busy}
          onClick={() =>
            void action(() =>
              setAccountsManagerDisabled(account.id, !account.disabled),
            )
          }
        >
          {account.disabled ? "Enable" : "Disable"}
        </Button>
        {confirm ? (
          <>
            <Button
              size="sm"
              variant="outline"
              className="border-destructive/40 text-destructive hover:bg-destructive/10"
              disabled={busy}
              onClick={() =>
                void action(() => removeAccountsManagerAccount(account.id))
              }
            >
              Remove
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setConfirm(false)}>
              Cancel
            </Button>
          </>
        ) : (
          <Button
            size="icon"
            variant="ghost"
            disabled={disabled || busy}
            aria-label="Remove account"
            onClick={() => setConfirm(true)}
          >
            <Trash2 className="size-4" />
          </Button>
        )}
      </div>
      {open ? (
        <div className="space-y-2 border-t border-border px-10 py-3 text-xs text-muted-foreground">
          <p>
            {details
              ? `${details.models?.length ?? 0} models${details.quota ? ` · ${details.quota}` : ""}`
              : "Loading details…"}
          </p>
          {account.cooldowns[0]?.retryAt ? (
            <p>
              Cooldown active until{" "}
              {new Date(account.cooldowns[0].retryAt).toLocaleString()}
            </p>
          ) : null}
          {account.quotaSupported ? (
            <Button
              size="sm"
              variant="outline"
              onClick={() =>
                void resetAccountsManagerQuota(account.id).catch(() =>
                  setError("Quota reset failed. Try again."),
                )
              }
            >
              Reset quota
            </Button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
