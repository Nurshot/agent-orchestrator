# Accounts Manager upstream engine

The source under `engine/` is an unmodified snapshot of CLIProxyAPI.

- Upstream: https://github.com/router-for-me/CLIProxyAPI
- Tag: `v7.3.8`
- Commit: `c93978c4ea2e908255a2a06c37599fda3651554a`
- Imported: 2026-09-19
- License: MIT; see `engine/LICENSE`

AO-specific lifecycle, API, storage, and product integration should live outside
`engine/`. Keeping the snapshot isolated makes upstream updates reviewable and
prevents AO-specific behavior from being mixed into the provider engine.

When updating the snapshot, replace `engine/` from a clean upstream checkout,
excluding only its `.git` directory, and update the tag and commit above in the
same change.
