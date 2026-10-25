---
title: backfill
description: "Ask the phone for older history."
---

# backfill

```bash
wacli backfill --chat <name|number|JID> | --all [--count 50] [--wait 1m]
```

Requests older history page by page, starting from the newest message and
following the phone's own order, so gaps in the middle of a chat are filled
too. Progress is saved per chat; a chat is marked complete when the phone has
nothing more (`wacli chats` shows it). The phone must be online.

Phones don't always hand everything to linked devices. For what's left, use
[import](import.md).
