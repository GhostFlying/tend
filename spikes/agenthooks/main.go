// Command agenthooks checks how Claude Code, Codex and traex run hooks and
// stdio MCP servers (plan C5). See README.md.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: agenthooks hook|mcp|run ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "hook":
		os.Exit(runHook(os.Args[2:]))
	case "mcp":
		os.Exit(runMCP(os.Args[2:]))
	case "run":
		os.Exit(runScenarios(os.Args[2:]))
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}
