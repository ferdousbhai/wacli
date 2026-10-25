---
title: Overview
permalink: /
description: "wacli keeps WhatsApp chats as iPhone-style export folders (_chat.txt plus media), kept current as messages arrive."
---

# wacli

wacli links to WhatsApp as a companion device and keeps every chat as a folder
shaped exactly like an iPhone **Export chat → Attach Media**: a `_chat.txt`
and the attached files. The folders are the data; there is no database to
query. Humans open them, agents `cat`, `grep -r` and `tail -f` them.

- [Install](install.md) and [Quickstart](quickstart.md)
- [The archive](archive.md): the format and how folders stay current
- Commands: [auth](auth.md), [sync](sync.md), [backfill](backfill.md),
  [import](import.md), [chats](chats.md), [send](send.md), [doctor](doctor.md)

Hard fork of [openclaw/wacli](https://github.com/openclaw/wacli). WhatsApp Web
is not a published API; expect occasional breakage. Released under the
[MIT license](https://github.com/ferdousbhai/wacli/blob/main/LICENSE).
