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
const Revision = 1

// sha256 of the SKILL.md that Revision describes.
const revisionHash = "825b1418568339a0f29e6772170b0f846126e483df2edfe173121bddb3fd1eba"
