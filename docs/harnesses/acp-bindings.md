# ACP Chat bindings and the TUI-only harness matrix

Chat is opt-in per harness. The registry
(`backend/internal/adapters/chatdriver/registry/registry.go`) is the whole
capability gate: a harness with no driver returns `ErrChatUnsupported`, and the
desktop never offers the TUI ⇄ Chat toggle for it. This page records each
binding's launch shape, how AO maps its permission and standing-instruction
policy, and why the remaining harnesses are still TUI-only.

## Shipped bindings

Every binding below launches the executable resolved by that harness's existing
agent plugin. AO never downloads, packages, or substitutes the provider CLI.

| Harness | Launch | Model | Permissions | Standing instructions |
| --- | --- | --- | --- | --- |
| Auggie | `auggie --acp` | `--model` at launch and `session/set_model` | ACP requests; `accept-edits`/`bypass` auto-resolve | session-private `--rules` file |
| Autohand | `autohand-acp` | session-advertised model options | `AUTOHAND_PERMISSION_MODE=external`; `accept-edits`/`auto`/`bypass` auto-resolve | not injectable |
| Cline | `cline --acp` | `CLINE_MODEL` env and `session/set_config_option` | `--auto-approve true` for `auto`/`bypass`; `accept-edits` auto-resolves edit tools | not injectable (ACP ignores `--system`) |
| Goose | `goose acp` | session-advertised model options | `GOOSE_MODE` env (`smart_approve`/`auto`) | not injectable |
| Kilo Code | `kilocode acp` | `session/set_config_option` | `KILO_CONFIG_CONTENT` permission map | `KILO_CONFIG_CONTENT` generated agent |
| Kiro | `kiro-cli acp --agent ao` | `session/set_model` | ACP requests; `accept-edits`/`auto`/`bypass` auto-resolve | workspace-local `ao` custom agent |
| Prime Agent | `prime-agent --mode acp` | `session/set_config_option` (`model`, `thought_level`) | unsupported: bypass-only admission | not injectable |
| Vibe | `vibe-acp` | session-advertised model options | ACP requests | not injectable |

Codex remains on its native app-server. Claude Code, Cursor, OpenCode, Droid,
Kimi, Kimchi, Pi, and OMP keep the bindings they already shipped.

### Deliberate limitations

- **Cline, Goose, and Vibe** expose no launch-time standing-instruction surface
  in ACP mode, so AO's role prompt is not forwarded. Cline ignores `--system`
  once `--acp` is set; `goose acp` accepts only `--with-builtin` and
  `--enable-scheduler`; `vibe-acp` accepts only setup/harness flags. The
  provider's own project rules (for example `AGENTS.md`) still apply.
- **Prime Agent** has no ACP permission requests and no `session/load`, so AO
  reports approvals and resume unsupported. Chat admission therefore requires
  the explicit per-session bypass choice, exactly as Pi does.
- Approval changes that a binding fixes at process launch are rejected with
  `ErrACPSetterUnsupported` rather than silently ignored; the UI tells the user
  to restart Chat.

### Verification status

Only Auggie was validated against a real local binary in this change (the
`auggie --acp` `initialize` handshake and the driver `Probe`). The other
bindings were derived from each provider's published ACP entrypoint and source
and are covered by focused unit tests over launch argv, environment, permission
mapping, and turn-setting validation. Each provider's build-tagged live test
(for example `AO_LIVE_AUGGIE_ACP=1`) is the path to end-to-end confirmation and
requires that provider to be installed and logged in; CI never depends on it.

## TUI-only harnesses: blocker matrix

| Harness | Structured protocol found | Evidence | Outcome |
| --- | --- | --- | --- |
| Aider | none | One-shot `--message`/`--message-file` only; no persistent agent protocol. | Blocked: no ACP or native structured control surface. |
| Amp | none | `@ampcode/cli` (renamed from `@sourcegraph/amp`); no ACP entrypoint published. | Blocked. |
| Crush | native server protocol, not ACP | `internal/server`, `internal/proto/server.go`, and a `/control` endpoint implement Crush's own client/server API. | Blocked for the ACP transport. A bespoke driver over Crush's native protocol would be a new non-ACP transport and could not be validated here. |
| Continue | none | `cn` (`@continuedev/cli`) is a one-shot/TUI CLI; no ACP or persistent bidirectional protocol. | Blocked. |
| Devin | none | Vendor CLI exposes no local ACP or structured session protocol. | Blocked. |
| Grok | none | `superagent-ai/grok-cli` has no ACP or persistent structured protocol. | Blocked. |
| Kilo Code | ACP | `@kilocode/cli` is a fork of OpenCode with a native `acp` subcommand. | Implemented. |
| Muse | third-party, unofficial | `@bex-co/muse-code-acp` is an unofficial community adapter for Meta's `muse` CLI; Meta publishes no first-party ACP server. | Not implemented: no first-party protocol and no local binary to validate the community adapter. |
| Prime Agent | ACP | `prime-agent --mode acp`; `packages/coding-agent/src/modes/acp`. | Implemented. |
| Autohand | ACP | Vendor-published `@autohandai/autohand-acp` adapter. | Implemented. |

## Open PRs that also close a harness gap

Audited against `main`; each registers its harness in the chat-driver registry.

- **#5019 Qwen Code Chat UI (native ACP)** — fully closes the Qwen gap. Adds a
  `qwenacp` binding with a version probe and live tests, registers it, and has
  all CI checks passing and the branch mergeable. No missing integration for the
  Chat gap.
- **#5403 GitHub Copilot ACP chat driver** — closes the Copilot gap. Adds a
  self-contained `copilotacp` binding plus the `LaunchSessionOptions` seam in
  the shared ACP/native ACP transports and the Copilot adapter helpers it needs.
  Concrete missing integration: no CI checks have run on the branch. It is
  mergeable, so it needs a push/CI run and review rather than code changes.
- **#3989 Agy Chat driver over stream-json** — closes the Agy gap with a native
  stream-json driver plus CLI/HTTP hook plumbing. Concrete missing integration:
  the branch is `CONFLICTING`/`DIRTY` against `main` and has no CI checks. It
  was based on an older `main` whose registry comment predates the Claude, Kimi,
  Kimchi, Pi, Cursor, and OMP bindings, so it needs a rebase and a CI run.

These three are intentionally not duplicated by this change.