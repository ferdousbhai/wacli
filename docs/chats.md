---
title: chats
description: "List archived chats and their folders."
---

# chats

```bash
wacli chats [--query text] [--json]
```

Lists each chat's last message time, number of entries, name, JID and folder.
`--json` adds the full path and whether its history is complete. Reads only the
archive; works while `sync --follow` runs.
