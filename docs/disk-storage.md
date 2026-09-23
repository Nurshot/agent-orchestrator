# Disk storage and cleanup

Starting note for reducing bytes under `~/.ao`. This is a reference for what is safe to delete and what order to attack the problem — not a design to implement as written.

The [performance.dev](https://performance.dev) articles, and the synthesis that informed [PR 5766](https://github.com/Untrivial-ai/agent-orchestrator/pull/5766), barely talk about deleting files. Their disk lesson is: do less disk work on the way to the first reply, and put a bound on the artifacts you keep writing. Deleting worktrees and archived sessions is an AO problem those articles do not solve.

## What the four performance.dev articles say about storage

[performance.dev](https://performance.dev) currently publishes only these four posts. None of them describes a user-data deleter, a retention window, or a scan of an app data directory. Storage shows up as an architecture choice that makes the UI fast, or as a side effect of fewer writes.

| Article | What it actually says about storage | What it does not say |
| --- | --- | --- |
| [The Conductor Rewrite](https://performance.dev/the-conductor-rewrite) | SQLite is the only store for workspaces, chat history, checkpoints, and settings. There is no remote database to copy from. Idle agent processes are killed and resumed from a session uuid that already lives on disk. The start checkpoint (`git add -A` over the whole tree) was moved off the path to the first token; the snapshot still happens, just in the background. | No cap on checkpoints, chat rows, or worktrees. Idle shutdown reclaims memory, not files. "The session lives on disk" is the reason a killed process is safe, which keeps bytes, it does not drop them. |
| [How's Linear so fast?](https://performance.dev/how-is-linear-so-fast-a-technical-breakdown) | One browser database (IndexedDB) is what the UI reads. The server is a sync target. Issue and Comment, the two heaviest tables, lazy-hydrate on demand so startup cost tracks workspace structure, not workspace size. The service worker precaches ~1,200 hashed assets. `localStorage` holds shell chrome (theme, sidebar width), not the issue database. | No eviction policy for IndexedDB. Lazy hydration bounds memory and first-load network, and only bounds disk if you also refuse to persist the tables you did not hydrate. A second local copy of a daemon SQLite database was rejected for AO for that reason. |
| [Reverse Engineering ChatGPT Web](https://performance.dev/chatgpt) | Client data is a TanStack Query cache seeded by the server (`window.__REACT_QUERY_CACHE__`). Theme is a `localStorage` read before paint. Assets use 30-day HTTP cache headers. There is no service worker: they deploy constantly, and an offline cache of a network conversation is stale complexity. | Nothing about a local data directory, SQLite, or deleting history. |
| [Wealthsimple: one year post-acquisition](https://performance.dev/wealthsimple-year-one) | Cache the shape of the page (avatar initial, account labels, skeleton row count) so the first paint does not wait on the network. Content-hash font filenames so a 10-year cache header cannot pin a stale binary. Logos became small vectors with guessable, forever-cacheable URLs. Prefetch cache keys must match the query exactly or the cache never hits. | Web delivery. Not a local data-dir problem. |

The aggregate note in the performance research treats the disk row as "explicit SQLite retention/pagination and bounded checkpoint/log artifacts," plus moving git and disk work off the path before the agent speaks without giving up crash safety. That retention sentence is an AO recommendation. It is not a claim any of the four articles made.

## Other writing that does speak to disk

These are outside performance.dev. They are the sources that actually describe where bytes go.

| Source | Storage point that applies to AO |
| --- | --- |
| [How we built checkpointing](https://blog.conductor.build/checkpointing/) and [Checkpoints](https://www.conductor.build/docs/reference/checkpoints) | Conductor rejected storing a full worktree snapshot inside SQLite (they would have had to rebuild `git diff`). Each turn writes a private ref, `.git/refs/conductor-checkpoints/<id>`, from three tree SHAs (HEAD, index, untracked worktree). Files are not copied; git objects are. The public docs do not cap those refs. Revert deletes later chat messages and code, which is user-initiated, not a janitor. |
| [Conductor archive](https://www.conductor.build/docs/reference/scripts) and [0.33.5](https://www.conductor.build/changelog/0.33.5-better-archiving) | Archive runs a project script to clean resources *outside* the workspace, can delete the branch (`git.delete_branch_on_archive`), and now *saves* uncommitted git state so unarchive still works. Archive is not "delete the checkout." |
| [Git worktrees](https://www.conductor.build/docs/concepts/git-worktrees) | Worktrees share one object database. Disk cost is the extra checkout, not a second clone. |
| [Linear startup changelog, 2021-03-31](https://linear.app/changelog/2021-03-31-startup-performance-improvements) | Pre-warmed clients already have the workspace in the local database. They lazy-load infrequently used data. Same lesson as the performance.dev piece: bound what you load, the local DB is still the record. An older Linear changelog also surfaces a warning when a local save fails because the disk is full. |
| [SQLite WAL](https://www.sqlite.org/wal.html) | A WAL grows without a bound while a reader or a large write transaction blocks checkpoint. Default checkpoints recycle around 1000 pages (~4 MB) and do not truncate. `journal_size_limit` and `wal_checkpoint(TRUNCATE)` are how you reclaim the file. Copying `ao.db` without `-wal`/`-shm` is not a consistent backup. |
| [VACUUM in WAL mode](https://photostructure.com/coding/how-to-vacuum-sqlite/) | `VACUUM` can move the bloat into the WAL. Net disk only drops after `checkpoint(TRUNCATE)`. |

None of these is a design for deleting AO sessions. The useful constraints are: one database, git objects instead of file copies, private refs that still need a cap, lazy load of the heavy tables, and an explicit WAL checkpoint if the `-wal` file is the pile you measured.

## How Orca keeps storage

[Orca](https://github.com/stablyai/orca) is a parallel-agent desktop, closer to AO than Linear or ChatGPT. There is no single "disk cleanup" document. The data-root contract is in [`docs/reference/orcad-operations.md`](https://github.com/stablyai/orca/blob/main/docs/reference/orcad-operations.md). The rest is in the persistence code.

**Where state lives.** The data root is `$ORCA_USER_DATA`, else `$XDG_DATA_HOME/Orca`, else `~/.orca`. The durable app record is `orca-data.json` in the Electron `userData` directory (`src/main/persistence/loading-store/user-data-path.ts`), not a SQLite database of sessions. SQLite is a side index.

**Bounds they actually implemented.**

- **JSON backups are capped.** `orca-data.json` keeps five rotating `.bak` copies, and a new snapshot is taken at most once an hour (`BACKUP_COUNT = 5`, `BACKUP_MIN_INTERVAL_MS` in `backup-recovery-rotation.ts`).
- **A hot cache is a sidecar, not a rewrite of the record.** GitHub PR/issue cache lives in `orca-github-cache.json` because a poll would otherwise rewrite the whole multi-megabyte JSON. The comment says it is safe to lose.
- **Missing worktrees drop metadata, not files, and only when nothing still owns them.** `missing-local-worktree-metadata-pruning.ts` removes sessionless metadata rows. A row stays if it is the last-active worktree, selected on a paired phone, held by a reattachable SSH lease, targeted by an enabled automation, or referenced by an automation run that has not finished. A terminated or expired lease does not pin the row. That is metadata GC, not `rm` of a checkout.
- **Removing a repo prunes that repo's keys in the JSON** (`repo-worktree-pruning.ts`), scoped to one execution host so a local delete does not wipe another host's tabs.
- **Broken git registrations are pruned, checkouts are not.** [`malformed-worktree-registration-removal.md`](https://github.com/stablyai/orca/blob/main/docs/reference/malformed-worktree-registration-removal.md): `git worktree prune` only when git itself marks the row prunable, it has a named branch and HEAD, it is not main, it is not locked, and `.git` is a regular file. Missing evidence refuses the recovery. The branch and the files stay.
- **Terminal scrollback is capped and deleted with the tab.** Snapshots live in `<userData>/terminal-scrollback/`. A removed tab deletes its snapshot file (`terminal-session-cleanup.ts`). Limits in `terminal-scrollback-limits.ts`: 512 KB per session buffer, 512 KB replay, 5 MB store.
- **Session search is an opt-in derivative.** `<dataRoot>/ai-vault/session-search.sqlite` indexes transcripts the host can already read. Default is off, and `historyDays: null` means no age bound (max 3650 days if set). Tool output over 3,072 characters is not indexed. Clearing the index deletes the sqlite file and sidecars and never deletes the original transcripts (`docs/reference/agent-session-search-contract.md`).
- **Do not copy a large SQLite into each launch home.** Merged PRs [#4323](https://github.com/stablyai/orca/pull/4323) and [#4377](https://github.com/stablyai/orca/pull/4377) symlink Codex `*.sqlite`/`-wal`/`-shm` across launch homes. Hashing a 1 GB `logs_2.sqlite` on every terminal launch cost ~1 s; skipping the read dropped that to a few milliseconds. Same lesson as Linear: one database, no second copy, and do not read it on the way to opening a terminal.
- **Daemon logs are explicitly unbounded.** `orcad-operations.md` says `<data-root>/logs/daemon.log` has no rotation.
- **Credential migration copies and leaves the source.** Mobile pairing files are copied into the canonical userData dir and not deleted, so a crash or a rollback does not strand paired devices.

What AO can take from Orca: cap derivative artifacts (backups, scrollback, search index) with a number, split hot rewriteable caches out of the record, garbage-collect metadata for worktrees that are gone and unowned, and refuse deletion when the evidence is missing or the tree is still referenced. Orca does not solve AO's problem of old conversation rows in `ao.db` or dirty worktrees left on disk.

### Already shipped from that work ([PR 5766](https://github.com/Untrivial-ai/agent-orchestrator/pull/5766))

- Streamed prose is committed about every 32 events, 8 KB, or 40 ms, instead of once per token. The finished answer is the same size. The win is fewer writes while it is still streaming.
- Git fetch runs after the agent starts.

Neither of those deletes old bytes.

### Written then left out of that PR (not shipped)

A static label list for `~/.ao` (`must-keep`, `user-work`, `safe-to-rebuild`) and a `CanDelete` check that refuses anything that is not safe to rebuild, and refuses a dirty worktree even then. It did not scan the disk and it did not delete on its own. It was only wired to two deletes that were already allowed: a session's generated prompt folder, and replacing the installed `using-ao` skill copy. Do not treat that as shipped.

## Where the bytes are

Default home is `~/.ao`, overridable with `AO_DATA_DIR`. Packaged Electron profile is `~/.ao/electron`. Dev profile is `~/.ao/dev/electron`.

Rough piles, from reading the tree and one real data dir. Sizes will differ. **Measure before deleting anything.**

| Path | What it is | Delete? |
| --- | --- | --- |
| `data/ao.db` and `-wal` / `-shm` | Sessions, conversations, messages | No. This is the record. |
| `data/worktrees/` | Per-session git worktrees | Only through the existing worktree teardown. Never a raw `rm` of a dirty tree. |
| `data/chat-hosts/` | Provider host descriptors needed to resume a chat | No, unless you have proved the session can never resume. |
| `data/mobile/` | Mobile credential and identity | No. |
| `data/prompts/` | Generated system prompts | Rebuildable. Already removed per session in `cleanupSystemPromptDir`. |
| `data/skills/` | Installed skill copy | Rebuildable. `skillassets.Install` writes it again. |
| `running.json` | Daemon handshake | Rebuildable. |
| `electron/Cache`, `Code Cache`, `GPUCache`, `Session Storage`, `Crashpad` | Chromium junk | Rebuildable. |
| `electron/Local Storage` | Unsent composer drafts | User work. |
| Rest of `electron/` | Desktop profile | Treat as user work unless you have named a rebuildable child. |

A real `~/.ao/data` looked roughly like: harnesses, `ao.db`, runtime, then worktrees. Worktrees were not the bulk of that particular dir. Check the machine you are cleaning before assuming worktrees are the win.

## Rules that already exist

From [AGENTS.md](../AGENTS.md) and the session manager. A cleanup that breaks these is wrong.

- All AO state stays under `~/.ao`. Do not invent another data dir.
- Do not force-delete a dirty registered worktree unless its uncommitted work is already in a durable local snapshot. Interactive archive snapshots that work, then removes the folder. The snapshot is an active `session_worktrees` row with `preserved_ref`, not a shutdown-restore row, so reopening checks out the branch and does not apply the edits. The person puts them back with a separate request. If the snapshot cannot be stored, the folder stays. Shutdown still writes a `removed` row before `ForceDestroy`, and that row is what brings the edits back on the next boot. `ao session cleanup` still skips a dirty folder that was never captured.
- A failed or unknown runtime probe is not proof a session is dead.
- Do not edit an already-merged SQLite migration. Retention for `change_log` or old messages needs a new migration.
- CDC stays trigger-based. Do not emit a parallel change log from Go just because you deleted rows.
- The loopback daemon stays unauthenticated. A cleanup endpoint is not a reason to add auth there.

`Kill` (`session_manager.Manager.Kill`) already tries to remove that session's worktree after the agent is stopped, attachments are imported, and the shell in that directory is closed. Archived-looking sessions in the sidebar are `is_terminated` rows. The sidebar hides them. The conversation rows stay in `ao.db`. Confirm on a real dir whether the worktree directory is already gone before building a second deleter.

Archived projects are `projects.archived_at`. On a filled test copy, archived projects still had project rows and terminated session rows in the database. Hiding them is not the same as deleting them.

## Work worth doing, in this order

1. **Measure.** For one real `~/.ao`, record bytes for `ao.db`, `ao.db-wal`, `data/worktrees`, `data/runtime`, `data/harnesses`, `electron/Cache`, and `data/prompts`. Split worktrees into live sessions, terminated sessions, and directories with no session row. Split `ao.db` by table (`conversation_messages`, `conversation_provider_events`, `change_log`, `sessions`). Do this before picking a deleter. The articles' whole point was to measure the bottleneck first.
2. **Orphan worktrees.** A directory under `data/worktrees` whose session row is gone is the safest git cleanup. A directory whose session is terminated but the folder remains is the next case, and only if `git status` is clean. Dirty means leave it. Live session means leave it. Do not `ForceDestroy` from a new cron without the same stash-then-row order shutdown already uses.
3. **Terminated sessions in the database.** The sidebar already drops them. The rows, messages, and provider-event archive remain. A retention job can delete conversation payloads for sessions that are terminated and whose worktree is already gone, after a grace period. Keep the session row if the UI still needs to show it in an archive. Do not delete a terminated session that still has a dirty worktree or a preserved ref in `session_worktrees`. Chat hosts for a session you might resume are not safe just because the sidebar hid it.
4. **Archived projects.** `archived_at` is set, but sessions under that project are not automatically wiped. Cleanup should follow the sessions, not the project flag alone. An archived project can still point at a repo path the user cares about. Deleting the project row is not deleting the repo.
5. **`change_log` and the provider-event archive.** These grow because every conversation write emits CDC, and every provider event is archived so a crash can replay. Batching writes less often slows growth; it does not cap the table. A new migration can delete `change_log` rows older than the client has consumed, and provider events for turns that are finished and acknowledged. Do not drop events a live host has not acknowledged.
6. **Rebuildable Electron and prompt files.** Cache, code cache, GPU cache, crash dumps, and `data/prompts` can be removed while the app is quit. Do not touch Local Storage. That holds unsent drafts.

## What not to build

- A second database, in IndexedDB or elsewhere, "so we can drop SQLite rows." The articles reject this. The daemon database is the record.
- Virtualizing the chat transcript as a storage plan. That saves renderer memory. It does not shrink `ao.db`.
- A scan of `~/.ao` on the Send path to decide what to delete. The label list, if you bring it back, has to be static. Send must not walk the disk.
- Force-deleting a dirty worktree that has no durable local snapshot, including "the session looks terminated."
- Treating write batching ([PR 5766](https://github.com/Untrivial-ai/agent-orchestrator/pull/5766)) as disk cleanup. It is not.

## Related

- [architecture.md](architecture.md) — persistence/CDC model and load-bearing worktree rules
- [AGENTS.md](../AGENTS.md) — hard rules for data dir, dirty worktrees, migrations, CDC
- [docs/performance/chat-responsiveness/](performance/chat-responsiveness/) — renderer/delivery performance (orthogonal to disk retention)
