// Package skills embeds the canonical Workbook agent skill so that Workbook
// can install it into a project without maintaining a second copy.
//
// The embedding package lives in this directory because go:embed patterns
// cannot traverse upwards. A Go file alongside the skill package does not
// affect skill discovery.
package skills

import _ "embed"

// SkillMarkdown is the canonical Workbook skill definition.
//
//go:embed workbook/SKILL.md
var SkillMarkdown string
