# Repository Guidelines

## Project Structure
- `cmd/wacli/`: CLI (auth, sync, backfill, refetch, import, chats, send, doctor, logout, skill) and the IPC that lets `send`/`backfill`/`refetch`/`import` run inside a running `sync --follow`.
- `internal/archive/`: the data. Chat folders shaped like iPhone exports (`_chat.txt` + media), the iOS line format (render/parse), per-chat ID index, holds/flushes, repair, import merge. No whatsmeow dependency.
- `internal/archiver/`: whatsmeow events → archive entries: history sync, live messages, edits/deletes, system lines, names and mentions, media download/retry queue, backfill paging, name refresh, status.json.
- `internal/wa/`: whatsmeow client wrapper and message parsing (only what the archiver and send use).
- `skills/wacli/SKILL.md`: the agent skill, embedded in the binary (`skills/embed.go`) and installed by `wacli skill install`. Update it whenever commands or the archive format change.
- `packaging/aur/wacli/PKGBUILD`: the Arch package (`/usr/bin/wacli`, the `wacli-sync` user unit made from `contrib/systemd/` with `ExecStart=/usr/bin/wacli`; never enabled by the package). `packaging/repo/`: the signed-repo `install.sh` (its `add_signed_repo` is shared byte-for-byte with the owner's other products and hash-pinned in `add_signed_repo.sha256`) and its tests.
- `bin/build-package <arch>`: native, unsigned package build from committed HEAD; `bin/arch-run` wraps it in a disposable Arch chroot (CI, `.github/workflows/packages.yml`); `bin/release <version>`: maintainer-only signing and publishing; `bin/verify-release` (also `.github/workflows/verify-release.yml`).
- `internal/config/`: store-dir resolution. `internal/lock/`: the store LOCK. `internal/delegate/`: socket/pipe transport for IPC. `internal/out/`, `internal/fsutil/`, `internal/sqliteutil/`: helpers.

## Key Architectural Facts
- **The folders are the data.** `~/WhatsApp/WhatsApp Chat - <name>/_chat.txt` must stay byte-compatible with an iPhone export; `internal/archive/testdata` and `WACLI_REFERENCE_EXPORT` (a real export, never committed) check that. Never add markers or IDs to `_chat.txt`.
- **Bookkeeping lives in `<archive>/.wacli/`**: `chats.json`, `index/<jid>.tsv` (row i ↔ entry i), `pending-media.json`, `refetched.json`, `date-format` (the layout its lines use; it wins over the flag), `status.json`. `session.db` (whatsmeow) is the only database.
- **Writes**: in-memory entries are authoritative. Newest message = two appends; anything else, or any failure, rewrites both files from memory (temp file + rename). History chunks `Hold` the chats they touch; other writers `Flush` before acknowledging.
- **Times** use the computer's time zone at write time. Don't pin a zone.
- **Backfill** follows the phone's page order (last message of each page), not timestamps.
- Spec: `docs/archive.md`; commands: `docs/commands.md`.

## Build, Test, and Development Commands
- `make build` → `dist/wacli`; `make install` → `~/.local/bin/wacli` (then `systemctl --user restart wacli-sync`).
- `make test`: `go test -race ./...`, Windows and 32-bit (GOARCH=arm) vet of the cgo-free packages, cgo-required check.
- `make lint`: gofmt, `go vet`, deadcode (production and tests), govulncheck.
- `make packaging`: installer, release and package-build script tests (stubbed pacman/gpg/gh; no system changes).
- `make check`: lint + test + packaging + build. Must pass before every commit; CI runs it.
- `WACLI_REFERENCE_EXPORT=<unzipped iPhone export> go test -run Reference ./internal/archive` checks the format against a real export.

## Releases
- Bump `const version` in `cmd/wacli/main.go` and `pkgver` in the PKGBUILD together (`make packaging` checks), add a CHANGELOG entry, tag `vX.Y.Z` and push it (CI builds both architectures), draft the GitHub release, then `bin/release X.Y.Z`. The signing key lives only in the maintainer's keyring, never CI. Don't run release scripts unless asked.
- Local package build on Arch: `bin/build-package "$(uname -m)"` (needs `go` from pacman); in a clean chroot: `sudo bin/arch-run bin/build-package "$(uname -m)" /output`.

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
