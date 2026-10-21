package main

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/cfgfile"
)

func TestExampleConfigLoads(t *testing.T) {
	var c config
	if err := cfgfile.Load("../../docs/examples/tend-server.hujson", &c); err != nil {
		t.Fatal(err)
	}
	if c.Tailnet == nil || c.MainAgent != nil || len(c.Hosts) != 1 || c.Queue.WaitPerHour == nil || *c.Queue.WaitPerHour != 0.1 ||
		c.HostLostAfter == nil || time.Duration(*c.HostLostAfter) != 24*time.Hour {
		t.Errorf("example config = %+v", c)
	}
}
