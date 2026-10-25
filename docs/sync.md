---
title: sync
description: "Archive new messages, once or continuously."
---

# sync

```bash
wacli sync [--follow] [--idle-exit 30s]
```

Connects and archives everything WhatsApp delivers: new messages, the backlog
queued while wacli was offline, edits, deletes, group changes. Without
`--follow` it stops once idle; with `--follow` it runs until stopped and
accepts work from `send`, `backfill` and `import` over a local socket.

What each event does to the folders is in [The archive](archive.md#how-chats-stay-current).

Media: an attachment appears as `‎image omitted` until its file is on disk, then
as `‎<attached: …>`. Expired media is requested again from the phone; failures
are retried with backoff and survive restarts (`.wacli/pending-media.json`).
