---
title: Install
description: "Build wacli from source with Go and cgo."
---

# Install

wacli needs Go 1.27+ and a C toolchain: the WhatsApp session store is SQLite
(`go-sqlite3`), so builds require cgo.

```bash
CGO_ENABLED=1 go install -tags sqlite_fts5 github.com/ferdousbhai/wacli/cmd/wacli@latest
wacli --version
```

From a checkout:

```bash
git clone https://github.com/ferdousbhai/wacli.git
cd wacli
pnpm build        # → dist/wacli
```

With GCC 15+, set `CGO_CFLAGS=-Wno-error=missing-braces` (`pnpm build` does).
