---
title: Quickstart
description: "Link, archive your history, keep it current."
---

# Quickstart

```bash
wacli auth
```

Scan the QR code in WhatsApp → **Linked devices → Link a device** (or use
`--phone +14155551212` for a pairing code). wacli asks the phone for its full
history and writes every chat to `~/WhatsApp/WhatsApp Chat - <name>/` with all
media. Keep the phone online until it says it's done.

```bash
wacli sync --follow
```

Keeps archiving as messages arrive. Run it as a service:
`contrib/systemd/wacli-sync.service`.

```bash
wacli chats --query maya
cat ~/WhatsApp/"WhatsApp Chat - Maya"/_chat.txt
grep -r "invoice" ~/WhatsApp
```

Missing older messages? `wacli backfill --chat Maya`. Still missing (the phone
won't send everything to linked devices)? Export the chat on the phone and
`wacli import "WhatsApp Chat - Maya.zip"`.
