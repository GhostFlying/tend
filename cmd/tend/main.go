// Command tend is the owner's command line and the agent add-on's bridge
// (D46). All logic is in internal/cli.
package main

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/protocol"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	home, _ := os.UserHomeDir()
	configDir := os.Getenv(cli.ConfigDirEnv)
	if configDir == "" {
		configDir = filepath.Join(home, ".config", "tend")
	}
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	if p, err := filepath.EvalSymlinks(self); err == nil {
		self = p
	}
	e := &cli.Env{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv,
		Home: home, ConfigDir: configDir,
		HTTP:       &http.Client{Timeout: 60 * time.Second},
		IDs:        protocol.NewULIDGen(time.Now, nil),
		Executable: self, LookPath: exec.LookPath,
		Run: func(ctx context.Context, name string, args ...string) error {
			c := exec.CommandContext(ctx, name, args...)
			c.Stdout, c.Stderr = os.Stderr, os.Stderr
			return c.Run()
		},
	}
	os.Exit(cli.Main(ctx, e, os.Args[1:]))
}
