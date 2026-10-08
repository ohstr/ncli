// Package skills embeds the agent skills shipped in this directory, so
// `ncli skills` can serve them from the binary alone.
package skills

import "embed"

// FS holds every skill directory (ncli-*/SKILL.md plus its references/).
//
//go:embed ncli-*
var FS embed.FS
