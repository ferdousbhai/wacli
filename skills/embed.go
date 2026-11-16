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
const Revision = 10

// sha256 of the SKILL.md that Revision describes.
const revisionHash = "1519502b082dc80e91bb1f99c5cb842292f8c4a44dbf8db09395fbeb20bc1248"
