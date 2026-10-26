# Changelog

## 0.26.1 - 2026-10-26

- Editing or deleting a message keeps its sender's name whole when the name holds ": ".
- The archive records the `--date-format` its lines are written in and keeps using it, so a later command or the service can't mix in another (older lines would become unreadable for edits and deletes). An archive from before this is checked against its lines; a mismatch is reported by `doctor`.
- `backfill` counts exactly the messages its pages added.
- `doctor` sees a running sync by its held lock, whatever the binary is called.
- An unusable `WACLI_DATE_FORMAT` (from a service's environment, say) is ignored with a note instead of stopping the start; a mistyped `--date-format` is still an error.

## 0.26.0 - 2026-10-26

First release of this fork: WhatsApp chats kept as iPhone-style export folders.

- Rewrite storage: chats are iPhone-style export folders (`_chat.txt` + media) under `~/WhatsApp`, kept current by `sync`; the SQLite message store, FTS search and the commands built on it are removed.
- `auth` asks for a full history sync by default; media is always downloaded, with re-upload requests for expired files.
- `backfill` walks history in the phone's page order, fixing chats where an out-of-order system message hid older messages.
- New `import` merges a phone export into the archive.
- Messages sent in the same second (an album and its photos, a quick burst) keep their order when they arrive through history.
- New `refetch` asks the phone again about lines archived empty (types an older version couldn't show), fills in what it can and records each type; a running sync does it by itself while WhatsApp is in use on the phone. New empty lines are logged and recorded with their type.
- New `skill install`: the agent skill ships in the binary and is kept current.
- `doctor` reports link, sync, media and empty-line status with what to do about each problem.
- `send`, `backfill`, `refetch` and `import` run inside a running `sync --follow`.
