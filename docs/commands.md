# Commands

Global flags: `--archive` (`WACLI_ARCHIVE_DIR`, default `~/WhatsApp`), `--store`
(`WACLI_STORE_DIR`, the session), `--date-format` (`WACLI_DATE_FORMAT`, a Go
time layout matching your phone's region), `--json`.

One process owns the WhatsApp session at a time. While `sync --follow` runs,
`send`, `backfill` and `import` hand their work to it; stopping the client
(Ctrl+C) stops that work. `chats` and `doctor` only read, and work any time.

## auth

```bash
wacli auth [--phone +NUMBER] [--history-days N] [--idle-exit 3m]
```

Links this computer as a companion device (QR code, or a pairing code with
`--phone`) and archives what the phone sends, with media. It asks for a
**full** history sync, as WhatsApp Desktop does. That request is part of the
linking handshake, so it only applies to a new link: run `wacli logout` first
to relink. History arrives in batches over several minutes; keep the phone
online. `--history-days` limits how far back; `--idle-exit` is how long to wait
after the last batch.

## sync

```bash
wacli sync [--follow] [--idle-exit 30s]
```

Archives everything WhatsApp delivers: new messages, the backlog queued while
wacli was offline, edits, deletes, group changes, and downloads media. Without
`--follow` it stops once idle. With `--follow` it runs until stopped (normally
as the `wacli-sync` systemd user service; see the README). If WhatsApp unlinks
this computer, it exits with status 3 so the service stops restarting.

## backfill

```bash
wacli backfill --chat <name|number|JID> | --all [--count 50] [--wait 1m]
```

Asks the phone for older history, page by page, starting from the newest
message and following the phone's own order, so gaps in the middle of a chat
are filled too. Progress is saved per chat, and a chat is marked complete when
the phone has nothing more. The phone must be online. Phones don't hand linked
devices everything; for the rest, use `import`.

## import

```bash
wacli import "WhatsApp Chat - Maya.zip" [--chat <name|number|JID>]
```

Merges an iPhone export (zip or folder) into the archived chat. Entries already
there are matched by sender, time (within 2 seconds) and content; missing ones
are added; attachments fill in entries that say `… omitted`. Importing the same
export again changes nothing. The chat is found from the export's name; pass
`--chat` when two chats share it. `--date-format` must match the export.

## chats

```bash
wacli chats [--query text] [--json]
```

Lists each chat's last message time, number of entries, name, JID and folder;
`--json` adds the full path and whether its history is complete.

## send

```bash
wacli send text --to <recipient> --message "text" [--message-escapes]
wacli send file --to <recipient> --file path [--caption "text"]
```

`--to` takes a phone number (`+14155551212`), a JID, or an archived chat's
name. Images, videos and audio are sent as such, anything else as a document.
What you send is archived like anything received. `--message-escapes`
interprets `\n`, `\r` and `\t`.

## doctor

```bash
wacli doctor [--check] [--json]
```

Shows where the archive and session live, whether this computer is linked,
whether sync is running and connected (from `.wacli/status.json`, which sync
updates every 30 seconds), when it last archived something, how many chats
have complete history and how many attachments are pending, then any problems:
unlinked, sync silent or disconnected, chats whose files had to be repaired.
`--check` also reads every chat and reports what it would repair, changing
nothing.

## logout

Unlinks this computer. The chat folders are kept.

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
