# Export archive

wacli keeps every WhatsApp chat as a folder shaped exactly like an iPhone
"Export chat → Attach Media" export, and keeps those folders current while
messages keep arriving. There is no message database: the folders are the data.

## Layout

```
~/WhatsApp/                                   archive root (--archive, WACLI_ARCHIVE_DIR)
  WhatsApp Chat - Maya/
    _chat.txt
    00000012-PHOTO-2025-10-24-05-33-09.jpg
    00000039-Restructuring Invoice - PT. Acme Holding.pdf
  WhatsApp Chat - Maya (+628110000000)/       name collision → number suffix
    _chat.txt
  .wacli/                                     bookkeeping, never needed to read chats
    chats.json                                JID → folder, chat kind, names
    index/<jid>.tsv                           one row per _chat.txt entry, same order:
                                              msg id, unix ts, from_me, sender JID
    index/<jid>.tsv.orphaned-<time>           index set aside when a chat folder went missing
    pending-media.json                        media not downloaded yet (keys to retry), and media given up on
    status.json                               written by a running sync; read by `wacli doctor`
    deleted/<folder>/                         media of messages later deleted for everyone
    names-scanned                             marks the one-time pass that named old chats
    tmp/                                      downloads and file writes in progress
~/.local/state/wacli/                         session store (--store, WACLI_STORE_DIR)
  session.db                                  WhatsApp login + Signal keys (whatsmeow)
  LOCK                                        held by the one process using the session
  .send.sock                                  where a running sync takes send/backfill/import
```

The index exists only because WhatsApp refers to earlier messages by ID (edits,
deletes, backfill anchors) and the export format has no IDs. Row *i* of the
index describes entry *i* of `_chat.txt`.

## `_chat.txt` format (iOS)

Matched against a real iOS export (`internal/archive/testdata`).

- Entry: `[2025-10-24, 05:33:36] Sender: text` — date format from the phone's
  locale (`--date-format`, default `2006-01-02, 15:04:05`), local time zone.
- Entries end in `\r\n`; newlines inside a message are bare `\n`.
- Attachment: `‎<attached: NNNNNNNN-PHOTO-YYYY-MM-DD-HH-MM-SS.jpg>`, caption
  first on the same line; the entry line itself is prefixed with U+200E.
  Kinds: PHOTO .jpg, VIDEO .mp4, AUDIO .opus, STICKER .webp, GIF .mp4;
  documents keep their filename (`NNNNNNNN-<name>`) and are preceded by
  `<name> • ‎<n> pages`.
- `NNNNNNNN` is the entry's 0-based position when it was first written and
  never changes afterwards (a later backfill can make numbers non-monotonic).
  Files merged by `import` keep the export's own names.
- System lines are attributed to the chat name and prefixed with U+200E:
  `‎You created group “…”`, `‎Messages and calls are end-to-end encrypted…`,
  `‎Voice call, ‎‎32 min • ‎3 joined`.
- Edited: `text ‎<This message was edited>`. Deleted: `‎This message was
  deleted.` / `‎You deleted this message.`
- Mentions: `@⁨Name⁩` (U+2068 … U+2069).
- Locations: `‎Location: https://maps.google.com/?q=<lat>,<lng>`.
- Senders are the names in your contacts; people not in them appear as
  `~ Profile name` or as their number, as on the phone. Your own messages use
  your profile name. Names learned later replace numbers already written.
- Reactions, quotes, receipts, starred state: not exported, not stored.

## How chats stay current

`wacli sync --follow` (normally a systemd user service) is the only writer.

| Event | What changes on disk |
|---|---|
| New message, newest in chat (the normal case) | One rendered entry is appended to `_chat.txt` with a single `write` (fast path), one row appended to the index. |
| New message older than the last entry (offline backlog, history sync, backfill) | Entry is inserted in timestamp order; `_chat.txt` and index are rewritten to a temp file and renamed over the original, so readers never see a partial file. |
| Same message again (history overlap, redelivery) | Kept once: its ID is already in the index. Only an entry archived empty gets the text, and media still "omitted" but no longer queued is queued again. |
| Media message | The entry is written as `‎image omitted` (or video, audio, …) and the file is queued in `pending-media.json`. A download worker fetches it (from WhatsApp's servers, else by asking the phone to re-upload) and rewrites the entry to `‎<attached: …>`. A failed download is tried twice more (after 15 minutes, then 2 hours), then given up; media the phone no longer has, or that is given up on, stays "omitted" (`wacli import` of a phone export can fill it in). |
| Edit | Entry located through the index, text replaced, `‎<This message was edited>` appended, file rewritten atomically. |
| Delete for everyone | Entry becomes `‎This message was deleted.`; its media file moves to `.wacli/deleted/`, keeping the chat folder identical to an export. |
| Reaction, receipt, typing, presence | Ignored. |
| Group renamed | System entry `‎X changed the group name to “Y”`. The folder keeps its name, so saved paths keep working. |
| Other group changes, calls | System entries as iOS writes them: created, added, removed, left, joined using the invite link, description or icon changed, missed and finished calls (call records come only from your own phone). |
| Edit or delete naming someone else's message | Ignored, unless it deletes as a group admin: only a message's sender may change it. A live edit of a message not archived yet is ignored too (the original arrives with history). |
| Deleted before wacli saw it | `‎This message was deleted.` (or `‎You deleted this message.`) at the original time (from history) or the time of deletion (live); a message an admin deleted keeps its sender. |
| New chat | Folder created. Until the contact's or group's name is known (right after linking) it is named by number; it is renamed once the name arrives. |

Writes during a history sync are batched: each chat is written once per batch,
not once per message.

If `_chat.txt` and its index ever disagree (a crash between the two writes, a
hand edit), the index is rebuilt on load by pairing rows with entries in time
order; entries that can't be paired keep working but can no longer be edited
or deleted by WhatsApp. `wacli doctor` lists such chats.

Timestamps are written in the computer's time zone at the time of writing.

Gaps:

- **wacli offline for a while**: the server queues messages for the linked
  device and replays them on reconnect; they go through the insert path above.
- **Missing older history**: `wacli backfill` walks backwards from the newest
  message, page by page; each request starts after the last message of the
  previous page in the phone's own order. That order isn't always time order,
  so this also fills gaps in the middle of a chat.
- **Anything the phone won't send**: phones don't hand linked devices every
  stretch of history. `wacli import <export.zip>` merges a phone export into the
  archive, matching entries by time, sender and text.

Readers need nothing from wacli: `cat`, `grep -r`, `tail -f _chat.txt`, or an
editor. `wacli send`, `backfill` and `import` go through the running sync over
its socket, so what they change lands in the archive like anything else.
