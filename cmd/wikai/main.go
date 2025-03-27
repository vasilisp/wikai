package main

import (
	"fmt"
	"os"

	"github.com/vasilisp/wikai/internal/cli"
	"github.com/vasilisp/wikai/internal/server"
)

func main() {
	if len(os.Args) == 1 {
		server.Main()
		return
	}

	switch os.Args[1] {
	case "cli":
		cli.Main(os.Args[2:])
	case "index":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: %s index <paths>\n", os.Args[0])
			os.Exit(1)
		}
		server.Index(os.Args[2:])
	case "server":
		server.Main()
	}
}
