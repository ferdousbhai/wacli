---
title: auth
description: "Link to WhatsApp and archive your full history."
---

# auth

```bash
wacli auth [--phone +NUMBER] [--history-days N] [--idle-exit 3m]
```

Links this computer as a companion device and archives what the phone sends.
wacli asks for a **full** history sync (as WhatsApp Desktop does), not the
recent window: that request travels in the linking handshake, so it only takes
effect on a new link. To relink an existing session with full history, run
`wacli logout` first.

- `--phone`: print a pairing code instead of a QR code.
- `--history-days`: limit the history requested (0 = everything).
- `--idle-exit`: finish once nothing new arrived for this long. History comes
  in batches over several minutes; keep the phone online.

Media is downloaded as entries arrive; see [sync](sync.md) for retries.
