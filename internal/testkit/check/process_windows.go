//go:build windows

package main

import (
	"github.com/GhostFlying/tend/internal/procx"
	"os/exec"
)

func containTask(cmd *exec.Cmd) { procx.Contain(cmd) }
