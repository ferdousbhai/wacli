---
title: doctor
description: "Check link status, sync health and the archive."
---

# doctor

```bash
wacli doctor [--check] [--json]
```

Shows where the archive and session live, whether this computer is linked,
whether `sync` is running and connected, when it last archived something, how
many chats have complete history, and how many attachments are pending. It
reads `.wacli/status.json`, which a running sync updates every 30 seconds.

Problems are listed at the end: WhatsApp unlinked this computer (sync exits
with status 3 so a service stops restarting; run `wacli auth`), sync running
but silent or disconnected, chats whose index had to be rebuilt.

`--check` opens every chat and verifies its files; stop sync first.
