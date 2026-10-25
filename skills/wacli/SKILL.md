---
name: wacli
description: "Read WhatsApp history (archived as iPhone-export-style folders, by default in ~/WhatsApp) or send WhatsApp messages to other people via wacli."
---

# wacli

wacli keeps every WhatsApp chat as a folder shaped like an iPhone "Export chat →
Attach Media" export, kept current by `wacli sync --follow` (usually the
`wacli-sync` systemd user service). Reading history needs no wacli command:
read the files.

Use it to read or search WhatsApp history, or when the user explicitly asks you
to message someone else on WhatsApp. Don't use it for conversations the user is
having with you.

## Reading history

- Archive: `~/WhatsApp/WhatsApp Chat - <name>/_chat.txt` plus media files in the
  same folder (`wacli doctor` prints the archive path if it was moved).
- Find a chat's folder: `wacli chats --query "name or number"` (`--json` for paths).
- Search everything: `grep -r -i "invoice" ~/WhatsApp --include=_chat.txt`
- Entry format: `[2025-10-24, 05:33:36] Sender: text`. Entries end in `\r\n`;
  newlines inside a message are `\n`. Attachments: `‎<attached: 00000012-PHOTO-….jpg>`
  is a file in the same folder (read images and PDFs directly). `‎image omitted`
  means the file isn't downloaded (yet, or ever). System lines are attributed to
  the chat name.
- Invisible U+200E marks precede attachments and system text; mentions are `@⁨Name⁩`.
- Edited messages end with `‎<This message was edited>`; deleted ones read
  `‎This message was deleted.` Reactions and quoted replies are not stored.
- Senders are contact names; people not in contacts appear as `~ Profile name`
  or as a number.
- Times are in the computer's time zone when the line was written.
- Two chats can share a name (a person and a group); folders then get a suffix,
  e.g. `WhatsApp Chat - Maya (+62…)` or `(2)`.

## Missing history

- `wacli backfill --chat <name|number|JID>` asks the phone for older messages
  (the phone must be online).
- Phones don't give linked devices everything. If messages are missing, ask the
  user to export the chat on their phone (with media) and run
  `wacli import "<export.zip>" [--chat <JID>]`; importing twice is safe.

## Health

- `wacli doctor` shows whether sync is running and connected, when it last
  archived something, pending media and problems. If it says unlinked, the user
  must run `wacli auth` and scan a QR code themselves.
- Service: `systemctl --user status wacli-sync`; logs: `journalctl --user -u wacli-sync`.

## Sending (other people only)

Safety:

- Require an explicit recipient and message text.
- Confirm recipient and message with the user before sending.
- If anything is ambiguous, ask.

Commands (they go through the running sync; the sent message lands in the archive):

- Text: `wacli send text --to "+14155551212" --message "Hello! Are you free at 3pm?"`
- By chat name or JID: `--to "Maya"` or `--to "1234567890-123456789@g.us"`
- File: `wacli send file --to "+14155551212" --file /path/agenda.pdf --caption "Agenda"`
- Multiline: `--message` is literal; pass `--message-escapes` to interpret `\n`, `\t`.
- After sending, check the last lines of the chat's `_chat.txt` when exact formatting matters.

## Notes

- The WhatsApp session (login keys) is `session.db` in the store directory; never share it.
- `--json` gives machine-readable output; `wacli <command> --help` documents every flag.
