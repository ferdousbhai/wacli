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
const Revision = 5

// sha256 of the SKILL.md that Revision describes.
const revisionHash = "4847c1f669a92f0b43928c89a59e401bb8d502e48cfbbc020b4f807f565d0cd7"
