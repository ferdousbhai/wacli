# Changelog

## Unreleased

- Rewrite storage: chats are iPhone-style export folders (`_chat.txt` + media) under `~/WhatsApp`, kept current by `sync`; the SQLite message store, FTS search and the commands built on it are removed.
- `auth` asks for a full history sync by default; media is always downloaded, with re-upload requests for expired files.
- `backfill` walks history in the phone's page order, fixing chats where an out-of-order system message hid older messages.
- New `import` merges a phone export into the archive.
- New `refetch` asks the phone again about lines archived empty (types an older version couldn't show), fills in what it can and records each type; a running sync does it by itself while WhatsApp is in use on the phone. New empty lines are logged and recorded with their type.
- New `skill install`: the agent skill ships in the binary and is kept current.
- `doctor` reports link, sync, media and empty-line status with what to do about each problem.
- `send`, `backfill`, `refetch` and `import` run inside a running `sync --follow`.
