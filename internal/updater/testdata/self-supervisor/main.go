// Self-supervisor is an opt-in fixture, never a distribution component. It
// behaves like a legacy supervisor with child-retaining shutdown and no modern
// self-update capability, using only the test's private configuration and unit.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/GhostFlying/tend/internal/updater"
)

// An ordinary service operation can be staged, but this fixture never grants
// permission to execute it. This permits testing bootstrap ownership races.
type deniedAuthority struct{}

func (deniedAuthority) Authorize(context.Context, updater.Binding) (updater.Grant, error) {
	return updater.Grant{}, nil
}

func (deniedAuthority) Health(context.Context, updater.Binding) (updater.Health, error) {
	return updater.Health{}, nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		os.Exit(2)
	}
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	path := flags.String("config", "", "private fixture configuration")
	_ = flags.Parse(os.Args[2:])
	raw, err := os.ReadFile(*path)
	if err != nil {
		panic(err)
	}
	var cfg updater.Config
	if err = json.Unmarshal(raw, &cfg); err != nil {
		panic(err)
	}
	supervisor, err := updater.Open(cfg, updater.Options{Authority: deniedAuthority{}})
	if err != nil {
		panic(err)
	}
	defer supervisor.Close()
	if supervisor.Status().Current.Binary == "" {
		child := exec.Command(cfg.InitialBinary, cfg.Args...)
		child.Env = append(os.Environ(), "TEND_UPDATER_PROCESS_INSTANCE_ID="+cfg.InstanceID+"-retained", "TEND_UPDATER_LAUNCH_ID="+cfg.InstanceID+"-retained")
		if err = child.Start(); err != nil {
			panic(err)
		}
		if err = supervisor.Bootstrap(ctx, child.Process.Pid); err != nil {
			_ = child.Process.Kill()
			panic(err)
		}
	}
	if err = supervisor.Serve(ctx); err != nil {
		panic(err)
	}
}
