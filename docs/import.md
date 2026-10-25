---
title: import
description: "Merge a chat exported from the phone."
---

# import

```bash
wacli import "WhatsApp Chat - Maya.zip" [--chat <name|number|JID>]
```

Merges an iPhone export (zip or folder) into the archived chat. Entries already
there are matched by sender and time (within 2 seconds) and content; missing
ones are added; attachments fill in entries that say `… omitted`. Importing the
same export twice changes nothing.

The chat is found from the export's name; pass `--chat` when two chats share it
(a person and a group both called "Maya"). `--date-format` must match the
export's timestamps.
