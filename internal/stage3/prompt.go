package stage3

import (
	_ "embed"
	"strings"
)

const MaxSystemPromptBytes = 64 << 10

//go:embed default_prompt.txt
var bundledDefaultPrompt string

func BundledDefaultPrompt() string {
	return strings.TrimSpace(bundledDefaultPrompt)
}
