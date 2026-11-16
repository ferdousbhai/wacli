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
const Revision = 9

// sha256 of the SKILL.md that Revision describes.
const revisionHash = "57541d552bf1ac39a674c4858cede7032a6ac2e12655a0053bc911d1ffb2c85f"
