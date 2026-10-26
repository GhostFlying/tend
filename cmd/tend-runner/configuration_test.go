package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigurationValidatorPreservesServiceIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	dir := t.TempDir()
	cli := filepath.Join(dir, "cli")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	before := filepath.Join(dir, "before")
	after := filepath.Join(dir, "after")
	base := config{HostID: "host-one", ServerURL: "https://example.invalid"}
	write := func(p string, c config) {
		t.Helper()
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(before, base)
	changed := base
	changed.DeliveryObserver = deliveryObserverConfig{Providers: []string{"github"}, GitHubBin: cli}
	write(after, changed)
	if err := checkConfiguration(before, after); err != nil {
		t.Fatal(err)
	}
	changed.HostID = "another-host"
	write(after, changed)
	if err := checkConfiguration(before, after); err == nil {
		t.Fatal("service identity change accepted")
	}
	changed.HostID = base.HostID
	changed.DeliveryObserver.GitHubBin = "gh"
	write(after, changed)
	if err := checkConfiguration(before, after); err == nil {
		t.Fatal("implicit executable accepted")
	}
}
