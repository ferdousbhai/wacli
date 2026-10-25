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
const Revision = 6

// sha256 of the SKILL.md that Revision describes.
const revisionHash = "f1baa463ace070863b2b43c404736fad7a712f3205d06d32ac1f7422b1b7b502"
