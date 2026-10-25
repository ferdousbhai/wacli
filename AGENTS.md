# Repository Guidelines

## Project Structure
- `cmd/wacli/`: CLI wiring (auth, sync, backfill, import, chats, send, doctor, logout) and the IPC that lets `send`/`backfill`/`import` run inside a running `sync --follow`.
- `internal/archive/`: the data. Chat folders shaped like iPhone exports (`_chat.txt` + media), the iOS line format (render/parse), per-chat ID index, merge of phone exports. No whatsmeow dependency.
- `internal/archiver/`: whatsmeow events → archive entries: history sync, live messages, edits/deletes, system lines, names and mentions, media download/retry queue, page-by-page backfill.
- `internal/wa/`: whatsmeow client wrapper and message parsing.
- `internal/config/`: store-dir resolution (`WACLI_STORE_DIR` → XDG state dir on Linux → `~/.wacli`).
- `internal/lock/`: LOCK file on the store dir; one process owns the WhatsApp session.
- `internal/delegate/`: local socket/pipe transport for IPC.
- `internal/out/`, `internal/fsutil/`, `internal/pathutil/`, `internal/sqliteutil/`: helpers.

## Key Architectural Facts
- **The folders are the data.** `~/WhatsApp/WhatsApp Chat - <name>/_chat.txt` must stay byte-compatible with an iPhone export; `internal/archive/testdata` and `WACLI_REFERENCE_EXPORT` (a real export, never committed) check that.
- **Bookkeeping lives in `<archive>/.wacli/`**: `chats.json` (JID → folder, backfill cursor), `index/<jid>.tsv` (one row per entry, same order: msg id, ts, from_me, sender), `pending-media.json`.
- **`session.db`** (whatsmeow, SQLite) is the only database: login and Signal keys.
- **Writes**: appends for the newest message; anything else rewrites the file to a temp file and renames it.
- **Backfill** follows the phone's page order (last message of each page), not timestamps.
- Spec: `docs/archive.md`.

## Build, Test, and Development Commands
- Build: `pnpm build` — compiles with `-tags sqlite_fts5` and `CGO_CFLAGS=-Wno-error=missing-braces` (required for GCC 15+).
- Run: `pnpm wacli -- <args>` — rebuilds then runs.
- Test: `pnpm test` — runs plain and FTS Go tests, the Windows lock/store test cross-compiles, cgo-required build check, and Node documentation/release-script tests.
- Lint: `pnpm lint` — `go vet ./...`; `make lint` also runs vulnerability checks and `pnpm lint:deadcode` for production and test reachability.
- Format fix: `pnpm format` — `gofmt -w .`.
- Format check: `pnpm format:check` — fails if any file would change.
- **Full gate** (must pass before every PR): `pnpm format:check && pnpm lint && pnpm lint:deadcode && pnpm test && pnpm build && pnpm docs:site && git diff --check`.

## Coding Style
- Standard `gofmt` formatting; run `pnpm format` before committing.
- Output: send structured data to stdout (`--json` / table); send human hints, progress, and errors to stderr via `internal/out`.
- Prefer explicit error returns over panics; write short, early-return functions.
- No build-time CGO beyond sqlite3; keep the dependency tree minimal.

## Testing Guidelines
- Every bug fix should ship with a regression test.
- FTS-sensitive tests must run under `-tags sqlite_fts5`; non-FTS path tests must also pass without the tag.
- Use `fake_wa_test.go` / table-driven tests for whatsmeow interaction; avoid hitting real WhatsApp in unit tests.
- Integration tests that need a live account are opt-in and not part of the standard gate.
- Local retrieval CLI scenarios: `WACLI_RETRIEVAL_ARTIFACTS=/outside/repository/artifacts go test ./internal/app -run '^TestRetrievalStage[123]CLI$' -count=1` (repeat with `-tags sqlite_fts5`). These build and execute the real CLI against disposable stores and save per-scenario transcripts.

## Commit & Pull Request Guidelines
- Follow Conventional Commits: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `security:`, `ci:` with an imperative summary.
- Keep commits focused; avoid bundling unrelated changes.
- PRs should state: what changed, why, how it was tested, and any new flags or env vars.
- Run the full gate locally before opening a PR; CI runs the same commands.
- New contributors: add `Co-authored-by:` trailers when building on their work.

## Agent Notes
- This repo uses `AGENTS.md` as its agent-instruction source; `CLAUDE.md` is explicitly ignored.
- For agent-safe execution, pass `--read-only` (or set `WACLI_READONLY=1`) to prevent writes.
- Prefer `--json` output for machine-readable parsing.
- Do not add dependencies or change build tooling without confirming with the maintainer.
