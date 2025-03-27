package data

import (
	_ "embed"
)

//go:embed prompt.txt
var SystemPrompt string

//go:embed prompt_summarize.txt
var SystemPromptSummarize string

//go:embed index.html
var IndexHTML []byte

//go:embed style.css
var StyleCSS []byte

//go:embed wikitemplate.html
var WikiTemplate []byte
