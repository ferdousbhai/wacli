---
title: send
description: "Send messages and files."
---

# send

```bash
wacli send text --to <recipient> --message "text" [--message-escapes]
wacli send file --to <recipient> --file path [--caption "text"]
```

`--to` takes a phone number (`+14155551212`), a JID, or an archived chat's name.
Images, videos and audio are sent as such; anything else as a document. Sent
messages are written to the archive like received ones. While `sync --follow`
runs, the send goes through it.
