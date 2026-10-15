package main

import (
	"testing"

	"github.com/GhostFlying/tend/internal/cfgfile"
)

func TestExampleConfigLoads(t *testing.T) {
	var c config
	if err := cfgfile.Load("../../docs/examples/tend-runner.hujson", &c); err != nil {
		t.Fatal(err)
	}
	if c.HostID == "" || len(c.Repos["tend"].Profiles) != 1 {
		t.Errorf("example config = %+v", c)
	}
}
