package data

import (
	_ "embed"
)

//go:embed prompt.txt
var SystemPrompt string

//go:embed index.html
var IndexHTML []byte
