// Command tend is the owner command line. All logic is in internal/cli.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/protocol"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	home, _ := os.UserHomeDir()
	configDir := os.Getenv(cli.ConfigDirEnv)
	if configDir == "" {
		configDir = filepath.Join(home, ".config", "tend")
	}
	e := &cli.Env{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv,
		Home: home, ConfigDir: configDir,
		HTTP:            &http.Client{Timeout: 60 * time.Second, Transport: cli.Transport()},
		IDs:             protocol.NewULIDGen(time.Now, nil),
		ImportExtension: extensions.Import,
	}
	binary, _ := os.Executable()
	e.CodexEnvironment = func(ctx context.Context, q protocol.LocalCodexEnvironmentRequest) (json.RawMessage, error) {
		s := codexenv.Service{Root: q.Root, UserHome: home, Binary: binary}
		return s.Local(ctx, q)
	}
	os.Exit(cli.Main(ctx, e, os.Args[1:]))
}
