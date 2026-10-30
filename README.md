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
and `tail -F` them. wacli links to WhatsApp as a companion device (like
WhatsApp Desktop) using [whatsmeow](https://github.com/tulir/whatsmeow).

Started from [openclaw/wacli](https://github.com/openclaw/wacli) (MIT), which
stores messages in SQLite. [docs/archive.md](docs/archive.md) describes the
format and how folders stay current; [docs/commands.md](docs/commands.md) every
command.

## Install

On Omarchy or any Arch Linux (x86_64 or aarch64):

```bash
curl -fsSL https://ferdousbhai.com/wacli/install.sh | sudo bash
```

It adds wacli's signed pacman repository and installs the `wacli` package
(`/usr/bin/wacli` and the `wacli-sync` systemd user service); updates then
arrive with `omarchy update` (or `pacman -Syu`). Then link WhatsApp, start the
service and, for coding agents, install the skill (if your phone doesn't show
dates like 2026-10-24, 05:33:36, set `WACLI_DATE_FORMAT` before `auth`; see
Settings):

```bash
wacli auth                                  # scan the QR code with your phone
systemctl --user enable --now wacli-sync    # keep archiving in the background
loginctl enable-linger "$USER"              # keep it running while you're logged out
wacli skill install                         # optional: the agent skill (see Coding agents)
```

Elsewhere (other Linux, macOS, Windows), build it with Go 1.27+ and a C
compiler (the WhatsApp session store is SQLite):

```bash
GOBIN=~/.local/bin go install github.com/ferdousbhai/wacli/cmd/wacli@latest   # or, from a checkout: make install
```

With GCC 15+, set `CGO_CFLAGS=-Wno-error=missing-braces` (the Makefile does).
Tested on Linux (CI and daily use, with the systemd unit). It builds for macOS
and Windows but is untested there, and there's no launchd unit yet.

Moving from a `go install` build to the package? The old binary usually comes
first on `PATH`, and a unit in `~/.config/systemd/user` overrides the packaged
one, so swap them over (the archive and the link are untouched):

```bash
systemctl --user disable wacli-sync   # its enable link points at the old unit
rm -f ~/.local/bin/wacli ~/.config/systemd/user/wacli-sync.service
systemctl --user daemon-reload
systemctl --user enable wacli-sync && systemctl --user restart wacli-sync
```

Coming from [openclaw/wacli](https://github.com/openclaw/wacli)? This wacli
uses the same session store, so it starts out linked, but a link made by the
old one didn't ask for full history: run `wacli logout` and `wacli auth` to
archive everything. The old `wacli.db` isn't read and can be deleted.

## Use

```bash
# First, if your phone doesn't show dates like 2026-10-24, 05:33:36, set
# WACLI_DATE_FORMAT (see Settings); the archive keeps the one it starts with.
wacli auth                  # scan the QR code; archives your full history and media
wacli sync --follow         # keep archiving as messages arrive (run it as a service)
wacli chats                 # where each chat's folder is
wacli backfill --all        # ask the phone for anything older it still has
wacli import "WhatsApp Chat - Maya.zip"   # merge a phone export for anything it won't send
wacli send text --to "+14155551212" --message "Running late"
wacli send file --to "Maya" --file invoice.pdf --caption "Invoice"
wacli doctor
```

`send`, `backfill`, `refetch` and `import` hand their work to a running `sync --follow`,
so they work while the service is up and what you send lands in the archive.

The Arch package ships the `wacli-sync` service (enable it as above). With a
`go install` build, add it as a systemd user service yourself:

```bash
mkdir -p ~/.config/systemd/user
curl -fsSL -o ~/.config/systemd/user/wacli-sync.service \
  https://raw.githubusercontent.com/ferdousbhai/wacli/main/contrib/systemd/wacli-sync.service
# (or, from a checkout: cp contrib/systemd/wacli-sync.service ~/.config/systemd/user/)
systemctl --user daemon-reload
systemctl --user enable --now wacli-sync   # runs ~/.local/bin/wacli
loginctl enable-linger "$USER"   # keep it running while you're logged out
```

`wacli doctor` shows whether it is connected and archiving.

## Coding agents

wacli ships with an agent skill (`skills/wacli/SKILL.md`) that tells coding
agents where the chat folders are, how to read them, and how to send safely:

```bash
wacli skill install   # ~/.agents/skills/wacli (Codex and others), linked into ~/.claude/skills for Claude Code
wacli skill show      # print it
wacli skill remove
```

An installed skill updates itself when wacli is updated, and wacli never
overwrites a skill it didn't install (`--force` adopts one, keeping a backup).
`wacli doctor` reports its state.

## Settings

| Flag | Env | Default |
|---|---|---|
| `--archive` | `WACLI_ARCHIVE_DIR` | `~/WhatsApp` |
| `--store` (session) | `WACLI_STORE_DIR` | `~/.local/state/wacli` on Linux, `~/.wacli` elsewhere |
| `--date-format` | `WACLI_DATE_FORMAT` | `2006-01-02, 15:04:05` (Go layout; match your phone's region; set it for the first `auth`, the archive then keeps it) |

Times are written in the computer's time zone, as the phone writes them in its own.

## Limits

- WhatsApp decides how much history a linked device gets. `auth` asks for the
  full history; `backfill` asks for more page by page; what the phone won't
  send, `import` merges from a manual export.
- Like a real export, the archive has no reactions, quoted replies or read
  receipts, and edits keep only the latest text.
- WhatsApp Web is not a public API. Use at your own risk.

MIT licensed.
