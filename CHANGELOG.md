# Changelog

## 0.27.1 - 2026-11-16

- wacli no longer calls `~/WhatsApp` "the archive" (in WhatsApp, archiving hides a chat): `--archive` is now `--dir` (env `WACLI_DIR`; `--archive` and `WACLI_ARCHIVE_DIR` still work), `--json` output says `dir` instead of `archive` and `last_saved` instead of `last_archived`, and messages and docs say "saved". `docs/archive.md` is now `docs/folders.md`. No change to the folders themselves.

## 0.27.0 - 2026-11-16

- Several WhatsApp accounts: run `wacli auth` once per account (with the sync stopped). Each account is archived in its own folder, `~/WhatsApp/<account>/WhatsApp Chat - <name>/` (`<account>` is its number, `+15551234567`, until you rename the folder; wacli finds a renamed one by its `.wacli/account`), with its session in `<store>/<number>/session.db`. The chat folders themselves are unchanged. One `sync --follow` serves every account; an account WhatsApp unlinks is left out while the others carry on. `--account <number|folder>` (env `WACLI_ACCOUNT`) picks one for `send`, `backfill`, `refetch`, `import` and `logout`, and is needed only when several are linked; `chats`, `doctor` and `watch` show all accounts unless given one. `chats` gains an `account` column; `doctor --json` lists accounts under `accounts`; `sync --json` and `auth --json` add `account`.
- Upgrading moves the existing archive by itself: the first command finds `session.db` at the top of the store and the chat folders at the top of the archive and renames them into `+<number>/` (nothing is copied or deleted; other files in `~/WhatsApp` stay), logging what it moved once. An interrupted move finishes on the next command. A running older sync must be restarted with this version for the move to happen.
- New `watch`: prints each new, edited or deleted message as one JSON line (`source`, `account`, `id`, `thread`, `chat`, `at`, `from`, `from_me`, `kind`, `text`, `media`, `ref`) once the running `sync --follow` has written it, so agents get messages as they arrive instead of polling files. A media message waits for its file (up to a minute). `--since <unix ms>` first replays archived messages from then, `--chat` keeps one chat, `--include-mine` adds your own messages. It uses the sync's connection; it exits non-zero when no sync is running or the sync stops, naming the `--since` to resume from. The running sync must be this version. No change to the archive.

## 0.26.2 - 2026-10-30

- Install on Omarchy/Arch (x86_64 and aarch64) from a signed pacman repository: `curl -fsSL https://ferdousbhai.com/wacli/install.sh | sudo bash`. The `wacli` package ships `/usr/bin/wacli` and the `wacli-sync` systemd user service (enable it after `wacli auth`); updates come with `omarchy update`. `go install` remains the way elsewhere. No change to the archive, its times or sync.

## 0.26.1 - 2026-10-26

- Editing or deleting a message keeps its sender's name whole when the name holds ": ", and a photo such a sender deleted mid-download is set aside as for anyone.
- The archive records the `--date-format` its lines are written in and keeps using it, so a later command or the service can't mix in another (older lines would become unreadable for edits and deletes). An archive from before this is checked against its lines; a mismatch is reported by `doctor` (by `doctor --check` while the sync is stopped).
- `backfill` counts exactly the messages its pages added.
- `doctor` sees a running sync by its held lock, whatever the binary is called, or, where the lock is out of sight (a sandbox, a container), by a fresh status.
- An unusable `WACLI_DATE_FORMAT` is an error when setting up a new archive; for an existing one (a service's environment, say) it's ignored with a note. A mistyped `--date-format` is always an error.

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
