package main

import (
	"os"

	"github.com/vasilisp/wikai/internal/cli"
	"github.com/vasilisp/wikai/internal/server"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "cli" {
		cli.Main(os.Args[2:])
		return
	}

	server.Main()
}
