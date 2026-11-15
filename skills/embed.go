// Package skills holds the agent skill that ships with wacli.
package skills

import _ "embed"

// Wacli is skills/wacli/SKILL.md, installed by `wacli skill install`.
//
//go:embed wacli/SKILL.md
var Wacli []byte

// Revision numbers the skill's content. An installed skill is refreshed
// only by a wacli carrying a higher revision, so an older binary never
// downgrades it. Bump it with every change to SKILL.md (a test checks).
const Revision = 8

// sha256 of the SKILL.md that Revision describes.
const revisionHash = "65149c69dc52632a218cd1cffc9f0be363a9f587dedd7db02b220eaeee74b4f3"
