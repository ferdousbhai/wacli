# Changelog

## Unreleased

- Rewrite storage: chats are iPhone-style export folders (`_chat.txt` + media) under `~/WhatsApp`, kept current by `sync`; the SQLite message store, FTS search and the commands built on it are removed.
- `auth` asks for a full history sync by default; media is always downloaded, with re-upload requests for expired files.
- `backfill` walks history in the phone's page order, fixing chats where an out-of-order system message hid older messages.
- New `import` merges a phone export into the archive.
- `send`, `backfill` and `import` run inside a running `sync --follow`.

Started from openclaw/wacli 0.25.0 (MIT); earlier history is in git.
