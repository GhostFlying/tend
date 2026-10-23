package main

import (
	"fmt"
	"os"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/protocol/compatcheck"
)

func main() {
	if err := compatcheck.Check(".", protocol.CurrentRunnerProtocol); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
