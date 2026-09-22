-- name: UpsertAccountsManagerRoutingPolicy :exec
INSERT INTO accounts_manager_routing_policies (provider, enabled, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(provider) DO UPDATE SET
    enabled = excluded.enabled,
    updated_at = excluded.updated_at;

-- name: DeleteAccountsManagerRoutingPolicyAccounts :exec
DELETE FROM accounts_manager_routing_policy_accounts WHERE provider = ?;

-- name: InsertAccountsManagerRoutingPolicyAccount :exec
INSERT INTO accounts_manager_routing_policy_accounts (provider, account_id, position)
VALUES (?, ?, ?);

-- name: GetAccountsManagerRoutingPolicy :one
SELECT provider, enabled, updated_at
FROM accounts_manager_routing_policies
WHERE provider = ?;

-- name: ListAccountsManagerRoutingPolicyAccounts :many
SELECT account_id
FROM accounts_manager_routing_policy_accounts
WHERE provider = ?
ORDER BY position ASC;

-- name: InsertAccountsManagerSessionRoute :execrows
INSERT INTO accounts_manager_session_routes (session_id, provider, account_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(session_id, provider) DO NOTHING;

-- name: GetAccountsManagerSessionRoute :one
SELECT session_id, provider, account_id, created_at, updated_at
FROM accounts_manager_session_routes
WHERE session_id = ? AND provider = ?;
