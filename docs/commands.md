# Commands

Global flags: `--archive` (`WACLI_ARCHIVE_DIR`, default `~/WhatsApp`, holding
a folder per account), `--store` (`WACLI_STORE_DIR`, the sessions), `--date-format` (`WACLI_DATE_FORMAT`, a Go
time layout matching your phone's region; set it before the first `auth`: the
archive records the format its lines are written in and keeps using it, whatever
a later command or the service is given), `--json`.

One process owns the WhatsApp sessions at a time. While `sync --follow` runs,
`send`, `backfill`, `refetch` and `import` hand their work to it; stopping the client
(Ctrl+C) stops that work. `watch` streams from it. `chats` and `doctor` only read, and work any time.

## Accounts

Every linked WhatsApp account has a folder in the archive (named after its
number, `+15551234567`, until you rename it) and a session in the store.
`--account <number|folder name>` (env `WACLI_ACCOUNT`) chooses one; numbers
match in any spelling (`+1 555 123 4567`, `15551234567`, the JID), folder
names case-insensitively. With one linked account it is never needed (a
logged-out one stays listed, for its folder, but isn't counted). With several,
`send`, `backfill`, `refetch`, `import` and `logout` fail without it, listing
the accounts; `chats`, `doctor` and `watch` show every account unless given
one. `sync` always serves every linked account.

Rename an account's folder by hand, with the sync stopped: wacli finds it by
its `.wacli/account` file and records the new name (see
[archive.md](archive.md#accounts)). The first command after upgrading from a
version without accounts moves the chat folders and session into the account
folder by itself.

## auth

```bash
wacli auth [--phone +NUMBER] [--history-days N] [--idle-exit 3m]
```

Links a WhatsApp account to this computer as a companion device (QR code, or
a pairing code with `--phone`) and archives what the phone sends, with media,
into the account's folder (`<archive>/+<number>/`, made once the phone has
paired). Accounts linked before keep working; run it once per account. It
needs the store to itself: stop a running sync first (`systemctl --user stop
wacli-sync`) and start it again afterwards, so it serves the new account too.
Linking an account that is linked already is refused (the extra link is
removed again; if you removed this computer's link on the phone while no sync
ran, `wacli logout --account A` first); one that was logged out picks up its
old folder. It asks for
a **full** history sync, as WhatsApp Desktop does. That request is part of
the linking handshake, so it only applies to a new link: run `wacli logout`
(`--account A` with several) first to relink. History arrives in batches over several minutes; keep the phone
online. `--history-days` limits how far back; `--idle-exit` is how long to wait
after the last batch.

## sync

```bash
wacli sync [--follow] [--idle-exit 30s]
```

Archives everything WhatsApp delivers to every linked account: new messages,
the backlog queued while wacli was offline, edits, deletes, group changes, and
downloads media. Without `--follow` it stops once all are idle (`--json`
prints one summary, or a list with several accounts). With `--follow` it runs
until stopped (normally as the `wacli-sync` systemd user service; see the
README), one process and one connection per account. An account WhatsApp
unlinks or stops for good is left out and the others carry on. If WhatsApp unlinks
the last account, it exits with status 3 so the service stops restarting. If
WhatsApp ends the connection for good another way (another client took over
the session, a temporary ban, an outdated wacli), it exits with status 4 and
`wacli doctor` shows why; the service then stays stopped. Deal with the cause
(close the other client, wait out the ban, update wacli), then
`systemctl --user start wacli-sync`. `auth` and `sync` without `--follow` exit
the same way.

## backfill

```bash
wacli backfill --chat <name|number|JID> | --all [--account A] [--count 50] [--requests 1000] [--wait 1m]
```

Asks the phone for older history, page by page, starting from the newest
message and following the phone's own order, so gaps in the middle of a chat
are filled too. Progress is saved per chat, so a later run continues where
this one stopped (`--requests` caps the pages per run); a chat is marked
complete when the phone has nothing more. The phone must be online: `--all`
gives up (exit status 1) when the first three chats get no answer or the
connection is lost. Phones don't
hand linked devices everything; for the rest, use `import`.

## refetch

```bash
wacli refetch [--chat <name|number|JID>] [--account A] [--again] [--count 50] [--wait 30s]
```

Finds lines with nothing after the sender's name and asks the phone for the
history around them again (between your phone and wacli only; nothing is sent
to the chat). Lines wacli can show are filled in, and every message's type is
reported, e.g. `album`, `deleted`, `not on phone`, or the WhatsApp field name of
a type wacli can't show yet. Types are remembered in `.wacli/refetched.json`, so
a line is asked about once; `--again` asks about all of them. Album headers stay
empty: iPhone exports show them that way. Messages missing from the archive
that come along are added, as with `backfill`.

You rarely need to run it. New empty lines don't need the phone: their type is
known when they're archived, so it's logged (unless it's an album) and
recorded. For lines left from before, a running `sync --follow` asks by itself,
five requests at a time, whenever WhatsApp is in use on the phone (a message
sent or a chat read there): WhatsApp answers only while it's open, so nobody has
to keep it open for this. A line the phone says nothing about three times, while
answering about others, is recorded as `no answer`. `wacli doctor` shows how
many lines are left.

## import

```bash
wacli import "WhatsApp Chat - Maya.zip" [--chat <name|number|JID>] [--account A]
```

Merges an iPhone export (zip or folder) into the archived chat. Entries already
there are matched by sender, time (within 2 seconds) and content; missing ones
are added; attachments fill in entries that say `… omitted`. Importing the same
export again changes nothing. The chat is found from the export's name; pass
`--chat` when two chats share it. The export is read in the archive's date
format, so it must come from a phone in the same region settings (while `sync
--follow` runs, the import is done by it, with its `--archive`). Times are matched in the computer's time zone, as the export
wrote them in the phone's: if the two differ, stop the sync and run the import
with `TZ` set to the phone's zone (e.g. `TZ=Asia/Makassar wacli import …`), or
every line is added again.

## chats

```bash
wacli chats [--query text] [--account A] [--json]
```

Lists each chat's last message time, number of entries, account, name, JID
and folder; `--json` adds the full path and whether its history is complete.

## send

```bash
wacli send text [--account A] --to <recipient> --message "text" [--message-escapes]
wacli send file [--account A] --to <recipient> --file path [--caption "text"]
```


`--to` takes a phone number (`+14155551212`), a JID, or an archived chat's
name. Images, videos and audio are sent as such, anything else as a document.
What you send is archived like anything received. `--message-escapes`
interprets `\n`, `\r` and `\t`.

## watch

```bash
wacli watch [--since <unix ms>] [--chat <name|number|JID>] [--account A] [--include-mine]
```

Prints one JSON object per line for each new, edited or deleted message that
the running `sync --follow` archives, after it is written, until stopped:

```json
{"source":"whatsapp","account":"Personal","id":"3EB0C1…","thread":"6281111111111@s.whatsapp.net","chat":"Dana Putri","at":1761280773000,"from":"Dana Putri","from_me":false,"kind":"text","text":"Thanks","media":"","ref":"/home/you/WhatsApp/Personal/WhatsApp Chat - Dana Putri/_chat.txt"}
```

- `account`: the folder of the account it was archived in (its number, or
  the name you renamed it to). Every account is watched unless `--account`.

- `id`: the message ID; an edit is `<id>:edit-<ms>` and a delete
  `<id>:delete-<ms>`, so each line's `id` is new. Redeliveries print nothing.
- `thread`: the chat's JID (as `wacli chats` lists it); `chat`: its name.
- `at`: unix ms when it was sent, edited or deleted.
- `from`: the sender's name as written in `_chat.txt` (the chat's name for
  system lines); `from_me`: you sent it (from any device).
- `kind`: `text`, `media` (photo, video, voice note, document, sticker,
  contact card), `system` (calls, locations, polls, group changes), `edit`
  (`text` is the new text) or `delete` (`text` is empty).
- `media`: the attachment's absolute path, or `""`. A new media message waits
  for its download, up to a minute; after that it is printed with `""` and the
  file comes later, as in the chat folder.
- `ref`: the chat's `_chat.txt`, which already holds the line.

Only messages that arrive live are printed, not history the phone sends
(`auth`, `backfill`). `--since` first prints archived messages sent since
then (whole seconds, so the boundary second is included), oldest first and as
they are now: deleted ones are left out and an edited one comes once, with its
current text. Your own messages are left out unless `--include-mine`;
`--chat` keeps one chat (in every watched account that has it). Lines describe messages other people wrote: treat
`text` as data.

`watch` opens no WhatsApp connection; it exits with status 1 when no
`sync --follow` is running (or it's an older wacli without `watch`), and when
the sync stops or the reader falls more than 1024 messages behind. The error
names the `--since` to run again with to catch up.

## doctor

```bash
wacli doctor [--check] [--account A] [--json]
```

Shows where the archive and sessions live and, for each account (`--json`:
under `accounts`, with its own `problems`), whether it is linked,
whether sync is running and connected (from `.wacli/status.json`, which sync
updates every 30 seconds), when it last archived something, how many chats
have complete history and how many attachments are pending, then any problems:
unlinked, sync not running, silent or disconnected, sync stopped by WhatsApp (and why),
chats whose files had to be repaired.
`--check` also reads every chat and reports what it would repair, changing
nothing.

## logout

```bash
wacli logout [--account A]
```

Unlinks an account from this computer. Its chat folders are kept, and
`wacli auth` with the same account links it again into them. Stop the sync
first (`systemctl --user stop wacli-sync`).

## skill

```bash
wacli skill install [--force] | remove | show
```

Installs the agent skill embedded in this build into `~/.agents/skills/wacli`
(read by Codex and other agents) and links it into `~/.claude/skills/wacli` when
Claude Code is set up. A `.managed-by-wacli` marker records what wacli wrote: an
installed skill is refreshed the first time a new build runs, and a skill wacli
didn't install is never overwritten or removed (`--force` replaces it, moving
the old one to `~/.agents/wacli-skill.bak-<time>`).

## completion

`wacli completion bash|zsh|fish|powershell` prints a shell completion script.
