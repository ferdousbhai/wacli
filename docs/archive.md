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
  WhatsApp Chat - Maya (+62 811-0000-0000)/  name collision → number suffix
    _chat.txt
  .wacli/                                     bookkeeping, never needed to read chats
    chats.json                                JID → folder, chat kind, names
    index/<jid>.tsv                           one row per _chat.txt entry, same order:
                                              msg id, unix ts, from_me, sender JID
    pending-media.json                        media not downloaded yet (keys to retry)
    status.json                               written by a running sync; read by `wacli doctor`
    deleted/                                  media of messages later deleted for everyone
    LOCK
~/.local/state/wacli/session.db               WhatsApp login + Signal keys (whatsmeow)
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
- `NNNNNNNN` is the entry's 0-based position at the time the file is saved and
  never changes afterwards (a later backfill can make numbers non-monotonic).
- System lines are attributed to the chat name and prefixed with U+200E:
  `‎You created group “…”`, `‎Messages and calls are end-to-end encrypted…`,
  `‎Voice call, ‎‎32 min • ‎3 joined`.
- Edited: `text ‎<This message was edited>`. Deleted: `‎This message was
  deleted.` / `‎You deleted this message.`
- Mentions: `@⁨Name⁩` (U+2068 … U+2069).
- Reactions, quotes, receipts, starred state: not exported, not stored.

## How chats stay current

`wacli sync --follow` (normally a systemd user service) is the only writer.

| Event | What changes on disk |
|---|---|
| New message, newest in chat (the normal case) | One rendered entry is appended to `_chat.txt` with a single `write` (fast path), one row appended to the index. |
| New message older than the last entry (offline backlog, history sync, backfill) | Entry is inserted in timestamp order; `_chat.txt` and index are rewritten to a temp file and renamed over the original, so readers never see a partial file. |
| Same message again (history overlap, redelivery) | Ignored: its ID is already in the index. |
| Media message | Media is downloaded first, then the entry is written with `<attached: …>`. If the download fails (expired on the CDN, offline) the entry is written as `‎image omitted` and queued in `pending-media.jsonl`; a background retry (CDN, then asking the phone to re-upload) rewrites the entry to `<attached: …>` once the file exists. |
| Edit | Entry located through the index, text replaced, `‎<This message was edited>` appended, file rewritten atomically. |
| Delete for everyone | Entry becomes `‎This message was deleted.`; its media file moves to `.wacli/deleted/`, keeping the chat folder identical to an export. |
| Reaction, receipt, typing, presence | Ignored. |
| Group renamed | System entry `‎X changed the group name to “Y”`. Folder name stays put, so paths that humans and agents saved keep working. |
| New chat | Folder created with the encryption notice (and `‎… created group` for groups). |

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
  message using a per-chat cursor (the oldest message of the last contiguous
  batch), not the oldest message on disk. A stray old system message therefore
  can't hide a gap in the middle of a chat.
- **Anything the protocol won't return**: `wacli import <export.zip>` merges a
  phone export into the archive, matching entries by time, sender and text.

Readers need nothing from wacli: `cat`, `grep -r`, `tail -f _chat.txt`, or an
editor. Sends (`wacli send …`) go through the running sync process over
`.wacli/sync.sock`, so the sent message lands in the archive like any other.
