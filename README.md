# wacli

Keep your WhatsApp chats as plain folders, shaped exactly like the iPhone's
**Export chat → Attach Media**, and kept up to date as messages arrive.

```
~/WhatsApp/
  WhatsApp Chat - Maya/
    _chat.txt
    00000039-Restructuring Invoice - PT. Acme Holding.pdf
    00000040-PHOTO-2025-10-25-04-22-21.jpg
  WhatsApp Chat - Sam Lee/
    _chat.txt
    …
```

```
[2025-10-25, 04:22:21] Alex: 🙏 ‎<attached: 00000040-PHOTO-2025-10-25-04-22-21.jpg>
[2025-10-25, 04:23:15] Maya: Thank u Alex ‎<This message was edited>
```

There is no database to query. People open the folders; agents `cat`, `grep -r`
and `tail -f` them. wacli links to WhatsApp as a companion device (like
WhatsApp Desktop) using [whatsmeow](https://github.com/tulir/whatsmeow).

This is a hard fork of [openclaw/wacli](https://github.com/openclaw/wacli),
which stores messages in SQLite. See [docs/archive.md](docs/archive.md) for the
format and how folders stay current.

## Install

Needs Go 1.27+ and a C compiler (the WhatsApp session store is SQLite):

```bash
CGO_ENABLED=1 go install -tags sqlite_fts5 github.com/ferdousbhai/wacli/cmd/wacli@latest
```

## Use

```bash
wacli auth                  # scan the QR code; archives your full history and media
wacli sync --follow         # keep archiving as messages arrive (run it as a service)
wacli chats                 # where each chat's folder is
wacli backfill --all        # ask the phone for anything older it still has
wacli import "WhatsApp Chat - Maya.zip"   # merge a phone export for anything it won't send
wacli send text --to "+14155551212" --message "Running late"
wacli send file --to "Maya" --file invoice.pdf --caption "Invoice"
wacli doctor
```

`send`, `backfill` and `import` hand their work to a running `sync --follow`,
so they work while the service is up and what you send lands in the archive.

To run sync as a systemd user service:

```bash
cp contrib/systemd/wacli-sync.service ~/.config/systemd/user/   # runs ~/.local/bin/wacli
systemctl --user enable --now wacli-sync
```

## Settings

| Flag | Env | Default |
|---|---|---|
| `--archive` | `WACLI_ARCHIVE_DIR` | `~/WhatsApp` |
| `--store` (session) | `WACLI_STORE_DIR` | `~/.local/state/wacli` on Linux, `~/.wacli` elsewhere |
| `--date-format` | `WACLI_DATE_FORMAT` | `2006-01-02, 15:04:05` (Go layout; match your phone's region) |

Times are written in the computer's time zone, as the phone writes them in its own.

## Limits

- WhatsApp decides how much history a linked device gets. `auth` asks for the
  full history; `backfill` asks for more page by page; what the phone won't
  send, `import` merges from a manual export.
- Like a real export, the archive has no reactions, quoted replies or read
  receipts, and edits keep only the latest text.
- WhatsApp Web is not a public API. Use at your own risk.

MIT licensed.
