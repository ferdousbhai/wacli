# Repository Guidelines

## Project Structure
- `cmd/wacli/`: CLI (auth, sync, backfill, import, chats, send, doctor, logout) and the IPC that lets `send`/`backfill`/`import` run inside a running `sync --follow`.
- `internal/archive/`: the data. Chat folders shaped like iPhone exports (`_chat.txt` + media), the iOS line format (render/parse), per-chat ID index, holds/flushes, repair, import merge. No whatsmeow dependency.
- `internal/archiver/`: whatsmeow events → archive entries: history sync, live messages, edits/deletes, system lines, names and mentions, media download/retry queue, backfill paging, name refresh, status.json.
- `internal/wa/`: whatsmeow client wrapper and message parsing (only what the archiver and send use).
- `skills/wacli/SKILL.md`: the agent skill, embedded in the binary (`skills/embed.go`) and installed by `wacli skill install`. Update it whenever commands or the archive format change.
- `internal/config/`: store-dir resolution. `internal/lock/`: the store LOCK. `internal/delegate/`: socket/pipe transport for IPC. `internal/out/`, `internal/fsutil/`, `internal/sqliteutil/`: helpers.

## Key Architectural Facts
- **The folders are the data.** `~/WhatsApp/WhatsApp Chat - <name>/_chat.txt` must stay byte-compatible with an iPhone export; `internal/archive/testdata` and `WACLI_REFERENCE_EXPORT` (a real export, never committed) check that. Never add markers or IDs to `_chat.txt`.
- **Bookkeeping lives in `<archive>/.wacli/`**: `chats.json`, `index/<jid>.tsv` (row i ↔ entry i), `pending-media.json`, `status.json`. `session.db` (whatsmeow) is the only database.
- **Writes**: in-memory entries are authoritative. Newest message = two appends; anything else, or any failure, rewrites both files from memory (temp file + rename). History chunks `Hold` the chats they touch; other writers `Flush` before acknowledging.
- **Times** use the computer's time zone at write time. Don't pin a zone.
- **Backfill** follows the phone's page order (last message of each page), not timestamps.
- Spec: `docs/archive.md`; commands: `docs/commands.md`.

## Build, Test, and Development Commands
- `make build` → `dist/wacli`; `make install` → `~/.local/bin/wacli` (then `systemctl --user restart wacli-sync`).
- `make test`: `go test -race ./...`, Windows and 32-bit (GOARCH=arm) vet of the cgo-free packages, cgo-required check.
- `make lint`: gofmt, `go vet`, deadcode (production and tests), govulncheck.
- `make check`: lint + test + build. Must pass before every commit; CI runs it.
- `WACLI_REFERENCE_EXPORT=<unzipped iPhone export> go test -run Reference ./internal/archive` checks the format against a real export.

## Coding Style
- Standard `gofmt`. Short, early-return functions; explicit errors, no panics.
- Structured data to stdout (`--json` / tables); progress and errors to stderr.
- Keep the dependency tree minimal; cgo only for sqlite3.

## Testing Guidelines
- Every bug fix ships with a regression test.
- Archiver tests drive `handle` with fake whatsmeow events (`archiver_test.go`); never hit real WhatsApp in tests.
- Never commit real chat content; tests and docs use fictional names.

## Commit Guidelines
- Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `ci:`), imperative summary, focused commits.
