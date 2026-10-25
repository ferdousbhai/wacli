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
const Revision = 3

// sha256 of the SKILL.md that Revision describes.
const revisionHash = "9bccc3bf2eacbb2afb095888ff84cd835d98c1f8c19de39d182d947e55b0d22b"
