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
const revisionHash = "69afb9586f0a478656c8ac3eb6a57d91a86501ba7f2a50419a8a997d5401f141"
