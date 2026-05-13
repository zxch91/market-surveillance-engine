package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <watch-markets|check-runners|monitor>\n", os.Args[0])
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "watch-markets":
		err = runWatchMarkets()
	case "check-runners":
		err = runCheckRunners()
	case "monitor":
		err = runMonitor()
	case "wh-explore":
		err = runWHExplore()
	case "wh-monitor":
		err = runWHMonitor()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
